package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// newCityWindowCmd creates the "gc city-window" command that opens a
// multi-pane tmux window showing city agents.
func newCityWindowCmd(stdout, stderr io.Writer) *cobra.Command {
	var agents []string
	var windowName string
	var noMonitor bool

	cmd := &cobra.Command{
		Use:   "city-window",
		Short: "Open a multi-pane tmux window showing city agents",
		Long: `Create a tmux window with a multi-pane layout showing city agents.

Default layout: mayor on the left half, 3 panes on the right
(concierge, terraform-deploy-agent, terraform-code-agent).
Each pane auto-reconnects when sessions restart.

Press prefix+a to pop a picker menu to switch any pane to a
different agent.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if cmdCityWindow(agents, windowName, noMonitor, stdout, stderr) != 0 {
				return errExit
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&agents, "agents", []string{
		"mayor", "concierge", "terraform-deploy-agent", "terraform-code-agent",
	}, "comma-separated agent aliases")
	cmd.Flags().StringVar(&windowName, "name", "city", "tmux window name")
	cmd.Flags().BoolVar(&noMonitor, "no-monitor", false, "disable attention auto-focus")
	return cmd
}

func cmdCityWindow(agents []string, windowName string, noMonitor bool, stdout, stderr io.Writer) int {
	cityPath, err := resolveCity()
	if err != nil {
		fmt.Fprintf(stderr, "gc city-window: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	cfg, err := loadCityConfig(cityPath, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "gc city-window: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}

	cityName := loadedCityName(cfg, cityPath)

	// Resolve tmux socket name (same logic as the session provider).
	socketName := cfg.Session.Socket
	if socketName == "" {
		socketName = cityName
	}

	if len(agents) == 0 {
		fmt.Fprintln(stderr, "gc city-window: at least one agent required") //nolint:errcheck // best-effort stderr
		return 1
	}

	gcBin, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "gc city-window: resolving executable: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	gcBin, _ = filepath.Abs(gcBin)

	layout := &cityWindowLayout{
		gcBin:      gcBin,
		cityPath:   cityPath,
		socketName: socketName,
		windowName: windowName,
		agents:     agents,
		noMonitor:  noMonitor,
	}

	// Determine if we are inside tmux already.
	tmuxEnv := os.Getenv("TMUX")
	if tmuxEnv != "" {
		return layout.createWindow(stdout, stderr)
	}
	return layout.createSessionAndAttach(cityName, stdout, stderr)
}

// newInternalCityWindowPaneCmd creates the hidden "gc internal city-window-pane"
// subcommand that loops forever, reconnecting to an agent session.
func newInternalCityWindowPaneCmd(_, stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:    "city-window-pane <alias>",
		Short:  "Reconnection loop for a city-window pane",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			alias := args[0]
			cityPath, err := resolveCity()
			if err != nil {
				fmt.Fprintf(stderr, "gc internal city-window-pane: %v\n", err) //nolint:errcheck // best-effort stderr
				return errExit
			}
			gcBin, err := os.Executable()
			if err != nil {
				fmt.Fprintf(stderr, "gc internal city-window-pane: %v\n", err) //nolint:errcheck // best-effort stderr
				return errExit
			}
			gcBin, _ = filepath.Abs(gcBin)

			// Build list of alias variants to try (gc session attach resolves these).
			// The alias may be: a session ID (sapd-ux8), a template name (mayor),
			// a pool instance (terraform-code-agent-1), or a rig-qualified name.
			candidates := []string{
				alias,
				alias + "-1",
			}

			for {
				attached := false
				for _, candidate := range candidates {
					fmt.Fprintf(os.Stdout, "Connecting to %s...\n", candidate) //nolint:errcheck // best-effort stdout

					// Use tmux switch-client or respawn to avoid nesting.
					// Since we're already in a tmux pane, we use tmux respawn-pane
					// to replace this pane's process with the agent's tmux session.
					// This avoids the nested tmux problem entirely.
					//
					// Strategy: find the agent's tmux session name, then use
					// "tmux respawn-pane -k -t <our-pane> 'tmux -L <socket> attach -t <agent-session>'"
					// But that still nests. Instead, we pipe the agent's output here.
					//
					// Practical approach: use gc session peek --follow (streaming mode)
					// if available, otherwise fall back to attach with TMUX unset.
					cmd := exec.Command(gcBin, "--city", cityPath, "session", "attach", candidate)
					cmd.Stdin = os.Stdin
					cmd.Stdout = os.Stdout
					cmd.Stderr = os.Stderr
					// Unset TMUX to allow nested attach (standard tmux workaround).
					env := filterEnv(os.Environ(), "TMUX")
					// Also unset TMUX_PANE to avoid stale pane references.
					env = filterEnv(env, "TMUX_PANE")
					cmd.Env = env
					if err := cmd.Run(); err == nil {
						attached = true
						fmt.Fprintf(os.Stderr, "Session detached, reconnecting in 3s...\n") //nolint:errcheck // best-effort stderr
						break
					}
					// Try next candidate
				}
				if !attached {
					fmt.Fprintf(os.Stderr, "Session not found for %s, retrying in 5s...\n", alias) //nolint:errcheck // best-effort stderr
					time.Sleep(5 * time.Second)
				} else {
					time.Sleep(3 * time.Second)
				}
			}
		},
	}
}

// newInternalCityWindowMenuCmd creates the hidden "gc internal city-window-menu"
// subcommand that displays a tmux menu to switch any pane to a different agent.
func newInternalCityWindowMenuCmd(stdout, stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:    "city-window-menu",
		Short:  "Display agent picker menu for city-window",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cityPath, err := resolveCity()
			if err != nil {
				fmt.Fprintf(stderr, "gc internal city-window-menu: %v\n", err) //nolint:errcheck // best-effort stderr
				return errExit
			}
			cfg, err := loadCityConfig(cityPath, stderr)
			if err != nil {
				fmt.Fprintf(stderr, "gc internal city-window-menu: %v\n", err) //nolint:errcheck // best-effort stderr
				return errExit
			}

			cityName := loadedCityName(cfg, cityPath)
			socketName := cfg.Session.Socket
			if socketName == "" {
				socketName = cityName
			}

			gcBin, err := os.Executable()
			if err != nil {
				fmt.Fprintf(stderr, "gc internal city-window-menu: %v\n", err) //nolint:errcheck // best-effort stderr
				return errExit
			}
			gcBin, _ = filepath.Abs(gcBin)

			// Get active sessions only (not all configured agents).
			listCmd := exec.Command(gcBin, "--city", cityPath, "session", "list", "--state", "active")
			listOut, err := listCmd.Output()
			if err != nil {
				fmt.Fprintf(stderr, "gc internal city-window-menu: listing sessions: %v\n", err) //nolint:errcheck // best-effort stderr
				return errExit
			}

			// Parse session list output: columns are ID, TEMPLATE, STATE, ...
			// Extract both ID and TEMPLATE for display and connection.
			type sessionEntry struct {
				id       string
				template string
			}
			var sessions []sessionEntry
			seen := make(map[string]bool)
			for _, line := range strings.Split(string(listOut), "\n") {
				fields := strings.Fields(line)
				if len(fields) < 2 || fields[0] == "ID" {
					continue // skip header and empty lines
				}
				id := fields[0]
				template := fields[1]
				if !seen[id] {
					seen[id] = true
					sessions = append(sessions, sessionEntry{id: id, template: template})
				}
			}

			if len(sessions) == 0 {
				fmt.Fprintln(stderr, "gc internal city-window-menu: no agents found in city config") //nolint:errcheck // best-effort stderr
				return errExit
			}

			// Build tmux display-menu command. Use session ID for connection (reliable).
			menuArgs := []string{"-L", socketName, "display-menu", "-T", "Switch Agent"}
			for _, s := range sessions {
				// Display template name, connect by session ID
				paneCmd := fmt.Sprintf("%s --city %s internal city-window-pane %s",
					gcBin, cityPath, s.id)
				menuArgs = append(menuArgs, s.template, "", fmt.Sprintf("respawn-pane -k '%s'", paneCmd))
			}

			cmd := exec.Command("tmux", menuArgs...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				fmt.Fprintf(stderr, "gc internal city-window-menu: %v\n", err) //nolint:errcheck // best-effort stderr
				return errExit
			}

			_ = stdout // satisfy interface
			return nil
		},
	}
}

// newInternalCityWindowFocusCmd creates the hidden "gc internal city-window-focus"
// subcommand that checks right-side panes for pending interaction and auto-focuses.
func newInternalCityWindowFocusCmd(_, stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:    "city-window-focus",
		Short:  "Auto-focus pane with pending interaction",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cityPath, err := resolveCity()
			if err != nil {
				fmt.Fprintf(stderr, "gc internal city-window-focus: %v\n", err) //nolint:errcheck // best-effort stderr
				return errExit
			}
			cfg, err := loadCityConfig(cityPath, stderr)
			if err != nil {
				fmt.Fprintf(stderr, "gc internal city-window-focus: %v\n", err) //nolint:errcheck // best-effort stderr
				return errExit
			}

			cityName := loadedCityName(cfg, cityPath)
			socketName := cfg.Session.Socket
			if socketName == "" {
				socketName = cityName
			}

			// List panes in the current window.
			out, err := tmuxExec(socketName, "list-panes", "-F", "#{pane_id}")
			if err != nil {
				// Silently exit — timer hook may fire after window is closed.
				return nil
			}

			paneIDs := strings.Split(strings.TrimSpace(out), "\n")
			// Skip the first pane (left side / mayor) and check remaining.
			for _, paneID := range paneIDs[1:] {
				paneID = strings.TrimSpace(paneID)
				if paneID == "" {
					continue
				}
				// Capture a few lines from the pane to check for idle prompt.
				content, err := tmuxExec(socketName, "capture-pane", "-t", paneID, "-p", "-l", "5")
				if err != nil {
					continue
				}
				// Simple heuristic: if the pane content contains the prompt
				// idle indicator, it has pending interaction.
				if strings.Contains(content, "❯") {
					_ = tmuxRun(socketName, "select-pane", "-t", paneID)
					return nil
				}
			}
			return nil
		},
	}
}

// tmuxExec runs a tmux command with the given socket and returns stdout.
func tmuxExec(socket string, args ...string) (string, error) {
	fullArgs := []string{"-L", socket}
	fullArgs = append(fullArgs, args...)
	cmd := exec.Command("tmux", fullArgs...)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// tmuxRun runs a tmux command with the given socket, discarding output.
func tmuxRun(socket string, args ...string) error {
	fullArgs := []string{"-L", socket}
	fullArgs = append(fullArgs, args...)
	cmd := exec.Command("tmux", fullArgs...)
	return cmd.Run()
}

// filterEnv returns a copy of env with the named variable removed.
func filterEnv(env []string, name string) []string {
	prefix := name + "="
	result := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			result = append(result, e)
		}
	}
	return result
}
