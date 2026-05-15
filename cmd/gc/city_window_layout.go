package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// cityWindowLayout holds the parameters for creating the tmux window layout.
type cityWindowLayout struct {
	gcBin      string
	cityPath   string
	socketName string
	windowName string
	agents     []string
	noMonitor  bool
}

// paneCommand returns the shell command string for a pane running an agent.
func (l *cityWindowLayout) paneCommand(alias string) string {
	return fmt.Sprintf("%s --city %s internal city-window-pane %s", l.gcBin, l.cityPath, alias)
}

// createWindow creates a new tmux window in the current session with the
// multi-pane layout. Called when already inside tmux.
func (l *cityWindowLayout) createWindow(stdout, stderr io.Writer) int {
	// Check if window name already exists.
	out, err := tmuxExec(l.socketName, "list-windows", "-F", "#{window_name}")
	if err == nil {
		for _, name := range strings.Split(out, "\n") {
			if strings.TrimSpace(name) == l.windowName {
				fmt.Fprintf(stderr, "gc city-window: window %q already exists, selecting it\n", l.windowName) //nolint:errcheck // best-effort stderr
				_ = tmuxRun(l.socketName, "select-window", "-t", l.windowName)
				return 0
			}
		}
	}

	// Create new window with the first agent.
	err = tmuxRun(l.socketName, "new-window", "-n", l.windowName, l.paneCommand(l.agents[0]))
	if err != nil {
		fmt.Fprintf(stderr, "gc city-window: creating window: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}

	if code := l.setupPanes(stderr); code != 0 {
		return code
	}

	fmt.Fprintf(stdout, "City window %q created with %d pane(s)\n", l.windowName, len(l.agents)) //nolint:errcheck // best-effort stdout
	return 0
}

// createSessionAndAttach creates a new tmux session and attaches to it.
// Called when not inside tmux.
func (l *cityWindowLayout) createSessionAndAttach(cityName string, stdout, stderr io.Writer) int {
	sessionName := "gc-" + cityName

	// Check if session already exists.
	if err := tmuxRun(l.socketName, "has-session", "-t", sessionName); err == nil {
		// Session exists — check for our window.
		out, _ := tmuxExec(l.socketName, "list-windows", "-t", sessionName, "-F", "#{window_name}")
		for _, name := range strings.Split(out, "\n") {
			if strings.TrimSpace(name) == l.windowName {
				fmt.Fprintf(stdout, "Attaching to existing session %q window %q\n", sessionName, l.windowName) //nolint:errcheck // best-effort stdout
				_ = tmuxRun(l.socketName, "select-window", "-t", sessionName+":"+l.windowName)
				return l.attachSession(sessionName, stderr)
			}
		}
		// Session exists but window doesn't — create window in it.
		err = tmuxRun(l.socketName, "new-window", "-t", sessionName, "-n", l.windowName, l.paneCommand(l.agents[0]))
		if err != nil {
			fmt.Fprintf(stderr, "gc city-window: creating window in existing session: %v\n", err) //nolint:errcheck // best-effort stderr
			return 1
		}
		if code := l.setupPanes(stderr); code != 0 {
			return code
		}
		return l.attachSession(sessionName, stderr)
	}

	// Create new session with the first agent in the first pane.
	err := tmuxRun(l.socketName, "new-session", "-d", "-s", sessionName, "-n", l.windowName, l.paneCommand(l.agents[0]))
	if err != nil {
		fmt.Fprintf(stderr, "gc city-window: creating session: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}

	// Set a different prefix for the city-window session to avoid conflicts
	// with nested agent sessions. Use Ctrl+g as the outer prefix.
	// Inner sessions keep Ctrl+b, so:
	//   Ctrl+g = city-window commands (switch pane, picker menu via Ctrl+g a)
	//   Ctrl+b = agent session commands (scroll history, copy mode, etc.)
	_ = tmuxRun(l.socketName, "set-option", "-t", sessionName, "prefix", "C-g")
	_ = tmuxRun(l.socketName, "set-option", "-t", sessionName, "prefix2", "None")
	// Enable mouse support for trackpad scrolling, pane selection, and resizing.
	_ = tmuxRun(l.socketName, "set-option", "-t", sessionName, "mouse", "on")

	if code := l.setupPanes(stderr); code != 0 {
		return code
	}

	fmt.Fprintf(stdout, "City window %q created in session %q with %d pane(s)\n", l.windowName, sessionName, len(l.agents)) //nolint:errcheck // best-effort stdout
	return l.attachSession(sessionName, stderr)
}

// setupPanes splits the window and configures panes for all agents beyond the first.
func (l *cityWindowLayout) setupPanes(stderr io.Writer) int {
	// Target the window we just created for split commands.
	windowTarget := l.windowName

	// Split: create right half (50%) for agent[1] if there are more agents.
	if len(l.agents) > 1 {
		err := tmuxRun(l.socketName, "split-window", "-t", windowTarget, "-h", "-p", "50", l.paneCommand(l.agents[1]))
		if err != nil {
			fmt.Fprintf(stderr, "gc city-window: splitting window: %v\n", err) //nolint:errcheck // best-effort stderr
			return 1
		}
	}

	// For agents[2:], split the right pane vertically.
	for i := 2; i < len(l.agents); i++ {
		err := tmuxRun(l.socketName, "split-window", "-t", windowTarget, "-v", l.paneCommand(l.agents[i]))
		if err != nil {
			fmt.Fprintf(stderr, "gc city-window: splitting pane for %s: %v\n", l.agents[i], err) //nolint:errcheck // best-effort stderr
			return 1
		}
	}

	// Set remain-on-exit for all panes so they don't disappear on crash.
	_ = tmuxRun(l.socketName, "set-option", "-t", windowTarget, "remain-on-exit", "on")

	// Set up keybinding: Ctrl+g a triggers the agent picker menu.
	menuCmd := fmt.Sprintf("%s --city %s internal city-window-menu", l.gcBin, l.cityPath)
	_ = tmuxRun(l.socketName, "bind-key", "-T", "prefix", "a", "run-shell", menuCmd)

	// Set up attention monitor (timer hook every 3s) unless disabled.
	if !l.noMonitor {
		focusCmd := fmt.Sprintf("%s --city %s internal city-window-focus", l.gcBin, l.cityPath)
		// Use set-hook to run the focus command periodically. The hook fires
		// on a timer via a background shell loop approach — we use a simpler
		// approach: a periodic tmux hook that runs every few seconds.
		hookCmd := fmt.Sprintf("run-shell -b '%s'", focusCmd)
		_ = tmuxRun(l.socketName, "set-hook", "-g", "periodic-tick-3", hookCmd)
	}

	// Select the first pane (left side) as default focus.
	_ = tmuxRun(l.socketName, "select-pane", "-t", windowTarget+".0")

	return 0
}

// attachSession attaches to the tmux session interactively.
func (l *cityWindowLayout) attachSession(sessionName string, stderr io.Writer) int {
	cmd := exec.Command("tmux", "-L", l.socketName, "attach-session", "-t", sessionName)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(stderr, "gc city-window: attaching to session: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	return 0
}
