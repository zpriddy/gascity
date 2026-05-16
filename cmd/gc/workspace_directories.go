package main

import "github.com/gastownhall/gascity/internal/config"

// agentWorkspaceDirectories returns the resolved workspace directories that
// should be injected into an agent's environment. Returns nil if the agent
// has not opted in.
func agentWorkspaceDirectories(a *config.Agent, city *config.City) []config.WorkspaceDirectory {
	if city == nil || len(city.WorkspaceDirectories) == 0 {
		return nil
	}

	// Selective: specific directories by name.
	if len(a.WorkspaceDirectoryNames) > 0 {
		dirs := config.FilterWorkspaceDirectories(city.WorkspaceDirectories, a.WorkspaceDirectoryNames)
		_ = config.ResolveWorkspaceDirectoryPaths(dirs, city.Rigs)
		return dirs
	}

	// All: include_workspace_directories = true.
	if a.IncludeWorkspaceDirectories != nil && *a.IncludeWorkspaceDirectories {
		dirs := make([]config.WorkspaceDirectory, len(city.WorkspaceDirectories))
		copy(dirs, city.WorkspaceDirectories)
		_ = config.ResolveWorkspaceDirectoryPaths(dirs, city.Rigs)
		return dirs
	}

	return nil
}
