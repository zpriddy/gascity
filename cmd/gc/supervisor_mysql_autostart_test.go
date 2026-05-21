package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/supervisor"
)

func writeMysqlMetadata(t *testing.T, dir, host, port string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{
		"backend":        "mysql",
		"database":       "demo",
		"mysql_host":     host,
		"mysql_port":     port,
		"mysql_user":     "root",
		"mysql_database": "demo",
	})
	if err := os.WriteFile(filepath.Join(dir, ".beads", "metadata.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeDoltMetadata(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{
		"backend":       "dolt",
		"database":      "dolt",
		"dolt_database": "hq",
		"dolt_mode":     "server",
	})
	if err := os.WriteFile(filepath.Join(dir, ".beads", "metadata.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCollectLoopbackMysqlTargetsDeduplicates(t *testing.T) {
	d1, d2, d3, d4 := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	writeMysqlMetadata(t, d1, "127.0.0.1", "3306")
	writeMysqlMetadata(t, d2, "127.0.0.1", "3306") // duplicate
	writeMysqlMetadata(t, d3, "localhost", "3307")
	writeDoltMetadata(t, d4)
	entries := []supervisor.CityEntry{
		{Path: d1, Name: "a"},
		{Path: d2, Name: "b"},
		{Path: d3, Name: "c"},
		{Path: d4, Name: "d"},
	}
	got := collectLoopbackMysqlTargets(entries)
	want := map[string]bool{"127.0.0.1:3306": true, "localhost:3307": true}
	if len(got) != len(want) {
		t.Fatalf("len(got)=%d, want %d (got=%v)", len(got), len(want), got)
	}
	for _, addr := range got {
		if !want[addr] {
			t.Fatalf("unexpected target %q", addr)
		}
	}
}

func TestCollectLoopbackMysqlTargetsSkipsRemote(t *testing.T) {
	d := t.TempDir()
	writeMysqlMetadata(t, d, "db.example.com", "3306")
	got := collectLoopbackMysqlTargets([]supervisor.CityEntry{{Path: d}})
	if len(got) != 0 {
		t.Fatalf("expected non-loopback to be skipped, got %v", got)
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1", "localhost", "", "  127.0.0.1  ", "LOCALHOST"} {
		if !isLoopbackHost(host) {
			t.Fatalf("%q should be loopback", host)
		}
	}
	for _, host := range []string{"db.example.com", "10.0.0.1", "::2"} {
		if isLoopbackHost(host) {
			t.Fatalf("%q should not be loopback", host)
		}
	}
}

func TestIsMysqlAutostartDisabled(t *testing.T) {
	for _, val := range []string{"", "0", "false", "OFF", "no"} {
		t.Setenv(mysqlAutostartEnv, val)
		if isMysqlAutostartDisabled() {
			t.Fatalf("%q should not disable", val)
		}
	}
	for _, val := range []string{"1", "true", "yes", "anything"} {
		t.Setenv(mysqlAutostartEnv, val)
		if !isMysqlAutostartDisabled() {
			t.Fatalf("%q should disable", val)
		}
	}
}

func TestStartLocalMysqldFirstSuccessWins(t *testing.T) {
	if len(mysqldStartCommands()) == 0 {
		t.Skip("no autostart commands on this platform")
	}
	prevRunner := mysqlAutostartRunner
	defer func() { mysqlAutostartRunner = prevRunner }()
	calls := 0
	mysqlAutostartRunner = func(ctx context.Context, c mysqlAutostartCommand) error {
		calls++
		return nil // first attempt succeeds
	}
	if err := startLocalMysqld(context.Background(), os.Stderr, os.Stderr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected first attempt to win, got %d calls", calls)
	}
}

func TestStartLocalMysqldFallsThrough(t *testing.T) {
	if len(mysqldStartCommands()) < 2 {
		t.Skip("platform has fewer than 2 strategies")
	}
	prevRunner := mysqlAutostartRunner
	defer func() { mysqlAutostartRunner = prevRunner }()
	calls := 0
	mysqlAutostartRunner = func(ctx context.Context, c mysqlAutostartCommand) error {
		calls++
		if calls < 2 {
			return errors.New("nope")
		}
		return nil
	}
	if err := startLocalMysqld(context.Background(), os.Stderr, os.Stderr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls < 2 {
		t.Fatalf("expected fallback, got %d calls", calls)
	}
}

func TestStartLocalMysqldAllFail(t *testing.T) {
	if len(mysqldStartCommands()) == 0 {
		t.Skip("no autostart commands on this platform")
	}
	prevRunner := mysqlAutostartRunner
	defer func() { mysqlAutostartRunner = prevRunner }()
	mysqlAutostartRunner = func(ctx context.Context, c mysqlAutostartCommand) error {
		return errors.New("nope")
	}
	err := startLocalMysqld(context.Background(), os.Stderr, os.Stderr)
	if err == nil {
		t.Fatal("expected error when all strategies fail")
	}
}

func TestSplitHostPort(t *testing.T) {
	host, port := splitHostPort("127.0.0.1:3306")
	if host != "127.0.0.1" || port != "3306" {
		t.Fatalf("got %q:%q", host, port)
	}
	// IPv6 form.
	host, port = splitHostPort("[::1]:3307")
	if host != "::1" || port != "3307" {
		t.Fatalf("got %q:%q", host, port)
	}
}
