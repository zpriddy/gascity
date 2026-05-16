# Workspace Directories — Design Spec

**Date**: 2026-05-16
**Branch**: `feat/workspace-directories`
**Status**: Design approved, implementation pending

## Summary

Add a `[[workspace_directories]]` config section to city.toml that declares named directories available to agents. These are lighter than rigs — no beads DB, no pack imports, no agent scoping — but provide named path references, optional git capability, and automatic injection into agent environments.

## Motivation

Cities need to reference supporting directories (Ansible roles, deployment subdirs, shared configs) without promoting them to full rigs. Two cities should be able to share common folders without each maintaining separate beads databases or rig registrations for simple path references.

## Config Schema

### city.toml

```toml
[workspace]
name = "my-city"

# Standalone directory (absolute path)
[[workspace_directories]]
name = "ansible_roles"
path = "/path/to/ansible/roles"

# Nested inside a rig (interpolated path)
[[workspace_directories]]
name = "dev_deployment"
path = "{rig:deployments}/dev"
git_enabled = true
default_branch = "main"

# Multiple related directories
[[workspace_directories]]
name = "ansible_playbooks"
path = "{rig:ansible}/playbooks"
git_enabled = true
default_branch = "main"

[[workspace_directories]]
name = "ansible_inventory"
path = "{rig:ansible}/inventory"
```

### Go struct (`internal/config/config.go`)

```go
// WorkspaceDirectory declares a named directory available to agents.
// Lighter than a rig: no beads DB, no pack imports, no agent scoping.
type WorkspaceDirectory struct {
    // Name is the unique identifier for this directory.
    Name string `toml:"name" jsonschema:"required"`
    // Path is the filesystem path. May use {rig:name} interpolation
    // for paths nested inside a rig.
    Path string `toml:"path" jsonschema:"required"`
    // GitEnabled allows agents to make git commits in this directory.
    GitEnabled bool `toml:"git_enabled,omitempty"`
    // DefaultBranch is the directory's mainline branch when git_enabled=true.
    DefaultBranch string `toml:"default_branch,omitempty"`
}
```

### Agent opt-in

```toml
[[agent]]
name = "dev-deployer"
scope = "city"
# Include all workspace directories in this agent's context
include_workspace_directories = true

[[agent]]
name = "ansible-runner"
scope = "rig"
# Include only specific directories
workspace_directories = ["ansible_roles", "ansible_playbooks"]
```

## Beads Scoping

**City-scoped** (default). Work performed by agents operating in workspace directories is tracked in the city's beads DB, not in a per-directory DB. This is consistent with the "lighter than rigs" philosophy.

## Path Interpolation

The `{rig:name}` syntax resolves at config load time during `LoadWithIncludes`:

1. Parse the path string for `{rig:<name>}` patterns
2. Look up the named rig in the city's `[[rigs]]` list
3. Replace with the rig's resolved absolute path
4. Validate the final path exists on disk (warning if not)

Invalid rig references produce a config load error.

## Agent Injection

When an agent has `include_workspace_directories = true` or specifies directories in `workspace_directories = [...]`:

### Environment Variables

Injected as `GC_DIR_<UPPER_SNAKE_NAME>=<resolved_path>`:
```
GC_DIR_ANSIBLE_ROLES=/path/to/ansible/roles
GC_DIR_DEV_DEPLOYMENT=/home/user/repos/deployments/dev
```

### Prompt Template Variables

Added to the existing `PathContext` (or a new `DirectoryContext` map):
```go
// Extended PathContext
type PathContext struct {
    Agent     string
    AgentBase string
    Rig       string
    RigRoot   string
    CityRoot  string
    CityName  string
    // NEW: workspace directories as a map
    Dirs      map[string]string
}
```

Templates can reference: `{{.Dirs.ansible_roles}}`, `{{index .Dirs "dev_deployment"}}`

### Prompt Injection (Fragment)

When `include_workspace_directories = true`, a standard fragment is appended:
```
## Available Workspace Directories
- ansible_roles: /path/to/ansible/roles
- dev_deployment: /home/user/repos/deployments/dev [git: main]
```

## CLI Commands

### `gc dir list`

Lists all workspace directories with resolved paths and git status.

```
$ gc dir list
NAME                PATH                                    GIT     BRANCH
ansible_roles       /path/to/ansible/roles                  no      -
dev_deployment      /home/user/repos/deployments/dev        yes     main
ansible_playbooks   /path/to/ansible/playbooks              yes     main
```

### `gc dir status`

Shows directory status (exists on disk, git status if enabled).

## Implementation Plan

### Phase 1: Config (internal/config)

1. Add `WorkspaceDirectory` struct
2. Add `WorkspaceDirectories []WorkspaceDirectory` to `City` struct
3. Add `IncludeWorkspaceDirectories *bool` and `WorkspaceDirectories []string` to `Agent` struct
4. Implement `{rig:name}` path interpolation in compose/load
5. Add validation (unique names, rig references exist, paths exist)

### Phase 2: Workdir + Env (internal/workdir, internal/session)

1. Extend `PathContext` with `Dirs map[string]string`
2. Inject `GC_DIR_*` env vars into agent sessions when opted-in
3. Resolve directory paths during session startup

### Phase 3: Prompt Integration

1. Create a workspace-directories fragment template
2. Auto-append when `include_workspace_directories = true`
3. Support selective directory listing via `workspace_directories = [...]`

### Phase 4: CLI (cmd/gc)

1. Add `gc dir` command group
2. Implement `list` and `status` subcommands
3. Wire into existing `gc config show` output

### Phase 5: Tests

1. Unit tests for config parsing + validation
2. Unit tests for path interpolation
3. Unit tests for env injection
4. Integration test in proving-grounds

## Scope Boundaries

**In scope:**
- Config schema + parsing
- Path interpolation
- Env var injection
- Prompt template vars
- CLI listing
- Git-enabled flag (commit, not push)

**Out of scope (future):**
- `gc dir add/remove` (manual edit for now)
- Directory-scoped agents (agents with `scope = "directory"`)
- Cross-city directory sharing via Dolt
- Directory-level beads (always city-scoped)
