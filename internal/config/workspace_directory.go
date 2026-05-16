package config

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// rigInterpolationPattern matches {rig:<name>} placeholders in directory paths.
var rigInterpolationPattern = regexp.MustCompile(`\{rig:([^}]+)\}`)

// ResolveWorkspaceDirectoryPaths resolves {rig:name} interpolation in workspace
// directory paths and sets the ResolvedPath and ParentRig fields. Returns an
// error if a referenced rig does not exist.
func ResolveWorkspaceDirectoryPaths(dirs []WorkspaceDirectory, rigs []Rig) error {
	rigPaths := make(map[string]string, len(rigs))
	for _, r := range rigs {
		rigPaths[r.Name] = r.Path
	}

	for i := range dirs {
		resolved, parentRig, err := interpolateRigPath(dirs[i].Path, rigPaths)
		if err != nil {
			return fmt.Errorf("additional_directories[%d] %q: %w", i, dirs[i].Name, err)
		}
		dirs[i].ResolvedPath = resolved
		dirs[i].ParentRig = parentRig
	}
	return nil
}

// interpolateRigPath replaces {rig:<name>} placeholders with the rig's path.
// Returns the resolved path and the parent rig name (empty if no interpolation).
func interpolateRigPath(path string, rigPaths map[string]string) (string, string, error) {
	if !strings.Contains(path, "{rig:") {
		return filepath.Clean(path), "", nil
	}

	var resolveErr error
	var parentRig string
	result := rigInterpolationPattern.ReplaceAllStringFunc(path, func(match string) string {
		submatch := rigInterpolationPattern.FindStringSubmatch(match)
		if len(submatch) < 2 {
			resolveErr = fmt.Errorf("invalid rig interpolation: %s", match)
			return match
		}
		rigName := submatch[1]
		rigPath, ok := rigPaths[rigName]
		if !ok {
			resolveErr = fmt.Errorf("references unknown rig %q", rigName)
			return match
		}
		parentRig = rigName
		return rigPath
	})

	if resolveErr != nil {
		return "", "", resolveErr
	}
	return filepath.Clean(result), parentRig, nil
}

// WorkspaceDirectoryEnvVars returns environment variable mappings for the
// given workspace directories. Variable names are GC_DIR_<UPPER_SNAKE_NAME>.
func WorkspaceDirectoryEnvVars(dirs []WorkspaceDirectory) map[string]string {
	env := make(map[string]string, len(dirs))
	for _, d := range dirs {
		key := "GC_DIR_" + toUpperSnake(d.Name)
		path := d.ResolvedPath
		if path == "" {
			path = d.Path
		}
		env[key] = path
	}
	return env
}

// WorkspaceDirectoryMap returns a name→path map suitable for template injection.
func WorkspaceDirectoryMap(dirs []WorkspaceDirectory) map[string]string {
	m := make(map[string]string, len(dirs))
	for _, d := range dirs {
		path := d.ResolvedPath
		if path == "" {
			path = d.Path
		}
		m[d.Name] = path
	}
	return m
}

// FilterWorkspaceDirectories returns only directories whose names appear in
// the allowed list. If allowed is empty, returns all directories.
func FilterWorkspaceDirectories(dirs []WorkspaceDirectory, allowed []string) []WorkspaceDirectory {
	if len(allowed) == 0 {
		return dirs
	}
	allowSet := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		allowSet[name] = true
	}
	var filtered []WorkspaceDirectory
	for _, d := range dirs {
		if allowSet[d.Name] {
			filtered = append(filtered, d)
		}
	}
	return filtered
}

// toUpperSnake converts a name like "ansible_roles" or "ansible-roles" to
// "ANSIBLE_ROLES" for env var naming.
func toUpperSnake(name string) string {
	result := strings.ReplaceAll(name, "-", "_")
	return strings.ToUpper(result)
}
