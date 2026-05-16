package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/spf13/cobra"
)

func newDirCmd(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dir",
		Short: "Manage workspace directories",
		Long: `Manage workspace directories declared in the city config.

Workspace directories are named path references available to agents.
They are lighter than rigs: no beads database, no pack imports, no
agent scoping. Agents opt in to receive directory paths as environment
variables and template context.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				fmt.Fprintln(stderr, "gc dir: missing subcommand (list, status)") //nolint:errcheck // best-effort stderr
			} else {
				fmt.Fprintf(stderr, "gc dir: unknown subcommand %q\n", args[0]) //nolint:errcheck // best-effort stderr
			}
			return errExit
		},
	}
	cmd.AddCommand(
		newDirListCmd(stdout, stderr),
		newDirStatusCmd(stdout, stderr),
	)
	return cmd
}

func newDirListCmd(stdout, stderr io.Writer) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List workspace directories",
		Long:  "List all workspace directories declared in the city config with resolved paths.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cityPath, err := resolveCity()
			if err != nil {
				return err
			}
			cfg, err := loadCityConfig(cityPath)
			if err != nil {
				return err
			}

			dirs := cfg.WorkspaceDirectories
			if len(dirs) == 0 {
				fmt.Fprintln(stdout, "No workspace directories configured.")
				return nil
			}

			// Resolve {rig:name} interpolation.
			if err := config.ResolveWorkspaceDirectoryPaths(dirs, cfg.Rigs); err != nil {
				fmt.Fprintf(stderr, "warning: %v\n", err) //nolint:errcheck
			}

			if jsonOutput {
				return printDirListJSON(stdout, dirs)
			}

			w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tPATH\tGIT\tBRANCH")
			for _, d := range dirs {
				path := d.ResolvedPath
				if path == "" {
					path = d.Path
				}
				gitStr := "no"
				if d.GitEnabled {
					gitStr = "yes"
				}
				branch := "-"
				if d.DefaultBranch != "" {
					branch = d.DefaultBranch
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", d.Name, path, gitStr, branch)
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}

func newDirStatusCmd(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show workspace directory status",
		Long:  "Show status of workspace directories: existence on disk, git status if enabled.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cityPath, err := resolveCity()
			if err != nil {
				return err
			}
			cfg, err := loadCityConfig(cityPath)
			if err != nil {
				return err
			}

			dirs := cfg.WorkspaceDirectories
			if len(dirs) == 0 {
				fmt.Fprintln(stdout, "No workspace directories configured.")
				return nil
			}

			if err := config.ResolveWorkspaceDirectoryPaths(dirs, cfg.Rigs); err != nil {
				fmt.Fprintf(stderr, "warning: %v\n", err) //nolint:errcheck
			}

			w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tPATH\tEXISTS\tGIT\tBRANCH")
			for _, d := range dirs {
				path := d.ResolvedPath
				if path == "" {
					path = d.Path
				}
				checkPath := path
				if !filepath.IsAbs(checkPath) {
					checkPath = filepath.Join(cityPath, checkPath)
				}
				exists := "yes"
				if _, err := os.Stat(checkPath); os.IsNotExist(err) {
					exists = "NO"
				}
				gitStr := "-"
				if d.GitEnabled {
					gitStr = "enabled"
				}
				branch := "-"
				if d.DefaultBranch != "" {
					branch = d.DefaultBranch
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", d.Name, path, exists, gitStr, branch)
			}
			return w.Flush()
		},
	}
	return cmd
}

func printDirListJSON(w io.Writer, dirs []config.WorkspaceDirectory) error {
	type dirEntry struct {
		Name          string `json:"name"`
		Path          string `json:"path"`
		ResolvedPath  string `json:"resolved_path,omitempty"`
		GitEnabled    bool   `json:"git_enabled"`
		DefaultBranch string `json:"default_branch,omitempty"`
	}
	entries := make([]dirEntry, len(dirs))
	for i, d := range dirs {
		entries[i] = dirEntry{
			Name:          d.Name,
			Path:          d.Path,
			ResolvedPath:  d.ResolvedPath,
			GitEnabled:    d.GitEnabled,
			DefaultBranch: d.DefaultBranch,
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(entries)
}
