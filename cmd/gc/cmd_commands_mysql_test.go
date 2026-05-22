package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

func TestArgsRequestHelp(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{nil, true},
		{[]string{}, true},
		{[]string{"status"}, false},
		{[]string{"status", "--help"}, true},
		{[]string{"-h"}, true},
		{[]string{"sql", "--query", "SELECT 1"}, false},
	} {
		if got := argsRequestHelp(tc.args); got != tc.want {
			t.Fatalf("argsRequestHelp(%v) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

func TestRunDiscoveredCommandRefusesGcDoltOnMysql(t *testing.T) {
	city := t.TempDir()
	if err := os.MkdirAll(filepath.Join(city, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{
		"backend":        "mysql",
		"database":       "demo",
		"mysql_host":     "127.0.0.1",
		"mysql_port":     "3306",
		"mysql_user":     "root",
		"mysql_database": "demo",
	})
	if err := os.WriteFile(filepath.Join(city, ".beads", "metadata.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	entry := config.DiscoveredCommand{
		BindingName: "dolt",
		Command:     []string{"status"},
		PackName:    "dolt",
		RunScript:   "/nonexistent/run.sh", // never executed because we refuse early
	}
	var stdout, stderr bytes.Buffer
	code := runDiscoveredCommand(entry, city, "demo", []string{"status"}, nil, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d (stdout=%s stderr=%s)", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "backend=mysql") {
		t.Fatalf("missing refusal message: %s", stderr.String())
	}
}

func TestRunDiscoveredCommandPermitsGcDoltHelpOnMysql(t *testing.T) {
	city := t.TempDir()
	if err := os.MkdirAll(filepath.Join(city, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{
		"backend":        "mysql",
		"database":       "demo",
		"mysql_host":     "127.0.0.1",
		"mysql_port":     "3306",
		"mysql_user":     "root",
		"mysql_database": "demo",
	})
	if err := os.WriteFile(filepath.Join(city, ".beads", "metadata.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	// --help arg means the guard should NOT short-circuit; the actual run
	// will fail because the script doesn't exist, but that's fine — we just
	// want to confirm we got past the guard.
	entry := config.DiscoveredCommand{
		BindingName: "dolt",
		Command:     []string{"status"},
		PackName:    "dolt",
		RunScript:   "/nonexistent/run.sh",
	}
	var stdout, stderr bytes.Buffer
	code := runDiscoveredCommand(entry, city, "demo", []string{"--help"}, nil, &stdout, &stderr)
	// The script doesn't exist, so the run will fail — but the message
	// must NOT contain our refusal text.
	if strings.Contains(stderr.String(), "backend=mysql") {
		t.Fatalf("guard fired on --help: %s", stderr.String())
	}
	_ = code
}

func TestRunDiscoveredCommandIgnoresNonDoltBindings(t *testing.T) {
	city := t.TempDir()
	if err := os.MkdirAll(filepath.Join(city, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{
		"backend":        "mysql",
		"database":       "demo",
		"mysql_host":     "127.0.0.1",
		"mysql_port":     "3306",
		"mysql_user":     "root",
		"mysql_database": "demo",
	})
	if err := os.WriteFile(filepath.Join(city, ".beads", "metadata.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	entry := config.DiscoveredCommand{
		BindingName: "maintenance", // not "dolt"
		Command:     []string{"status"},
		PackName:    "maintenance",
		RunScript:   "/nonexistent/run.sh",
	}
	var stdout, stderr bytes.Buffer
	code := runDiscoveredCommand(entry, city, "demo", []string{"status"}, nil, &stdout, &stderr)
	if strings.Contains(stderr.String(), "backend=mysql") {
		t.Fatalf("guard fired on non-dolt binding: %s", stderr.String())
	}
	_ = code
}
