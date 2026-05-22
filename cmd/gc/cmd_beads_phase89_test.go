package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPromptWizardBackendDefaultsToDolt(t *testing.T) {
	in := bytes.NewBufferString("\n")
	var out bytes.Buffer
	got := promptWizardBackend(bufio.NewReader(in), &out)
	if got.Backend != "" {
		t.Fatalf("expected empty Backend (dolt default), got %q", got.Backend)
	}
}

func TestPromptWizardBackendSelectsMysql(t *testing.T) {
	// Inputs: choose 2 → host (default) → port (default) → user (default) → database
	in := bytes.NewBufferString("2\n\n\n\nmy_beads\n")
	var out bytes.Buffer
	got := promptWizardBackend(bufio.NewReader(in), &out)
	if got.Backend != "mysql" {
		t.Fatalf("Backend = %q, want mysql", got.Backend)
	}
	if got.Host != "127.0.0.1" || got.Port != "3306" || got.User != "root" || got.Database != "my_beads" {
		t.Fatalf("unexpected mysql opts: %+v", got)
	}
}

func TestPromptWizardBackendCustomHost(t *testing.T) {
	in := bytes.NewBufferString("mysql\ndb.example.com\n3307\nadmin\nbeads_db\n")
	var out bytes.Buffer
	got := promptWizardBackend(bufio.NewReader(in), &out)
	if got.Host != "db.example.com" || got.Port != "3307" || got.User != "admin" {
		t.Fatalf("custom values not captured: %+v", got)
	}
}

func TestPromptWizardBackendBlankDatabaseFallsBack(t *testing.T) {
	// User chose mysql then gave no database — fall back to dolt.
	in := bytes.NewBufferString("2\n\n\n\n\n")
	var out bytes.Buffer
	got := promptWizardBackend(bufio.NewReader(in), &out)
	if got.Backend != "" {
		t.Fatalf("expected fallback to dolt, got %+v", got)
	}
	if !strings.Contains(out.String(), "falling back to managed dolt") {
		t.Fatalf("missing fallback message: %s", out.String())
	}
}

func TestPromptWizardBackendUnknownChoiceFallsBack(t *testing.T) {
	in := bytes.NewBufferString("postgres\n")
	var out bytes.Buffer
	got := promptWizardBackend(bufio.NewReader(in), &out)
	if got.Backend != "" {
		t.Fatalf("expected fallback to dolt, got %+v", got)
	}
}

func TestDescribeBeadsBackendLabelDefault(t *testing.T) {
	dir := t.TempDir()
	if got := describeBeadsBackendLabel(dir); got != "managed dolt" {
		t.Fatalf("got %q, want managed dolt", got)
	}
}

func TestDescribeBeadsBackendLabelMysql(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
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
	if err := os.WriteFile(filepath.Join(dir, ".beads", "metadata.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := describeBeadsBackendLabel(dir); got != "mysql" {
		t.Fatalf("got %q, want mysql", got)
	}
}
