package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/spf13/cobra"
)

func newDoltConfigCmd(_ io.Writer, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "dolt-config",
		Short:  "Internal Dolt config helpers",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	var (
		configFile   string
		host         string
		port         string
		dataDir      string
		logLevel     string
		archiveLevel int
		cityPath     string
		scopeDir     string
		issuePrefix  string
		doltDatabase string
	)

	writeManaged := &cobra.Command{
		Use:    "write-managed",
		Short:  "Write a managed Dolt SQL config file",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := writeManagedDoltConfigFile(configFile, host, port, dataDir, logLevel, archiveLevel, cityPath); err != nil {
				fmt.Fprintf(stderr, "gc dolt-config write-managed: %v\n", err) //nolint:errcheck
				return errExit
			}
			return nil
		},
	}
	writeManaged.Flags().StringVar(&configFile, "file", "", "path to dolt-config.yaml")
	writeManaged.Flags().StringVar(&host, "host", "", "listener host")
	writeManaged.Flags().StringVar(&port, "port", "", "listener port")
	writeManaged.Flags().StringVar(&dataDir, "data-dir", "", "Dolt data directory")
	writeManaged.Flags().StringVar(&logLevel, "log-level", "warning", "Dolt log level")
	writeManaged.Flags().IntVar(&archiveLevel, "archive-level", 0, "Dolt auto_gc archive_level (0=off, 1=on)")
	writeManaged.Flags().StringVar(&cityPath, "city", "", "city root for [dolt] config lookup (optional)")
	_ = writeManaged.MarkFlagRequired("file")
	_ = writeManaged.MarkFlagRequired("host")
	_ = writeManaged.MarkFlagRequired("port")
	_ = writeManaged.MarkFlagRequired("data-dir")
	cmd.AddCommand(writeManaged)

	normalizeScope := &cobra.Command{
		Use:    "normalize-scope",
		Short:  "Normalize canonical bd scope files after backend init",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if cityPath == "" {
				fmt.Fprintln(stderr, "gc dolt-config normalize-scope: missing --city") //nolint:errcheck
				return errExit
			}
			if scopeDir == "" {
				fmt.Fprintln(stderr, "gc dolt-config normalize-scope: missing --dir") //nolint:errcheck
				return errExit
			}
			if issuePrefix == "" {
				fmt.Fprintln(stderr, "gc dolt-config normalize-scope: missing --prefix") //nolint:errcheck
				return errExit
			}
			if err := normalizeCanonicalBdScopeFilesForInit(cityPath, scopeDir, issuePrefix, doltDatabase); err != nil {
				fmt.Fprintf(stderr, "gc dolt-config normalize-scope: %v\n", err) //nolint:errcheck
				return errExit
			}
			if err := removeScopeLocalDoltServerArtifacts(scopeDir); err != nil {
				fmt.Fprintf(stderr, "gc dolt-config normalize-scope: %v\n", err) //nolint:errcheck
				return errExit
			}
			return nil
		},
	}
	normalizeScope.Flags().StringVar(&cityPath, "city", "", "city root")
	normalizeScope.Flags().StringVar(&scopeDir, "dir", "", "scope root to normalize")
	normalizeScope.Flags().StringVar(&issuePrefix, "prefix", "", "scope issue prefix")
	normalizeScope.Flags().StringVar(&doltDatabase, "dolt-database", "", "pinned Dolt database")
	_ = normalizeScope.MarkFlagRequired("city")
	_ = normalizeScope.MarkFlagRequired("dir")
	_ = normalizeScope.MarkFlagRequired("prefix")
	cmd.AddCommand(normalizeScope)
	return cmd
}

func writeManagedDoltConfigFile(path, host, port, dataDir, logLevel string, archiveLevel int, cityPath string) error {
	if path == "" {
		return fmt.Errorf("missing --file")
	}
	if host == "" {
		return fmt.Errorf("missing --host")
	}
	if port == "" {
		return fmt.Errorf("missing --port")
	}
	if dataDir == "" {
		return fmt.Errorf("missing --data-dir")
	}
	if logLevel == "" {
		logLevel = "warning"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	autoGcEnabled := resolveDoltAutoGc(cityPath)
	autocommit := resolveDoltAutocommit(cityPath)
	resolvedArchive := resolveDoltArchiveLevelForWriter(cityPath, archiveLevel)
	autoGcEnableYAML := "false"
	autoGcSysVarYAML := "OFF"
	if autoGcEnabled {
		autoGcEnableYAML = "true"
		autoGcSysVarYAML = "ON"
	}
	autocommitYAML := "false"
	if autocommit {
		autocommitYAML = "true"
	}
	waitTimeout := managedDoltWaitTimeout()
	waitTimeoutLine := ""
	if waitTimeout > 0 {
		waitTimeoutLine = fmt.Sprintf("  wait_timeout: %q\n", strconv.Itoa(waitTimeout))
	}
	content := fmt.Sprintf(`# Dolt SQL server configuration — managed by gc-beads-bd
# Do not edit manually; changes are overwritten on each server start.
#
# To customize, set environment variables (highest priority):
#   GC_DOLT_PORT, GC_DOLT_HOST, GC_DOLT_USER, GC_DOLT_PASSWORD, GC_DOLT_LOGLEVEL
#   GC_DOLT_AUTO_GC                  ("true"/"on"/"enabled" or "false"/"off"/"disabled")
#   GC_DOLT_AUTOCOMMIT               ("on"/"true" or "batch"/"off"/"false")
#   GC_DOLT_AUTO_GC_ARCHIVE_LEVEL    (0=off, 1=on)
#
# Or set per-city in city.toml [dolt] section:
#   [dolt]
#   auto_gc = "true"          # default: "true"
#   autocommit = "batch"      # default: "batch" (off; group writes)
#   archive_level = 0         # default: 0
#
# Or globally in ~/.gc/dolt-config.yaml.
#
# Defaults: auto_gc=true, autocommit=batch (off), archive_level=0.
#
# Note on auto_gc: dolt#10944's load-avg gating means upstream auto_gc may
# not fire on busy machines. Pair with 'gc dolt compact' for guaranteed
# storage cleanup. See gastownhall/gascity#1918, #1200, #1977 for context.

log_level: %s

listener:
  port: %s
  host: %s
  max_connections: 1000
  back_log: 50
  max_connections_timeout_millis: 5000
  read_timeout_millis: 300000
  write_timeout_millis: 300000

data_dir: %q

behavior:
  autocommit: %s
  auto_gc_behavior:
    enable: %s
    archive_level: %d

# Managed Gas City workloads generate short-lived probe and metadata queries.
# Dolt's persistent stats worker can make those tiny databases grow large
# stats stores and burn CPU, especially on macOS endpoint-managed machines.
# Keep stats disabled for managed servers; use explicit gc dolt maintenance
# commands for storage cleanup instead of background workers.
system_variables:
  dolt_auto_gc_enabled: %q
  dolt_stats_enabled: "OFF"
  dolt_stats_gc_enabled: "OFF"
  dolt_stats_memory_only: "ON"
  dolt_stats_paused: "ON"
%s`, logLevel, port, host, dataDir, autocommitYAML, autoGcEnableYAML, resolvedArchive, autoGcSysVarYAML, waitTimeoutLine)
	if err := fsys.WriteFileAtomic(fsys.OSFS{}, path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}
	return nil
}

// resolveDoltAutoGc returns whether Dolt auto_gc should be enabled, with
// resolution priority: GC_DOLT_AUTO_GC env → city.toml [dolt] auto_gc →
// global ~/.gc/city.toml [dolt] auto_gc → default (true).
func resolveDoltAutoGc(cityPath string) bool {
	if v, ok := parseAutoGcValue(os.Getenv("GC_DOLT_AUTO_GC")); ok {
		return v
	}
	if cityPath != "" {
		if cfg, err := config.Load(fsys.OSFS{}, filepath.Join(cityPath, "city.toml")); err == nil && cfg != nil {
			if v, ok := parseAutoGcValue(cfg.Dolt.AutoGc); ok {
				return v
			}
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if cfg, err := config.Load(fsys.OSFS{}, filepath.Join(home, ".gc", "city.toml")); err == nil && cfg != nil {
			if v, ok := parseAutoGcValue(cfg.Dolt.AutoGc); ok {
				return v
			}
		}
	}
	return true
}

// resolveDoltAutocommit returns whether Dolt autocommit should be true (per-statement)
// or false (batch). Resolution: GC_DOLT_AUTOCOMMIT env → city.toml [dolt]
// autocommit → global ~/.gc/city.toml [dolt] autocommit → default (false / batch).
func resolveDoltAutocommit(cityPath string) bool {
	if v, ok := parseAutocommitValue(os.Getenv("GC_DOLT_AUTOCOMMIT")); ok {
		return v
	}
	if cityPath != "" {
		if cfg, err := config.Load(fsys.OSFS{}, filepath.Join(cityPath, "city.toml")); err == nil && cfg != nil {
			if v, ok := parseAutocommitValue(cfg.Dolt.Autocommit); ok {
				return v
			}
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if cfg, err := config.Load(fsys.OSFS{}, filepath.Join(home, ".gc", "city.toml")); err == nil && cfg != nil {
			if v, ok := parseAutocommitValue(cfg.Dolt.Autocommit); ok {
				return v
			}
		}
	}
	return false
}

// resolveDoltArchiveLevelForWriter returns the auto_gc archive_level used when
// writing the managed dolt-config.yaml. Priority: GC_DOLT_AUTO_GC_ARCHIVE_LEVEL
// env → cmd-line --archive-level flag value (which the bd script can populate
// from GC_DOLT_ARCHIVE_LEVEL). city.toml [dolt] archive_level read by the
// caller and threaded via cmdDefault.
func resolveDoltArchiveLevelForWriter(_ string, cmdDefault int) int {
	if raw := strings.TrimSpace(os.Getenv("GC_DOLT_AUTO_GC_ARCHIVE_LEVEL")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			return n
		}
	}
	return cmdDefault
}

// parseAutoGcValue parses a string into a bool for auto_gc. Accepted (case-
// insensitive, whitespace-trimmed): "true"/"on"/"enabled"/"yes"/"1" → true;
// "false"/"off"/"disabled"/"no"/"0" → false. Unrecognized → (false, false).
func parseAutoGcValue(raw string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "on", "enabled", "yes", "1":
		return true, true
	case "false", "off", "disabled", "no", "0":
		return false, true
	}
	return false, false
}

// parseAutocommitValue parses a string into a bool for autocommit. Accepted
// (case-insensitive, whitespace-trimmed): "on"/"true"/"yes"/"1" → true;
// "batch"/"off"/"false"/"no"/"0" → false. Unrecognized → (false, false).
func parseAutocommitValue(raw string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "on", "true", "yes", "1":
		return true, true
	case "batch", "off", "false", "no", "0":
		return false, true
	}
	return false, false
}

func managedDoltWaitTimeout() int {
	const defaultWaitTimeout = 30
	raw := os.Getenv("GC_DOLT_WAIT_TIMEOUT")
	if raw == "" {
		return defaultWaitTimeout
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return defaultWaitTimeout
	}
	if n < 0 {
		return 0
	}
	return n
}
