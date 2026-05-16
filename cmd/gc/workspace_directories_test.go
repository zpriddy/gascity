package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

func TestAgentWorkspaceDirectories_NilCity(t *testing.T) {
	a := &config.Agent{Name: "test"}
	got := agentWorkspaceDirectories(a, nil)
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestAgentWorkspaceDirectories_NoDirs(t *testing.T) {
	a := &config.Agent{Name: "test"}
	city := &config.City{}
	got := agentWorkspaceDirectories(a, city)
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestAgentWorkspaceDirectories_NotOptedIn(t *testing.T) {
	a := &config.Agent{Name: "test"}
	city := &config.City{
		WorkspaceDirectories: []config.WorkspaceDirectory{
			{Name: "ansible", Path: "/path/to/ansible"},
		},
	}
	got := agentWorkspaceDirectories(a, city)
	if got != nil {
		t.Fatalf("expected nil for non-opted-in agent, got %v", got)
	}
}

func TestAgentWorkspaceDirectories_IncludeAll(t *testing.T) {
	includeAll := true
	a := &config.Agent{
		Name:                        "test",
		IncludeWorkspaceDirectories: &includeAll,
	}
	city := &config.City{
		WorkspaceDirectories: []config.WorkspaceDirectory{
			{Name: "ansible", Path: "/path/to/ansible"},
			{Name: "config", Path: "/path/to/config"},
		},
	}
	got := agentWorkspaceDirectories(a, city)
	if len(got) != 2 {
		t.Fatalf("expected 2 dirs, got %d", len(got))
	}
}

func TestAgentWorkspaceDirectories_SelectiveByName(t *testing.T) {
	a := &config.Agent{
		Name:                    "test",
		WorkspaceDirectoryNames: []string{"ansible"},
	}
	city := &config.City{
		WorkspaceDirectories: []config.WorkspaceDirectory{
			{Name: "ansible", Path: "/path/to/ansible"},
			{Name: "config", Path: "/path/to/config"},
			{Name: "deploy", Path: "/path/to/deploy"},
		},
	}
	got := agentWorkspaceDirectories(a, city)
	if len(got) != 1 {
		t.Fatalf("expected 1 dir, got %d", len(got))
	}
	if got[0].Name != "ansible" {
		t.Fatalf("expected 'ansible', got %q", got[0].Name)
	}
}

func TestAgentWorkspaceDirectories_SelectiveTakesPrecedence(t *testing.T) {
	includeAll := true
	a := &config.Agent{
		Name:                        "test",
		IncludeWorkspaceDirectories: &includeAll,
		WorkspaceDirectoryNames:     []string{"config"},
	}
	city := &config.City{
		WorkspaceDirectories: []config.WorkspaceDirectory{
			{Name: "ansible", Path: "/path/to/ansible"},
			{Name: "config", Path: "/path/to/config"},
		},
	}
	got := agentWorkspaceDirectories(a, city)
	if len(got) != 1 {
		t.Fatalf("selective should take precedence: expected 1 dir, got %d", len(got))
	}
	if got[0].Name != "config" {
		t.Fatalf("expected 'config', got %q", got[0].Name)
	}
}

func TestAgentWorkspaceDirectories_RigInterpolation(t *testing.T) {
	includeAll := true
	a := &config.Agent{
		Name:                        "test",
		IncludeWorkspaceDirectories: &includeAll,
	}
	city := &config.City{
		Rigs: []config.Rig{
			{Name: "myrig", Path: "/repos/myrig"},
		},
		WorkspaceDirectories: []config.WorkspaceDirectory{
			{Name: "subdir", Path: "{rig:myrig}/src"},
		},
	}
	got := agentWorkspaceDirectories(a, city)
	if len(got) != 1 {
		t.Fatalf("expected 1 dir, got %d", len(got))
	}
	if got[0].ResolvedPath != "/repos/myrig/src" {
		t.Fatalf("expected resolved path '/repos/myrig/src', got %q", got[0].ResolvedPath)
	}
	if got[0].ParentRig != "myrig" {
		t.Fatalf("expected ParentRig 'myrig', got %q", got[0].ParentRig)
	}
}

func TestAgentWorkspaceDirectories_IncludeFalse(t *testing.T) {
	includeFalse := false
	a := &config.Agent{
		Name:                        "test",
		IncludeWorkspaceDirectories: &includeFalse,
	}
	city := &config.City{
		WorkspaceDirectories: []config.WorkspaceDirectory{
			{Name: "ansible", Path: "/path/to/ansible"},
		},
	}
	got := agentWorkspaceDirectories(a, city)
	if got != nil {
		t.Fatalf("include=false should return nil, got %v", got)
	}
}

func TestAgentWorkspaceDirectories_SelectiveNonexistentName(t *testing.T) {
	a := &config.Agent{
		Name:                    "test",
		WorkspaceDirectoryNames: []string{"nonexistent"},
	}
	city := &config.City{
		WorkspaceDirectories: []config.WorkspaceDirectory{
			{Name: "ansible", Path: "/path/to/ansible"},
		},
	}
	got := agentWorkspaceDirectories(a, city)
	if len(got) != 0 {
		t.Fatalf("expected 0 dirs for nonexistent name, got %d", len(got))
	}
}
