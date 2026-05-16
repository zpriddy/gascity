package config

import (
	"testing"

	"github.com/BurntSushi/toml"
)

func TestValidateWorkspaceDirectories(t *testing.T) {
	tests := []struct {
		name    string
		dirs    []WorkspaceDirectory
		wantErr string
	}{
		{
			name: "valid standalone",
			dirs: []WorkspaceDirectory{
				{Name: "ansible_roles", Path: "/path/to/roles"},
				{Name: "ansible_playbooks", Path: "/path/to/playbooks"},
			},
		},
		{
			name: "valid with git",
			dirs: []WorkspaceDirectory{
				{Name: "dev", Path: "/deploy/dev", GitEnabled: true, DefaultBranch: "main"},
			},
		},
		{
			name:    "missing name",
			dirs:    []WorkspaceDirectory{{Path: "/path"}},
			wantErr: "name is required",
		},
		{
			name:    "missing path",
			dirs:    []WorkspaceDirectory{{Name: "test"}},
			wantErr: "path is required",
		},
		{
			name: "duplicate name",
			dirs: []WorkspaceDirectory{
				{Name: "dup", Path: "/a"},
				{Name: "dup", Path: "/b"},
			},
			wantErr: "duplicate name",
		},
		{
			name: "default_branch without git_enabled",
			dirs: []WorkspaceDirectory{
				{Name: "test", Path: "/a", DefaultBranch: "main"},
			},
			wantErr: "default_branch set but git_enabled is false",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWorkspaceDirectories(tt.dirs)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestResolveWorkspaceDirectoryPaths(t *testing.T) {
	rigs := []Rig{
		{Name: "ansible", Path: "/home/user/repos/ansible"},
		{Name: "deployments", Path: "/home/user/repos/deployments"},
	}

	tests := []struct {
		name     string
		dirs     []WorkspaceDirectory
		wantPath string
		wantErr  string
	}{
		{
			name:     "standalone path unchanged",
			dirs:     []WorkspaceDirectory{{Name: "test", Path: "/absolute/path"}},
			wantPath: "/absolute/path",
		},
		{
			name:     "rig interpolation",
			dirs:     []WorkspaceDirectory{{Name: "roles", Path: "{rig:ansible}/roles"}},
			wantPath: "/home/user/repos/ansible/roles",
		},
		{
			name:     "rig interpolation with subpath",
			dirs:     []WorkspaceDirectory{{Name: "dev", Path: "{rig:deployments}/dev/terraform"}},
			wantPath: "/home/user/repos/deployments/dev/terraform",
		},
		{
			name:    "unknown rig reference",
			dirs:    []WorkspaceDirectory{{Name: "bad", Path: "{rig:nonexistent}/path"}},
			wantErr: "references unknown rig",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ResolveWorkspaceDirectoryPaths(tt.dirs, rigs)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.dirs[0].ResolvedPath != tt.wantPath {
				t.Fatalf("ResolvedPath = %q, want %q", tt.dirs[0].ResolvedPath, tt.wantPath)
			}
		})
	}
}

func TestWorkspaceDirectoryEnvVars(t *testing.T) {
	dirs := []WorkspaceDirectory{
		{Name: "ansible_roles", Path: "/path/to/roles", ResolvedPath: "/path/to/roles"},
		{Name: "dev-deployment", Path: "{rig:deploy}/dev", ResolvedPath: "/repos/deploy/dev"},
	}

	env := WorkspaceDirectoryEnvVars(dirs)

	if env["GC_DIR_ANSIBLE_ROLES"] != "/path/to/roles" {
		t.Errorf("GC_DIR_ANSIBLE_ROLES = %q, want %q", env["GC_DIR_ANSIBLE_ROLES"], "/path/to/roles")
	}
	if env["GC_DIR_DEV_DEPLOYMENT"] != "/repos/deploy/dev" {
		t.Errorf("GC_DIR_DEV_DEPLOYMENT = %q, want %q", env["GC_DIR_DEV_DEPLOYMENT"], "/repos/deploy/dev")
	}
}

func TestWorkspaceDirectoryMap(t *testing.T) {
	dirs := []WorkspaceDirectory{
		{Name: "ansible_roles", ResolvedPath: "/path/to/roles"},
		{Name: "dev", ResolvedPath: "/repos/deploy/dev"},
	}

	m := WorkspaceDirectoryMap(dirs)

	if m["ansible_roles"] != "/path/to/roles" {
		t.Errorf("ansible_roles = %q, want %q", m["ansible_roles"], "/path/to/roles")
	}
	if m["dev"] != "/repos/deploy/dev" {
		t.Errorf("dev = %q, want %q", m["dev"], "/repos/deploy/dev")
	}
}

func TestFilterWorkspaceDirectories(t *testing.T) {
	dirs := []WorkspaceDirectory{
		{Name: "a", Path: "/a"},
		{Name: "b", Path: "/b"},
		{Name: "c", Path: "/c"},
	}

	t.Run("empty filter returns all", func(t *testing.T) {
		got := FilterWorkspaceDirectories(dirs, nil)
		if len(got) != 3 {
			t.Fatalf("got %d dirs, want 3", len(got))
		}
	})

	t.Run("selective filter", func(t *testing.T) {
		got := FilterWorkspaceDirectories(dirs, []string{"a", "c"})
		if len(got) != 2 {
			t.Fatalf("got %d dirs, want 2", len(got))
		}
		if got[0].Name != "a" || got[1].Name != "c" {
			t.Fatalf("got names %q and %q, want a and c", got[0].Name, got[1].Name)
		}
	})
}

func TestToUpperSnake(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"ansible_roles", "ANSIBLE_ROLES"},
		{"dev-deployment", "DEV_DEPLOYMENT"},
		{"simple", "SIMPLE"},
		{"mixed-case_name", "MIXED_CASE_NAME"},
	}
	for _, tt := range tests {
		got := toUpperSnake(tt.input)
		if got != tt.want {
			t.Errorf("toUpperSnake(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestParseAdditionalDirectoriesFromTOML(t *testing.T) {
	input := `
[workspace]
provider = "claude"

[[rigs]]
name = "my_rig"
prefix = "mr"
path = "/repos/my_rig"

[[additional_directories]]
name = "ansible_roles"
path = "/path/to/roles"

[[additional_directories]]
name = "dev_deploy"
path = "{rig:my_rig}/dev"
git_enabled = true
default_branch = "main"

[daemon]
patrol_interval = "60s"
`
	var cfg City
	_, err := toml.Decode(input, &cfg)
	if err != nil {
		t.Fatalf("TOML decode error: %v", err)
	}

	if len(cfg.WorkspaceDirectories) != 2 {
		t.Fatalf("got %d directories, want 2", len(cfg.WorkspaceDirectories))
	}

	d1 := cfg.WorkspaceDirectories[0]
	if d1.Name != "ansible_roles" {
		t.Errorf("dir[0].Name = %q, want %q", d1.Name, "ansible_roles")
	}
	if d1.Path != "/path/to/roles" {
		t.Errorf("dir[0].Path = %q, want %q", d1.Path, "/path/to/roles")
	}
	if d1.GitEnabled {
		t.Error("dir[0].GitEnabled should be false")
	}

	d2 := cfg.WorkspaceDirectories[1]
	if d2.Name != "dev_deploy" {
		t.Errorf("dir[1].Name = %q, want %q", d2.Name, "dev_deploy")
	}
	if d2.Path != "{rig:my_rig}/dev" {
		t.Errorf("dir[1].Path = %q, want %q", d2.Path, "{rig:my_rig}/dev")
	}
	if !d2.GitEnabled {
		t.Error("dir[1].GitEnabled should be true")
	}
	if d2.DefaultBranch != "main" {
		t.Errorf("dir[1].DefaultBranch = %q, want %q", d2.DefaultBranch, "main")
	}

	// Verify rig interpolation works.
	err = ResolveWorkspaceDirectoryPaths(cfg.WorkspaceDirectories, cfg.Rigs)
	if err != nil {
		t.Fatalf("ResolveWorkspaceDirectoryPaths error: %v", err)
	}
	if cfg.WorkspaceDirectories[1].ResolvedPath != "/repos/my_rig/dev" {
		t.Errorf("dir[1].ResolvedPath = %q, want %q", cfg.WorkspaceDirectories[1].ResolvedPath, "/repos/my_rig/dev")
	}
	if cfg.WorkspaceDirectories[1].ParentRig != "my_rig" {
		t.Errorf("dir[1].ParentRig = %q, want %q", cfg.WorkspaceDirectories[1].ParentRig, "my_rig")
	}
}

func TestBackwardsCompatibility_NoDirectories(t *testing.T) {
	input := `
[workspace]
provider = "claude"

[[rigs]]
name = "test"
prefix = "t"
path = "/repos/test"

[daemon]
patrol_interval = "60s"
`
	var cfg City
	_, err := toml.Decode(input, &cfg)
	if err != nil {
		t.Fatalf("TOML decode error: %v", err)
	}
	if len(cfg.WorkspaceDirectories) != 0 {
		t.Fatalf("expected 0 directories, got %d", len(cfg.WorkspaceDirectories))
	}
	if len(cfg.Rigs) != 1 {
		t.Fatalf("expected 1 rig, got %d", len(cfg.Rigs))
	}
}
