package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/doctor"
)

func writeMysqlScopeForCheck(t *testing.T, dir, host, port, db string) {
	t.Helper()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]string{
		"backend":        "mysql",
		"database":       db,
		"mysql_host":     host,
		"mysql_port":     port,
		"mysql_user":     "root",
		"mysql_database": db,
	})
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), meta, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := strings.Join([]string{
		"issue_prefix: cs",
		"mysql.server-host: " + host,
		"mysql.server-port: " + port,
		"mysql.server-user: root",
		"mysql.database: " + db,
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeBackendDriftScope(t *testing.T, dir string) {
	t.Helper()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// metadata.json says dolt
	meta, _ := json.Marshal(map[string]string{
		"backend":       "dolt",
		"database":      "dolt",
		"dolt_database": "hq",
		"dolt_mode":     "server",
	})
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), meta, 0o644); err != nil {
		t.Fatal(err)
	}
	// config.yaml says mysql (the drift state)
	cfg := strings.Join([]string{
		"issue_prefix: cs",
		"mysql.server-host: 127.0.0.1",
		"mysql.server-port: 3306",
		"mysql.server-user: root",
		"mysql.database: drift_beads",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// stub probe — used to make connection-drift tests deterministic.
func withProbe(t *testing.T, reachable bool) {
	t.Helper()
	prev := mysqlAutostartProbe
	t.Cleanup(func() { mysqlAutostartProbe = prev })
	mysqlAutostartProbe = func(_ context.Context, _, _ string) bool { return reachable }
}

func TestMysqlBackendCheckSkipsNonBdScope(t *testing.T) {
	dir := t.TempDir()
	check := newMysqlBackendCheck(dir)
	res := check.Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusOK {
		t.Fatalf("expected OK on empty scope, got %d: %s", res.Status, res.Message)
	}
}

func TestMysqlBackendCheckOKWhenReachable(t *testing.T) {
	dir := t.TempDir()
	writeMysqlScopeForCheck(t, dir, "127.0.0.1", "3306", "demo_beads")
	withProbe(t, true)

	check := newMysqlBackendCheck(dir)
	res := check.Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusOK {
		t.Fatalf("expected OK, got %d: %s", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "reachable") {
		t.Fatalf("unexpected message: %s", res.Message)
	}
}

func TestMysqlBackendCheckErrorWhenUnreachable(t *testing.T) {
	dir := t.TempDir()
	writeMysqlScopeForCheck(t, dir, "127.0.0.1", "3306", "demo_beads")
	withProbe(t, false)

	check := newMysqlBackendCheck(dir)
	res := check.Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusError {
		t.Fatalf("expected Error, got %d: %s", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "not reachable") {
		t.Fatalf("unexpected message: %s", res.Message)
	}
	if check.CanFix() {
		t.Fatal("connection drift should not be auto-fixable")
	}
}

func TestMysqlBackendCheckDetectsBackendDrift(t *testing.T) {
	dir := t.TempDir()
	writeBackendDriftScope(t, dir)

	check := newMysqlBackendCheck(dir)
	res := check.Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusError {
		t.Fatalf("expected Error, got %d: %s", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "mid-migration drift") {
		t.Fatalf("unexpected message: %s", res.Message)
	}
	if !check.CanFix() {
		t.Fatal("backend drift should be auto-fixable")
	}
}

func TestMysqlBackendCheckFixFlipsMetadata(t *testing.T) {
	dir := t.TempDir()
	writeBackendDriftScope(t, dir)

	check := newMysqlBackendCheck(dir)
	if res := check.Run(&doctor.CheckContext{}); res.Status != doctor.StatusError {
		t.Fatalf("setup failure: %s", res.Message)
	}
	if err := check.Fix(&doctor.CheckContext{}); err != nil {
		t.Fatalf("Fix() error: %v", err)
	}
	// Re-run: should now report OK (with probe set to reachable).
	withProbe(t, true)
	res := check.Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusOK {
		t.Fatalf("post-fix expected OK, got %d: %s", res.Status, res.Message)
	}

	// metadata.json must now have backend=mysql, no dolt_* keys.
	data, err := os.ReadFile(filepath.Join(dir, ".beads", "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if meta["backend"] != "mysql" {
		t.Fatalf("backend = %v, want mysql", meta["backend"])
	}
	if _, ok := meta["dolt_database"]; ok {
		t.Fatal("dolt_database should be scrubbed")
	}
	if meta["mysql_database"] != "drift_beads" {
		t.Fatalf("mysql_database = %v, want drift_beads", meta["mysql_database"])
	}
}

func TestMysqlBackendCheckFixRefusesWhenNoDrift(t *testing.T) {
	dir := t.TempDir()
	writeMysqlScopeForCheck(t, dir, "127.0.0.1", "3306", "demo_beads")
	withProbe(t, true)
	check := newMysqlBackendCheck(dir)
	if res := check.Run(&doctor.CheckContext{}); res.Status != doctor.StatusOK {
		t.Fatalf("setup expected OK: %s", res.Message)
	}
	if err := check.Fix(&doctor.CheckContext{}); err == nil {
		t.Fatal("Fix should error when no drift detected")
	}
}
