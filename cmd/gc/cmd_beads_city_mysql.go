// gc beads city use-mysql — switch a city (and its inherited rigs) to a
// MySQL beads backend.
//
// This file is intentionally modular: the dolt path in cmd_beads_city.go is
// untouched. All mysql-specific logic — flag parsing, database provisioning,
// metadata/config writing, and rig cascade — lives here. The shared bits
// reused from the dolt path are the file-snapshot/rollback machinery and
// `ensureBeadsDir`. Anything dolt-shaped (port artifacts, managed runtime,
// inherited dolt config) is bypassed because mysql cities have none of it.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/spf13/cobra"

	"github.com/gastownhall/gascity/internal/beads/contract"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
)

type cityMysqlOptions struct {
	Host         string
	Port         string
	User         string
	Password     string
	Database     string
	DryRun       bool
	SkipBdInit   bool // testing seam: skip the bd init invocation
	SkipDBCreate bool // testing seam: skip CREATE DATABASE
}

// mysqlProvisioner is the indirection used by tests to swap out the real
// CREATE DATABASE call. Production callers use realMysqlProvisioner.
type mysqlProvisioner func(ctx context.Context, host, port, user, password, database string) error

// bdMysqlInitFn is the indirection used by tests for `bd init --backend=mysql`.
// Production callers use runBdInitMysql.
type bdMysqlInitFn func(scopeRoot, prefix, database, host, port, user string) error

var (
	defaultMysqlProvisioner mysqlProvisioner = realMysqlProvisioner
	defaultBdMysqlInit      bdMysqlInitFn    = runBdInitMysql
)

func newBeadsCityUseMysqlCmd(stdout, stderr io.Writer) *cobra.Command {
	var opts cityMysqlOptions
	cmd := &cobra.Command{
		Use:   "use-mysql",
		Short: "Set the city endpoint to a MySQL backend",
		Long: `Switch the city (and any inherited rigs) to a bd MySQL backend.

Creates the target database if it does not exist, writes canonical metadata.json
and config.yaml entries, and runs ` + "`bd init --backend=mysql`" + ` to seed the
config table. Inherited rigs share the city's database — each rig keeps its own
issue prefix.

This command does NOT modify cities that use a Dolt backend; run it only on a
fresh city or after explicit migration. Use --dry-run to preview the change set.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if cmdBeadsCityUseMysql(opts, stdout, stderr) != 0 {
				return errExit
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.Host, "host", "127.0.0.1", "MySQL server host")
	cmd.Flags().StringVar(&opts.Port, "port", "3306", "MySQL server port")
	cmd.Flags().StringVar(&opts.User, "user", "root", "MySQL server user")
	cmd.Flags().StringVar(&opts.Password, "password", "", "MySQL server password (read from $GC_MYSQL_PASSWORD if unset)")
	cmd.Flags().StringVar(&opts.Database, "database", "", "MySQL database name (required; also used by inherited rigs)")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "show the canonical changes without writing files or touching MySQL")
	return cmd
}

func cmdBeadsCityUseMysql(opts cityMysqlOptions, stdout, stderr io.Writer) int {
	cityPath, err := resolveCity()
	if err != nil {
		fmt.Fprintf(stderr, "gc beads city use-mysql: %v\n", err) //nolint:errcheck
		return 1
	}
	return doBeadsCityUseMysql(fsys.OSFS{}, cityPath, opts, stdout, stderr)
}

//nolint:unparam // FS seam intentional for tests
func doBeadsCityUseMysql(fs fsys.FS, cityPath string, opts cityMysqlOptions, stdout, stderr io.Writer) int {
	const name = "gc beads city use-mysql"

	if err := validateMysqlOptions(&opts); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err) //nolint:errcheck
		return 1
	}
	if !cityUsesBdStoreContract(cityPath) {
		fmt.Fprintf(stderr, "%s: only supported for bd-backed beads providers\n", name) //nolint:errcheck
		return 1
	}

	cfg, err := loadCityConfigFS(fs, filepath.Join(cityPath, "city.toml"), stderr)
	if err != nil {
		fmt.Fprintf(stderr, "%s: loading config: %v\n", name, err) //nolint:errcheck
		return 1
	}
	resolveRigPaths(cityPath, cfg.Rigs)

	cityState := buildMysqlCityState(cfg, opts, contract.EndpointOriginManagedCity)
	rigPlans := planMysqlRigUpdates(cfg.Rigs, cityState)

	if opts.DryRun {
		printMysqlDryRun(stdout, cityPath, cityState, rigPlans)
		return 0
	}

	if !opts.SkipDBCreate {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := defaultMysqlProvisioner(ctx, opts.Host, opts.Port, opts.User, opts.Password, opts.Database); err != nil {
			fmt.Fprintf(stderr, "%s: provisioning database: %v\n", name, err) //nolint:errcheck
			return 1
		}
	}

	snapshots, err := snapshotMysqlScopeFiles(fs, cityPath, rigPlans)
	if err != nil {
		fmt.Fprintf(stderr, "%s: snapshot canonical files: %v\n", name, err) //nolint:errcheck
		return 1
	}

	cityPrefix := config.EffectiveHQPrefix(cfg)

	// Order matters: bd init writes its own metadata.json/config.yaml, so we
	// run it FIRST and then overwrite with canonical shapes. Running our
	// writers first then bd init would clobber the canonical state.
	if !opts.SkipBdInit {
		if err := defaultBdMysqlInit(cityPath, cityPrefix, opts.Database, opts.Host, opts.Port, opts.User); err != nil {
			writeMysqlRollbackError(fs, stderr, snapshots, name, "bd init --backend=mysql for city", err)
			return 1
		}
	}
	if err := writeMysqlScope(fs, cityPath, cityState, cityPrefix, opts); err != nil {
		writeMysqlRollbackError(fs, stderr, snapshots, name, "writing canonical city scope", err)
		return 1
	}

	for _, plan := range rigPlans {
		if !plan.Update {
			continue
		}
		rigPrefix := plan.Rig.EffectivePrefix()
		if !opts.SkipBdInit {
			if err := defaultBdMysqlInit(plan.Rig.Path, rigPrefix, opts.Database, opts.Host, opts.Port, opts.User); err != nil {
				writeMysqlRollbackError(fs, stderr, snapshots, name, "bd init --backend=mysql for rig", err)
				return 1
			}
		}
		if err := writeMysqlScope(fs, plan.Rig.Path, plan.Target, rigPrefix, opts); err != nil {
			writeMysqlRollbackError(fs, stderr, snapshots, name, "writing canonical rig scope", err)
			return 1
		}
	}

	if err := stopManagedDoltIfRunning(cityPath, stderr); err != nil {
		// Stop failures are non-fatal — the metadata flip is durable; surface
		// the warning but don't roll back.
		fmt.Fprintf(stderr, "%s: warning: stopping managed dolt: %v\n", name, err) //nolint:errcheck
	}
	// Clean up dolt-runtime side-files (dolt/, dolt-server.port) left behind
	// by gc init's brief dolt path. Best-effort: errors are surfaced as
	// warnings but don't fail the command, mirroring stopManagedDolt above.
	if err := clearDoltSideFiles(cityPath, rigPlans); err != nil {
		fmt.Fprintf(stderr, "%s: warning: cleaning dolt side files: %v\n", name, err) //nolint:errcheck
	}

	printMysqlResult(stdout, cityPath, opts, rigPlans)
	return 0
}

func validateMysqlOptions(opts *cityMysqlOptions) error {
	if opts.Password == "" {
		opts.Password = strings.TrimSpace(os.Getenv("GC_MYSQL_PASSWORD"))
	}
	if strings.TrimSpace(opts.Database) == "" {
		return fmt.Errorf("--database is required")
	}
	if strings.TrimSpace(opts.Host) == "" {
		return fmt.Errorf("--host is required")
	}
	if strings.TrimSpace(opts.Port) == "" {
		return fmt.Errorf("--port is required")
	}
	if port, err := strconv.Atoi(opts.Port); err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid --port %q (must be 1..65535)", opts.Port)
	}
	if strings.TrimSpace(opts.User) == "" {
		return fmt.Errorf("--user is required")
	}
	if !validMysqlIdentifier(opts.Database) {
		return fmt.Errorf("invalid --database %q (must match [A-Za-z0-9_]+)", opts.Database)
	}
	return nil
}

func validMysqlIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_':
		default:
			return false
		}
	}
	return true
}

func buildMysqlCityState(cfg *config.City, opts cityMysqlOptions, origin contract.EndpointOrigin) contract.ConfigState {
	return contract.ConfigState{
		IssuePrefix:    config.EffectiveHQPrefix(cfg),
		Backend:        "mysql",
		EndpointOrigin: origin,
		EndpointStatus: contract.EndpointStatusVerified,
		MysqlHost:      strings.TrimSpace(opts.Host),
		MysqlPort:      strings.TrimSpace(opts.Port),
		MysqlUser:      strings.TrimSpace(opts.User),
		MysqlDatabase:  strings.TrimSpace(opts.Database),
	}
}

func planMysqlRigUpdates(rigs []config.Rig, cityState contract.ConfigState) []cityRigEndpointPlan {
	plans := make([]cityRigEndpointPlan, 0, len(rigs))
	for i := range rigs {
		target := contract.ConfigState{
			IssuePrefix:    rigs[i].EffectivePrefix(),
			Backend:        "mysql",
			EndpointOrigin: contract.EndpointOriginInheritedCity,
			EndpointStatus: contract.EndpointStatusVerified,
			MysqlHost:      cityState.MysqlHost,
			MysqlPort:      cityState.MysqlPort,
			MysqlUser:      cityState.MysqlUser,
			MysqlDatabase:  cityState.MysqlDatabase,
		}
		plans = append(plans, cityRigEndpointPlan{
			Rig:    rigs[i],
			Target: target,
			Update: true,
		})
	}
	return plans
}

func writeMysqlScope(fs fsys.FS, scopeRoot string, state contract.ConfigState, prefix string, opts cityMysqlOptions) error {
	beadsDir := filepath.Join(scopeRoot, ".beads")
	if err := ensureBeadsDir(fs, beadsDir); err != nil {
		return err
	}
	cfgState := state
	cfgState.IssuePrefix = prefix
	if _, err := contract.EnsureCanonicalConfig(fs, filepath.Join(beadsDir, "config.yaml"), cfgState); err != nil {
		return fmt.Errorf("config.yaml: %w", err)
	}
	metaState := contract.MetadataState{
		Database:      opts.Database,
		Backend:       "mysql",
		MysqlHost:     state.MysqlHost,
		MysqlPort:     state.MysqlPort,
		MysqlUser:     state.MysqlUser,
		MysqlDatabase: state.MysqlDatabase,
	}
	if _, err := contract.EnsureCanonicalMetadata(fs, filepath.Join(beadsDir, "metadata.json"), metaState); err != nil {
		return fmt.Errorf("metadata.json: %w", err)
	}
	return nil
}

func snapshotMysqlScopeFiles(fs fsys.FS, cityPath string, plans []cityRigEndpointPlan) ([]fileSnapshot, error) {
	scopes := []string{cityPath}
	for _, plan := range plans {
		if plan.Update {
			scopes = append(scopes, plan.Rig.Path)
		}
	}
	snaps := make([]fileSnapshot, 0, len(scopes)*2)
	for _, scope := range scopes {
		for _, name := range []string{"config.yaml", "metadata.json"} {
			snap, err := snapshotOptionalFile(fs, filepath.Join(scope, ".beads", name))
			if err != nil {
				return nil, err
			}
			snaps = append(snaps, snap)
		}
	}
	return snaps, nil
}

func writeMysqlRollbackError(fs fsys.FS, stderr io.Writer, snapshots []fileSnapshot, name, action string, cause error) {
	if restoreErr := restoreSnapshots(fs, snapshots); restoreErr != nil {
		fmt.Fprintf(stderr, "%s: %s: %v (rollback failed: %v)\n", name, action, cause, restoreErr) //nolint:errcheck
		return
	}
	fmt.Fprintf(stderr, "%s: %s: %v\n", name, action, cause) //nolint:errcheck
}

func printMysqlDryRun(stdout io.Writer, cityPath string, cityState contract.ConfigState, plans []cityRigEndpointPlan) {
	fmt.Fprintln(stdout, "WOULD UPDATE: city endpoint to mysql")                                                                                          //nolint:errcheck
	fmt.Fprintf(stdout, "  city: %s\n", cityPath)                                                                                                         //nolint:errcheck
	fmt.Fprintf(stdout, "  database: %s@%s:%s/%s\n", cityState.MysqlUser, cityState.MysqlHost, cityState.MysqlPort, cityState.MysqlDatabase)              //nolint:errcheck
	fmt.Fprintf(stdout, "  files: .beads/config.yaml, .beads/metadata.json\n")                                                                            //nolint:errcheck
	for _, plan := range plans {
		if !plan.Update {
			continue
		}
		fmt.Fprintf(stdout, "  rig %s: prefix=%s shares city database\n", plan.Rig.Name, plan.Rig.EffectivePrefix()) //nolint:errcheck
	}
}

func printMysqlResult(stdout io.Writer, cityPath string, opts cityMysqlOptions, plans []cityRigEndpointPlan) {
	fmt.Fprintln(stdout, "UPDATED: city endpoint to mysql")                                                                       //nolint:errcheck
	fmt.Fprintf(stdout, "  city: %s\n", cityPath)                                                                                 //nolint:errcheck
	fmt.Fprintf(stdout, "  database: %s@%s:%s/%s\n", opts.User, opts.Host, opts.Port, opts.Database)                              //nolint:errcheck
	updated := 0
	for _, plan := range plans {
		if plan.Update {
			updated++
		}
	}
	fmt.Fprintf(stdout, "  inherited rigs updated: %d\n", updated) //nolint:errcheck
}

// realMysqlProvisioner connects to MySQL and runs CREATE DATABASE IF NOT EXISTS.
// Database name validation is done in validateMysqlOptions; we still quote the
// identifier with backticks to be defensive.
func realMysqlProvisioner(ctx context.Context, host, port, user, password, database string) error {
	if !validMysqlIdentifier(database) {
		return fmt.Errorf("refusing to create database %q: invalid identifier", database)
	}

	cfg := mysqldriver.NewConfig()
	cfg.User = user
	cfg.Passwd = password
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(host, port)
	cfg.AllowNativePasswords = true
	cfg.Timeout = 10 * time.Second
	cfg.ReadTimeout = 10 * time.Second
	cfg.WriteTimeout = 10 * time.Second

	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return fmt.Errorf("open mysql: %w", err)
	}
	defer db.Close() //nolint:errcheck

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping mysql at %s: %w", cfg.Addr, err)
	}

	stmt := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", database)
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("create database %q: %w", database, err)
	}
	return nil
}

// errBdMysqlInit signals a bd init failure surfaced by runBdInitMysql.
var errBdMysqlInit = errors.New("bd init mysql failed")

// runBdInitMysql invokes `bd init --backend=mysql` against the given scope,
// then `bd config set types.custom` to register the gc-required custom types.
// Idempotent: bd init reports the database as already-initialized and exits
// non-zero with a clear message; we treat that as success and only fail on
// genuine errors (missing binary, connection failures, etc.).
func runBdInitMysql(scopeRoot, prefix, database, host, port, user string) error {
	bdPath, err := lookupBd()
	if err != nil {
		return err
	}
	args := []string{
		"init",
		"--backend=mysql",
		"--non-interactive",
		"--quiet",
		"--prefix", prefix,
		"--database", database,
		"--server-host", host,
		"--server-port", port,
		"--server-user", user,
		"--external",
		"--skip-hooks",
		"--skip-agents",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bdPath, args...)
	cmd.Dir = scopeRoot
	cmd.Env = append(os.Environ(), "BEADS_DIR="+filepath.Join(scopeRoot, ".beads"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Idempotency: bd init refuses to re-init when local data already
		// exists, exiting non-zero. Treat that as success — the caller is
		// re-running use-mysql.
		text := string(out)
		if !strings.Contains(text, "already initialized") && !strings.Contains(text, "already exists") {
			return fmt.Errorf("%w: %v: %s", errBdMysqlInit, err, strings.TrimSpace(text))
		}
	}
	return setBdCustomTypes(scopeRoot, bdPath)
}

// setBdCustomTypes registers the gc-required custom bd types. Mirrors the
// doctor check_custom_types fix path — without these types `gc bd create`
// rejects molecule/convoy/etc. types with "unknown type".
func setBdCustomTypes(scopeRoot, bdPath string) error {
	const types = "molecule,convoy,message,event,gate,merge-request,agent,role,rig,session,spec,convergence,step"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bdPath, "config", "set", "types.custom", types)
	cmd.Dir = scopeRoot
	cmd.Env = append(os.Environ(), "BEADS_DIR="+filepath.Join(scopeRoot, ".beads"))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bd config set types.custom: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// clearDoltSideFiles removes per-scope dolt runtime artifacts that linger
// after a brief dolt-init pass (e.g. when gc init scaffolds a city with bd
// init's dolt default before flipping to mysql). These are inert when no
// dolt server is running but look like configuration drift to gc doctor.
//
// Removed: .beads/dolt/ directory, .beads/dolt-server.port file. Both
// per-scope (city + each rig that the cascade touched).
func clearDoltSideFiles(cityPath string, rigPlans []cityRigEndpointPlan) error {
	scopes := []string{cityPath}
	for _, plan := range rigPlans {
		if plan.Update {
			scopes = append(scopes, plan.Rig.Path)
		}
	}
	var firstErr error
	for _, scope := range scopes {
		beadsDir := filepath.Join(scope, ".beads")
		if err := os.RemoveAll(filepath.Join(beadsDir, "dolt")); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("remove %s/dolt: %w", beadsDir, err)
		}
		if err := os.Remove(filepath.Join(beadsDir, "dolt-server.port")); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = fmt.Errorf("remove %s/dolt-server.port: %w", beadsDir, err)
		}
	}
	return firstErr
}

// initDirIfReadyManagedMysql is the mysql-backend counterpart to
// initDirIfReadyManagedDolt. Called by initDirIfReady when the city's
// metadata.json declares backend=mysql; runs bd init --backend=mysql against
// the new rig dir using the city's mysql connection settings, then writes
// canonical metadata.json + config.yaml so the rig matches the rest of the
// cascade.
//
// dir is the rig path; cityPath is the owning city. The new rig shares the
// city's database — this matches the proving-grounds pattern where every
// rig prefix lives in one bd database.
func initDirIfReadyManagedMysql(cityPath, dir, prefix string) error {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return fmt.Errorf("rig prefix is required for mysql cascade")
	}
	city, ok, err := loadCityMysqlState(cityPath)
	if err != nil {
		return fmt.Errorf("read city mysql state: %w", err)
	}
	if !ok {
		return fmt.Errorf("city at %s has backend=mysql but metadata.json is incomplete", cityPath)
	}

	if err := defaultBdMysqlInit(dir, prefix, city.Database, city.Host, city.Port, city.User); err != nil {
		return fmt.Errorf("bd init mysql for rig: %w", err)
	}
	rigState := contract.ConfigState{
		IssuePrefix:    prefix,
		Backend:        "mysql",
		EndpointOrigin: contract.EndpointOriginInheritedCity,
		EndpointStatus: contract.EndpointStatusVerified,
		MysqlHost:      city.Host,
		MysqlPort:      city.Port,
		MysqlUser:      city.User,
		MysqlDatabase:  city.Database,
	}
	opts := cityMysqlOptions{
		Host:     city.Host,
		Port:     city.Port,
		User:     city.User,
		Database: city.Database,
	}
	if err := writeMysqlScope(fsys.OSFS{}, dir, rigState, prefix, opts); err != nil {
		return fmt.Errorf("write canonical rig scope: %w", err)
	}
	return installBeadHooks(dir, cityPath)
}

// cityMysqlState is the resolved mysql connection target for a city.
type cityMysqlState struct {
	Host     string
	Port     string
	User     string
	Database string
}

// loadCityMysqlState reads the city's metadata.json and returns the mysql
// connection target. Falls back to config.yaml mysql.* keys when metadata
// is incomplete (e.g. legacy scopes that haven't been re-canonicalised yet
// — including the scs-core-services-pre-fix shape where metadata.json says
// backend=mysql but lacks the mysql_* fields, which the contract rejects
// with a parse error). Returns (zero, false, nil) when neither source has
// all four fields.
func loadCityMysqlState(cityPath string) (cityMysqlState, bool, error) {
	fs := fsys.OSFS{}
	state := cityMysqlState{}
	meta, ok, err := contract.LoadMetadataState(fs, filepath.Join(cityPath, ".beads", "metadata.json"))
	if err != nil {
		// Tolerate parse-error metadata (e.g. mysql with missing fields) —
		// the config.yaml fallback below may have what we need.
		var perr *contract.MetadataParseError
		if !errors.As(err, &perr) {
			return state, false, err
		}
	}
	if ok && meta.Backend == "mysql" {
		state.Host = meta.MysqlHost
		state.Port = meta.MysqlPort
		state.User = meta.MysqlUser
		state.Database = meta.MysqlDatabase
	}
	if state.Host == "" || state.Port == "" || state.User == "" || state.Database == "" {
		cfg, cfgOK, err := contract.ReadConfigState(fs, filepath.Join(cityPath, ".beads", "config.yaml"))
		if err != nil {
			return state, false, err
		}
		if cfgOK {
			if state.Host == "" {
				state.Host = cfg.MysqlHost
			}
			if state.Port == "" {
				state.Port = cfg.MysqlPort
			}
			if state.User == "" {
				state.User = cfg.MysqlUser
			}
			if state.Database == "" {
				state.Database = cfg.MysqlDatabase
			}
		}
	}
	if state.Host == "" || state.Port == "" || state.User == "" || state.Database == "" {
		return state, false, nil
	}
	return state, true, nil
}

// stopManagedDoltIfRunning best-effort stops any managed-dolt runtime owned by
// this city. Used by use-mysql to leave no zombie dolt sql-server behind.
// Failures are non-fatal: the caller logs but does not roll back, because the
// metadata flip is already durable on disk.
func stopManagedDoltIfRunning(cityPath string, _ io.Writer) error {
	owned, err := managedDoltLifecycleOwned(cityPath)
	if err != nil {
		return fmt.Errorf("checking managed dolt ownership: %w", err)
	}
	if !owned {
		return nil
	}
	provider := beadsProvider(cityPath)
	if !strings.HasPrefix(provider, "exec:") || !providerUsesBdStoreContract(provider) {
		return nil
	}
	script := strings.TrimPrefix(provider, "exec:")
	configuredProvider := configuredBeadsProviderValue(cityPath)
	if (configuredProvider == "" || configuredProvider == "bd") && execProviderBase(provider) == "gc-beads-bd" {
		if err := MaterializeBuiltinPacks(cityPath); err != nil {
			return fmt.Errorf("materialize provider for stop: %w", err)
		}
		script = gcBeadsBdScriptPath(cityPath)
	}
	env, err := providerLifecycleProcessEnvWithError(cityPath, provider)
	if err != nil {
		return fmt.Errorf("provider env for stop: %w", err)
	}
	if err := runProviderOpWithEnv(script, env, "stop"); err != nil {
		return fmt.Errorf("provider stop: %w", err)
	}
	if err := clearManagedDoltRuntimeStateIfOwned(cityPath); err != nil {
		return fmt.Errorf("clear managed runtime state: %w", err)
	}
	return nil
}

// lookupBd resolves the bd binary on PATH. Wrapped for testability.
var lookupBd = func() (string, error) {
	return exec.LookPath("bd")
}
