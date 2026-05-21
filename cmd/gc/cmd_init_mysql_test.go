package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestValidateInitMysqlOptionsEmptyBackendOK(t *testing.T) {
	if err := validateInitMysqlOptions(initMysqlOptions{}); err != nil {
		t.Fatalf("empty opts should be allowed: %v", err)
	}
}

func TestValidateInitMysqlOptionsRejectsUnknownBackend(t *testing.T) {
	err := validateInitMysqlOptions(initMysqlOptions{Backend: "postgres"})
	if err == nil {
		t.Fatal("expected error for non-mysql backend")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("unexpected message: %v", err)
	}
}

func TestValidateInitMysqlOptionsRequiresDatabase(t *testing.T) {
	err := validateInitMysqlOptions(initMysqlOptions{Backend: "mysql"})
	if err == nil {
		t.Fatal("expected error for missing --mysql-database")
	}
	if !strings.Contains(err.Error(), "--mysql-database") {
		t.Fatalf("unexpected message: %v", err)
	}
}

func TestValidateInitMysqlOptionsAcceptsValid(t *testing.T) {
	err := validateInitMysqlOptions(initMysqlOptions{
		Backend:  "mysql",
		Database: "demo_beads",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewInitCmdSurfacesMysqlFlags(t *testing.T) {
	cmd := newInitCmd(&bytes.Buffer{}, &bytes.Buffer{})
	for _, name := range []string{"backend", "mysql-host", "mysql-port", "mysql-user", "mysql-password", "mysql-database"} {
		if cmd.Flag(name) == nil {
			t.Fatalf("init flag %q missing", name)
		}
	}
}

func TestNewInitCmdHelpMentionsMysql(t *testing.T) {
	cmd := newInitCmd(&bytes.Buffer{}, &bytes.Buffer{})
	help := cmd.Long
	for _, needle := range []string{"--backend=mysql", "--mysql-database"} {
		if !strings.Contains(help, needle) {
			t.Fatalf("help missing %q:\n%s", needle, help)
		}
	}
}
