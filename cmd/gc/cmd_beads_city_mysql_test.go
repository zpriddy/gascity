package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads/contract"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
)

func TestNewBeadsCmdIncludesUseMysql(t *testing.T) {
	cmd := newBeadsCmd(&bytes.Buffer{}, &bytes.Buffer{})
	useMysql, _, err := cmd.Find([]string{"city", "use-mysql"})
	if err != nil {
		t.Fatalf("Find(city use-mysql): %v", err)
	}
	if useMysql == nil || useMysql.Name() != "use-mysql" {
		t.Fatalf("use-mysql command = %#v", useMysql)
	}
}

func TestValidateMysqlOptionsRequiresDatabase(t *testing.T) {
	opts := cityMysqlOptions{Host: "127.0.0.1", Port: "3306", User: "root"}
	if err := validateMysqlOptions(&opts); err == nil {
		t.Fatal("expected --database required error")
	}
}

func TestValidateMysqlOptionsRejectsBadDatabase(t *testing.T) {
	opts := cityMysqlOptions{Host: "127.0.0.1", Port: "3306", User: "root", Database: "drop;table--"}
	if err := validateMysqlOptions(&opts); err == nil {
		t.Fatal("expected invalid database error")
	}
}

func TestValidateMysqlOptionsRejectsBadPort(t *testing.T) {
	opts := cityMysqlOptions{Host: "127.0.0.1", Port: "99999", User: "root", Database: "csi_beads"}
	if err := validateMysqlOptions(&opts); err == nil {
		t.Fatal("expected invalid port error")
	}
}

func TestValidateMysqlOptionsAcceptsValid(t *testing.T) {
	opts := cityMysqlOptions{Host: "127.0.0.1", Port: "3306", User: "root", Database: "csi_beads"}
	if err := validateMysqlOptions(&opts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateMysqlOptionsReadsPasswordFromEnv(t *testing.T) {
	t.Setenv("GC_MYSQL_PASSWORD", "secret")
	opts := cityMysqlOptions{Host: "127.0.0.1", Port: "3306", User: "root", Database: "csi_beads"}
	if err := validateMysqlOptions(&opts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.Password != "secret" {
		t.Fatalf("password not read from env, got %q", opts.Password)
	}
}

// fakeMysqlProvisioner records arguments without actually touching MySQL.
type fakeMysqlProvisioner struct {
	called   bool
	host     string
	port     string
	user     string
	database string
	err      error
}

func (f *fakeMysqlProvisioner) provision(ctx context.Context, host, port, user, password, database string) error {
	f.called = true
	f.host = host
	f.port = port
	f.user = user
	f.database = database
	return f.err
}

// fakeBdInit records calls.
type fakeBdInit struct {
	calls []bdInitCall
	err   error
}

type bdInitCall struct {
	scopeRoot, prefix, database, host, port, user string
}

func (f *fakeBdInit) run(scopeRoot, prefix, database, host, port, user string) error {
	f.calls = append(f.calls, bdInitCall{scopeRoot, prefix, database, host, port, user})
	return f.err
}

func newCityToml(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "city.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a minimal identity.toml so EnsureCanonicalMetadata picks up a
	// project_id without needing one.
	identityPath := filepath.Join(dir, ".beads", "identity.toml")
	if err := os.WriteFile(identityPath, []byte("[project]\nid = \"test-project\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDoBeadsCityUseMysqlWritesCityScope(t *testing.T) {
	dir := t.TempDir()
	newCityToml(t, dir, `[workspace]
name = "demo"
provider = "claude"
`)

	prov := &fakeMysqlProvisioner{}
	bdi := &fakeBdInit{}
	prevProv := defaultMysqlProvisioner
	prevBd := defaultBdMysqlInit
	defer func() {
		defaultMysqlProvisioner = prevProv
		defaultBdMysqlInit = prevBd
	}()
	defaultMysqlProvisioner = prov.provision
	defaultBdMysqlInit = bdi.run

	var stdout, stderr bytes.Buffer
	code := doBeadsCityUseMysql(fsys.OSFS{}, dir, cityMysqlOptions{
		Host:     "127.0.0.1",
		Port:     "3306",
		User:     "root",
		Database: "demo_beads",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doBeadsCityUseMysql = %d (stderr: %s)", code, stderr.String())
	}
	if !prov.called || prov.database != "demo_beads" {
		t.Fatalf("provisioner not called correctly: %+v", prov)
	}
	if len(bdi.calls) != 1 || bdi.calls[0].scopeRoot != dir {
		t.Fatalf("expected 1 bd init call for city, got %+v", bdi.calls)
	}

	// Verify metadata.json contents.
	metaPath := filepath.Join(dir, ".beads", "metadata.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if meta["backend"] != "mysql" {
		t.Fatalf("expected backend=mysql, got %v", meta["backend"])
	}
	if meta["mysql_database"] != "demo_beads" {
		t.Fatalf("expected mysql_database=demo_beads, got %v", meta["mysql_database"])
	}

	// Verify config.yaml contents.
	cfgPath := filepath.Join(dir, ".beads", "config.yaml")
	cfgBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfgStr := string(cfgBytes)
	for _, needle := range []string{
		"mysql.server-host: 127.0.0.1",
		"mysql.server-port: 3306",
		"mysql.database: demo_beads",
		"dolt.auto-start: false",
	} {
		if !strings.Contains(cfgStr, needle) {
			t.Fatalf("config.yaml missing %q:\n%s", needle, cfgStr)
		}
	}
}

func TestDoBeadsCityUseMysqlCascadesToRigs(t *testing.T) {
	cityDir := t.TempDir()
	rigDir := t.TempDir()
	newCityToml(t, cityDir, `[workspace]
name = "demo"
provider = "claude"

[[rigs]]
name = "test-rig"
prefix = "tr"
path = "`+rigDir+`"
`)
	if err := os.MkdirAll(filepath.Join(rigDir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}

	prov := &fakeMysqlProvisioner{}
	bdi := &fakeBdInit{}
	prevProv := defaultMysqlProvisioner
	prevBd := defaultBdMysqlInit
	defer func() {
		defaultMysqlProvisioner = prevProv
		defaultBdMysqlInit = prevBd
	}()
	defaultMysqlProvisioner = prov.provision
	defaultBdMysqlInit = bdi.run

	var stdout, stderr bytes.Buffer
	code := doBeadsCityUseMysql(fsys.OSFS{}, cityDir, cityMysqlOptions{
		Host:     "127.0.0.1",
		Port:     "3306",
		User:     "root",
		Database: "demo_beads",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doBeadsCityUseMysql = %d (stderr: %s)", code, stderr.String())
	}
	if len(bdi.calls) != 2 {
		t.Fatalf("expected 2 bd init calls (city + 1 rig), got %d: %+v", len(bdi.calls), bdi.calls)
	}
	// First call is city (prefix from config.EffectiveHQPrefix — derives from
	// workspace name), second call is rig with prefix=tr.
	if bdi.calls[1].prefix != "tr" {
		t.Fatalf("rig bd init prefix = %q, want tr", bdi.calls[1].prefix)
	}
	if bdi.calls[1].scopeRoot != rigDir {
		t.Fatalf("rig bd init scopeRoot = %q, want %q", bdi.calls[1].scopeRoot, rigDir)
	}

	// Verify rig metadata.json
	metaPath := filepath.Join(rigDir, ".beads", "metadata.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if meta["backend"] != "mysql" || meta["mysql_database"] != "demo_beads" {
		t.Fatalf("rig metadata not mysql-shaped: %v", meta)
	}
}

func TestDoBeadsCityUseMysqlDryRunDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	newCityToml(t, dir, `[workspace]
name = "demo"
provider = "claude"
`)

	prov := &fakeMysqlProvisioner{}
	bdi := &fakeBdInit{}
	prevProv := defaultMysqlProvisioner
	prevBd := defaultBdMysqlInit
	defer func() {
		defaultMysqlProvisioner = prevProv
		defaultBdMysqlInit = prevBd
	}()
	defaultMysqlProvisioner = prov.provision
	defaultBdMysqlInit = bdi.run

	var stdout, stderr bytes.Buffer
	code := doBeadsCityUseMysql(fsys.OSFS{}, dir, cityMysqlOptions{
		Host:     "127.0.0.1",
		Port:     "3306",
		User:     "root",
		Database: "demo_beads",
		DryRun:   true,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("dry-run failed: %s", stderr.String())
	}
	if prov.called {
		t.Fatal("dry-run must not call provisioner")
	}
	if len(bdi.calls) > 0 {
		t.Fatalf("dry-run must not run bd init, got %d calls", len(bdi.calls))
	}
	if _, err := os.Stat(filepath.Join(dir, ".beads", "metadata.json")); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote metadata.json: %v", err)
	}
	if !strings.Contains(stdout.String(), "WOULD UPDATE") {
		t.Fatalf("dry-run output missing WOULD UPDATE: %s", stdout.String())
	}
}

func TestDoBeadsCityUseMysqlRollsBackOnBdInitFailure(t *testing.T) {
	dir := t.TempDir()
	newCityToml(t, dir, `[workspace]
name = "demo"
provider = "claude"
`)
	// Pre-write a sentinel config.yaml so we can detect rollback.
	cfgPath := filepath.Join(dir, ".beads", "config.yaml")
	sentinel := []byte("issue_prefix: original\n")
	if err := os.WriteFile(cfgPath, sentinel, 0o644); err != nil {
		t.Fatal(err)
	}

	prov := &fakeMysqlProvisioner{}
	bdi := &fakeBdInit{err: errBdMysqlInit}
	prevProv := defaultMysqlProvisioner
	prevBd := defaultBdMysqlInit
	defer func() {
		defaultMysqlProvisioner = prevProv
		defaultBdMysqlInit = prevBd
	}()
	defaultMysqlProvisioner = prov.provision
	defaultBdMysqlInit = bdi.run

	var stdout, stderr bytes.Buffer
	code := doBeadsCityUseMysql(fsys.OSFS{}, dir, cityMysqlOptions{
		Host:     "127.0.0.1",
		Port:     "3306",
		User:     "root",
		Database: "demo_beads",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit")
	}
	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, sentinel) {
		t.Fatalf("rollback failed: config.yaml = %s, want %s", got, sentinel)
	}
}

func TestPlanMysqlRigUpdatesPropagatesConnection(t *testing.T) {
	cityState := contract.ConfigState{
		Backend:       "mysql",
		MysqlHost:     "127.0.0.1",
		MysqlPort:     "3306",
		MysqlUser:     "root",
		MysqlDatabase: "demo_beads",
	}
	rigs := []config.Rig{{Name: "test-rig", Prefix: "tr", Path: "/tmp/whatever"}}
	plans := planMysqlRigUpdates(rigs, cityState)
	if len(plans) != 1 {
		t.Fatalf("expected 1 plan, got %d", len(plans))
	}
	plan := plans[0]
	if !plan.Update {
		t.Fatal("plan.Update should be true")
	}
	if plan.Target.Backend != "mysql" {
		t.Fatalf("Backend = %q", plan.Target.Backend)
	}
	if plan.Target.MysqlDatabase != "demo_beads" {
		t.Fatal("rig didn't inherit database")
	}
	if plan.Target.IssuePrefix != "tr" {
		t.Fatalf("rig prefix = %q, want tr", plan.Target.IssuePrefix)
	}
	if plan.Target.EndpointOrigin != contract.EndpointOriginInheritedCity {
		t.Fatalf("rig endpoint origin = %q", plan.Target.EndpointOrigin)
	}
}
