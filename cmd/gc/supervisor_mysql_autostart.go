// Supervisor mysqld autostart — when the supervisor wakes up and finds
// MySQL-backed cities in the registry whose mysql.* connection target is
// loopback, ensure a local mysqld is reachable. If not, try a short list of
// well-known start commands (brew services, launchctl, systemctl, mysqld_safe)
// before giving up.
//
// Skipped entirely when GC_SUPERVISOR_NO_MYSQL_AUTOSTART=1 is set, or when no
// registered city declares backend=mysql, or when every mysql city points at
// a non-loopback host (operator owns those).
//
// Failures here are non-fatal: the supervisor continues on. Cities depending
// on mysql will fail their own probes and surface clear errors.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads/contract"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/gastownhall/gascity/internal/supervisor"
)

// mysqlAutostartEnv disables the autostart entirely when set to a
// truthy value. Useful for CI / test sandboxes.
const mysqlAutostartEnv = "GC_SUPERVISOR_NO_MYSQL_AUTOSTART"

// mysqlAutostartCommand describes one strategy for starting a local mysqld.
// runner is invoked in order; the first to succeed wins.
type mysqlAutostartCommand struct {
	name    string
	command string
	args    []string
}

// mysqlAutostartRunner is a test seam for invoking a command. Production uses
// realRunMysqlAutostartCommand which exec's the binary.
var mysqlAutostartRunner = realRunMysqlAutostartCommand

// mysqlAutostartProbe is a test seam for "is mysqld reachable?". Production
// dials the address.
var mysqlAutostartProbe = realProbeMysqlReachable

// supervisorEnsureMysqldRunning scans registered cities for mysql backends and
// ensures a local mysqld is listening on each unique loopback host:port.
// Best-effort: any failure is logged to stderr and ignored.
func supervisorEnsureMysqldRunning(reg *supervisor.Registry, stdout, stderr io.Writer) {
	if isMysqlAutostartDisabled() {
		return
	}
	entries, err := reg.List()
	if err != nil {
		fmt.Fprintf(stderr, "gc supervisor: mysql autostart: registry list: %v\n", err) //nolint:errcheck
		return
	}
	targets := collectLoopbackMysqlTargets(entries)
	if len(targets) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, addr := range targets {
		host, port := splitHostPort(addr)
		if mysqlAutostartProbe(ctx, host, port) {
			continue
		}
		fmt.Fprintf(stdout, "gc supervisor: mysqld at %s not reachable; attempting autostart\n", addr) //nolint:errcheck
		if err := startLocalMysqld(ctx, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "gc supervisor: mysql autostart: %v\n", err) //nolint:errcheck
			continue
		}
		// Wait up to 15s for mysqld to become reachable after start.
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if mysqlAutostartProbe(ctx, host, port) {
				fmt.Fprintf(stdout, "gc supervisor: mysqld at %s now reachable\n", addr) //nolint:errcheck
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if !mysqlAutostartProbe(ctx, host, port) {
			fmt.Fprintf(stderr, "gc supervisor: mysql autostart: mysqld at %s did not become reachable in 15s\n", addr) //nolint:errcheck
		}
	}
}

// isMysqlAutostartDisabled checks the env override. Empty / "0" / "false" are
// treated as not-disabled.
func isMysqlAutostartDisabled() bool {
	val := strings.ToLower(strings.TrimSpace(os.Getenv(mysqlAutostartEnv)))
	switch val {
	case "", "0", "false", "off", "no":
		return false
	}
	return true
}

// collectLoopbackMysqlTargets reads each registered city's metadata.json and
// returns the de-duplicated set of mysql host:port addresses that point at
// loopback. Non-loopback targets (operator-managed) are skipped.
func collectLoopbackMysqlTargets(entries []supervisor.CityEntry) []string {
	seen := map[string]struct{}{}
	var addrs []string
	for _, e := range entries {
		path := filepath.Join(e.Path, ".beads", "metadata.json")
		state, ok, err := contract.LoadMetadataState(fsys.OSFS{}, path)
		if err != nil || !ok {
			continue
		}
		if state.Backend != "mysql" {
			continue
		}
		host := strings.TrimSpace(state.MysqlHost)
		port := strings.TrimSpace(state.MysqlPort)
		if !isLoopbackHost(host) {
			continue
		}
		addr := net.JoinHostPort(host, port)
		if _, dup := seen[addr]; dup {
			continue
		}
		seen[addr] = struct{}{}
		addrs = append(addrs, addr)
	}
	return addrs
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "127.0.0.1", "::1", "localhost", "":
		return true
	}
	return false
}

func splitHostPort(addr string) (host, port string) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, "3306"
	}
	return host, port
}

// realProbeMysqlReachable dials host:port and (if connection succeeds) does
// a MySQL handshake ping with a 2s timeout.
func realProbeMysqlReachable(ctx context.Context, host, port string) bool {
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return false
	}
	_ = conn.Close()
	// Bare TCP reachability is enough — mysqld autostart's job is "is
	// something listening on the port?", not "is the credentials path good?".
	// The latter is bd's responsibility.
	return true
}

// startLocalMysqld tries each platform-appropriate start command in order.
// Success means at least one command exited 0 — we then poll for reachability
// in the caller.
func startLocalMysqld(ctx context.Context, stdout, stderr io.Writer) error {
	cmds := mysqldStartCommands()
	if len(cmds) == 0 {
		return errors.New("no autostart strategy available on this platform")
	}
	var lastErr error
	for _, c := range cmds {
		fmt.Fprintf(stdout, "gc supervisor: trying %s\n", c.name) //nolint:errcheck
		if err := mysqlAutostartRunner(ctx, c); err == nil {
			return nil
		} else {
			lastErr = err
			fmt.Fprintf(stderr, "gc supervisor: %s: %v\n", c.name, err) //nolint:errcheck
		}
	}
	if lastErr != nil {
		return fmt.Errorf("all autostart strategies failed; last error: %w", lastErr)
	}
	return errors.New("all autostart strategies failed")
}

// mysqldStartCommands returns the ordered list of start strategies for the
// current OS. Order matters: prefer the lightest-touch tool first.
func mysqldStartCommands() []mysqlAutostartCommand {
	switch runtime.GOOS {
	case "darwin":
		return []mysqlAutostartCommand{
			{name: "brew services start mysql", command: "brew", args: []string{"services", "start", "mysql"}},
			{name: "brew services start mariadb", command: "brew", args: []string{"services", "start", "mariadb"}},
			{name: "launchctl load homebrew.mxcl.mysql", command: "launchctl", args: []string{"load", "-w", expandHome("~/Library/LaunchAgents/homebrew.mxcl.mysql.plist")}},
		}
	case "linux":
		return []mysqlAutostartCommand{
			{name: "systemctl start mysql", command: "systemctl", args: []string{"start", "mysql"}},
			{name: "systemctl start mariadb", command: "systemctl", args: []string{"start", "mariadb"}},
			{name: "service mysql start", command: "service", args: []string{"mysql", "start"}},
		}
	}
	return nil
}

func expandHome(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~/"))
}

// realRunMysqlAutostartCommand exec's the chosen command with a context
// timeout. We treat exit-0 as success regardless of stdout/stderr — operators
// using brew services may see "already started" output that's still success.
func realRunMysqlAutostartCommand(ctx context.Context, c mysqlAutostartCommand) error {
	if _, err := exec.LookPath(c.command); err != nil {
		return fmt.Errorf("%s not found on PATH", c.command)
	}
	cmd := exec.CommandContext(ctx, c.command, c.args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %v: %s", c.command, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// validatePortString is a small helper used by tests to confirm a target
// looks port-shaped before we hand it to net.SplitHostPort.
func validatePortString(s string) bool {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return false
	}
	return v > 0 && v <= 65535
}
