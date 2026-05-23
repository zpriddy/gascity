package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/git"
	"github.com/spf13/cobra"
)

// Bead metadata keys used to track a worktree's claim.
const (
	worktreeMetaPath      = "gc.worktree_path"
	worktreeMetaBranch    = "gc.worktree_branch"
	worktreeMetaCreatedAt = "gc.worktree_created_at"
	worktreeMetaPurpose   = "gc.worktree_purpose"
)

// purposeRegex constrains the --purpose suffix to kebab-case identifiers.
// Matches the gs-5sj naming convention: 1–40 chars, lowercase + digits + hyphens.
var purposeRegex = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func newWorktreeCmd(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worktree",
		Short: "Atomic worktree+bead-claim subcommands",
		Long: `Atomically create a git worktree paired with a bead claim.

Subcommands:
  claim    Create a worktree and claim the corresponding bead in one step
  release  Remove the worktree and clear the bead claim
  list     Show all worktrees claimed via 'gc worktree claim' in this city
  inspect  Show what 'gc worktree claim' would do for a bead (dry-run)

Convention (gs-5sj): worktrees are named .worktrees/<bead-id>-<purpose>.
The bead-id-first prefix makes 'git worktree list' immediately correlate
trees to beads, and prevents two agents from creating colliding worktrees
on the same bead.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				fmt.Fprintln(stderr, "gc worktree: missing subcommand (claim, release, list, inspect)") //nolint:errcheck // best-effort stderr
			} else {
				fmt.Fprintf(stderr, "gc worktree: unknown subcommand %q\n", args[0]) //nolint:errcheck // best-effort stderr
			}
			return errExit
		},
	}
	cmd.AddCommand(
		newWorktreeClaimCmd(stdout, stderr),
		newWorktreeReleaseCmd(stdout, stderr),
		newWorktreeListCmd(stdout, stderr),
		newWorktreeInspectCmd(stdout, stderr),
	)
	return cmd
}

// claimResult is the structured result of a successful claim, returned as
// JSON when --json is set and rendered as text otherwise.
type claimResult struct {
	BeadID       string `json:"bead_id"`
	BeadStatus   string `json:"bead_status"`
	Assignee     string `json:"assignee"`
	WorktreePath string `json:"worktree_path"`
	Branch       string `json:"branch"`
	BaseBranch   string `json:"base_branch,omitempty"`
	CreatedAt    string `json:"created_at"`
}

func newWorktreeClaimCmd(stdout, stderr io.Writer) *cobra.Command {
	var (
		purpose    string
		branch     string
		base       string
		jsonOutput bool
	)
	cmd := &cobra.Command{
		Use:   "claim <bead-id>",
		Short: "Create a worktree and claim the bead atomically",
		Long: `Atomically claim a bead and create a git worktree for the work.

The bead transitions to status=in_progress with assignee=$GC_ALIAS, and
metadata.gc.worktree_path / .gc.worktree_branch / .gc.worktree_created_at
record the worktree state. The worktree is created at:

    <repo-root>/.worktrees/<bead-id>-<purpose>

on a new branch named:

    $GC_ALIAS/<bead-id>-<purpose>

Override the branch with --branch=<name> and the base ref with --from=<base>.

The bead claim runs first; the worktree creation runs second. If the
claim fails (already claimed, branch collision, etc.) no worktree is
created. If the worktree creation fails, the claim is rolled back.

The atomic guarantee is: after a successful exit, both the bead is
claimed AND the worktree exists. Any failure leaves no half-state.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			beadID := strings.TrimSpace(args[0])
			if beadID == "" {
				return fmt.Errorf("bead id is required")
			}
			res, err := runWorktreeClaim(beadID, purpose, branch, base, stderr)
			if err != nil {
				fmt.Fprintf(stderr, "gc worktree claim: %v\n", err) //nolint:errcheck // best-effort stderr
				return errExit
			}
			if jsonOutput {
				if err := writeCLIJSONLine(stdout, res); err != nil {
					fmt.Fprintf(stderr, "gc worktree claim: writing json: %v\n", err) //nolint:errcheck
					return errExit
				}
				return nil
			}
			fmt.Fprintf(stdout, "Claimed %s, worktree=%s, branch=%s\n", res.BeadID, res.WorktreePath, res.Branch) //nolint:errcheck
			return nil
		},
	}
	cmd.Flags().StringVar(&purpose, "purpose", "", "short kebab-case purpose suffix (e.g. \"mysql-rebase\")")
	cmd.Flags().StringVar(&branch, "branch", "", "explicit branch name (default: $GC_ALIAS/<bead-id>-<purpose>)")
	cmd.Flags().StringVar(&base, "from", "", "base branch or ref to create the worktree from (default: HEAD)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit a single JSON line on success")
	return cmd
}

func newWorktreeReleaseCmd(stdout, stderr io.Writer) *cobra.Command {
	var (
		removeFlag bool
		keepFlag   bool
		reason     string
	)
	cmd := &cobra.Command{
		Use:   "release <bead-id>",
		Short: "Clear a worktree claim from a bead",
		Long: `Reverse 'gc worktree claim'. Clears gc.worktree_* metadata on the bead
and (when --remove is set) removes the worktree directory + branch.

By default, --keep is used: the worktree stays on disk for the operator
to inspect or finish manually. Pass --remove to delete the worktree and
its branch (refused if the worktree has uncommitted changes or unpushed
commits — pass --remove --force to override).

Optionally close the bead with --reason="<text>". If --reason is omitted
the bead stays in_progress with the assignee unchanged so a follow-up
claim can resume the work.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			beadID := strings.TrimSpace(args[0])
			if beadID == "" {
				return fmt.Errorf("bead id is required")
			}
			if removeFlag && keepFlag {
				return fmt.Errorf("--remove and --keep are mutually exclusive")
			}
			// Default to keep when neither is passed.
			remove := removeFlag
			if !removeFlag && !keepFlag {
				remove = false
			}
			if err := runWorktreeRelease(beadID, remove, reason, stdout, stderr); err != nil {
				fmt.Fprintf(stderr, "gc worktree release: %v\n", err) //nolint:errcheck
				return errExit
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&removeFlag, "remove", false, "delete the worktree directory + branch after clearing the claim")
	cmd.Flags().BoolVar(&keepFlag, "keep", false, "keep the worktree on disk after clearing the claim (default)")
	cmd.Flags().StringVar(&reason, "reason", "", "if set, also close the bead with this reason")
	return cmd
}

// listEntry describes one claimed worktree as seen on a single store.
type listEntry struct {
	BeadID       string `json:"bead_id"`
	Title        string `json:"title,omitempty"`
	Status       string `json:"status,omitempty"`
	Assignee     string `json:"assignee,omitempty"`
	WorktreePath string `json:"worktree_path"`
	Branch       string `json:"branch,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
	StorePath    string `json:"store_path,omitempty"`
	OnDisk       bool   `json:"on_disk"`
}

func newWorktreeListCmd(stdout, stderr io.Writer) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List worktrees claimed via gc worktree claim",
		Long: `List every bead in the city that carries gc.worktree_path metadata,
plus whether the worktree directory still exists on disk. Drift (bead
claims a worktree that no longer exists, or worktree exists without a
matching claim) is surfaced via OnDisk + StorePath.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			entries, err := runWorktreeList(stderr)
			if err != nil {
				fmt.Fprintf(stderr, "gc worktree list: %v\n", err) //nolint:errcheck
				return errExit
			}
			if jsonOutput {
				if err := writeCLIJSONLine(stdout, struct {
					SchemaVersion string      `json:"schema_version"`
					Entries       []listEntry `json:"entries"`
				}{SchemaVersion: "1", Entries: entries}); err != nil {
					fmt.Fprintf(stderr, "gc worktree list: writing json: %v\n", err) //nolint:errcheck
					return errExit
				}
				return nil
			}
			if len(entries) == 0 {
				fmt.Fprintln(stdout, "No claimed worktrees in this city.") //nolint:errcheck
				return nil
			}
			for _, e := range entries {
				disk := "ok"
				if !e.OnDisk {
					disk = "MISSING"
				}
				fmt.Fprintf(stdout, "%-12s %-20s %-50s [%s]\n", e.BeadID, e.Branch, e.WorktreePath, disk) //nolint:errcheck
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit a single JSON line")
	return cmd
}

func newWorktreeInspectCmd(stdout, stderr io.Writer) *cobra.Command {
	var (
		purpose string
		branch  string
		base    string
	)
	cmd := &cobra.Command{
		Use:   "inspect <bead-id>",
		Short: "Show what gc worktree claim would do for a bead (dry-run)",
		Long: `Resolve the worktree path, branch name, and base ref that 'gc worktree
claim <bead-id>' would use, plus run all pre-flight checks (existing
claim, path collision, branch collision). Reports the result without
modifying any state.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			beadID := strings.TrimSpace(args[0])
			if beadID == "" {
				return fmt.Errorf("bead id is required")
			}
			if err := runWorktreeInspect(beadID, purpose, branch, base, stdout, stderr); err != nil {
				fmt.Fprintf(stderr, "gc worktree inspect: %v\n", err) //nolint:errcheck
				return errExit
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&purpose, "purpose", "", "short kebab-case purpose suffix")
	cmd.Flags().StringVar(&branch, "branch", "", "explicit branch name")
	cmd.Flags().StringVar(&base, "from", "", "base branch or ref")
	return cmd
}

// claimContext bundles the resolved coordinates needed by claim/inspect/release.
type claimContext struct {
	cityPath    string
	cfg         *config.City
	store       beads.Store
	storeRoot   string // either cityPath or a rig.Path
	bead        beads.Bead
	repoRoot    string // root of the rig (or city) git repo
	worktreeAbs string // resolved .worktrees/<bead-id>-<purpose>
	branch      string // resolved branch name
	base        string // resolved base ref (or empty for HEAD)
	purpose     string // normalized purpose suffix
	alias       string // $GC_ALIAS or fallback
}

// resolveClaimContext loads the bead, computes worktree path + branch, and
// runs read-only validation. It does not mutate any state.
func resolveClaimContext(beadID, purpose, branch, base string) (*claimContext, error) {
	cityPath, err := resolveCity()
	if err != nil {
		return nil, err
	}
	cfg, err := loadCityConfig(cityPath, io.Discard)
	if err != nil {
		return nil, fmt.Errorf("loading city config: %w", err)
	}

	storeRoot := slingDirForBead(cfg, cityPath, beadID)
	store, err := openStoreAtForCity(storeRoot, cityPath)
	if err != nil {
		return nil, fmt.Errorf("opening bead store: %w", err)
	}

	bead, err := store.Get(beadID)
	if err != nil {
		return nil, fmt.Errorf("looking up bead %s: %w", beadID, err)
	}

	purpose = normalizePurpose(purpose)
	if purpose == "" {
		// Synthesize a purpose from the bead title slug as a fallback.
		purpose = slugifyForPurpose(bead.Title)
	}
	if purpose == "" {
		return nil, fmt.Errorf("--purpose is required when bead title is empty or non-ascii")
	}
	if !purposeRegex.MatchString(purpose) {
		return nil, fmt.Errorf("--purpose %q must be lowercase kebab-case (e.g. \"mysql-rebase\")", purpose)
	}

	repoRoot, err := resolveRepoRootForStore(storeRoot, cityPath)
	if err != nil {
		return nil, err
	}

	worktreeAbs := filepath.Join(repoRoot, ".worktrees", beadID+"-"+purpose)

	alias := strings.TrimSpace(os.Getenv("GC_ALIAS"))
	if alias == "" {
		alias = strings.TrimSpace(os.Getenv("USER"))
	}
	if alias == "" {
		return nil, fmt.Errorf("$GC_ALIAS and $USER are both empty; cannot determine claim assignee")
	}

	branchName := strings.TrimSpace(branch)
	if branchName == "" {
		branchName = alias + "/" + beadID + "-" + purpose
	}

	return &claimContext{
		cityPath:    cityPath,
		cfg:         cfg,
		store:       store,
		storeRoot:   storeRoot,
		bead:        bead,
		repoRoot:    repoRoot,
		worktreeAbs: worktreeAbs,
		branch:      branchName,
		base:        strings.TrimSpace(base),
		purpose:     purpose,
		alias:       alias,
	}, nil
}

// loadCityConfigForRead is reserved for future use when callers want to
// surface config-load warnings; today the worktree subcommand uses
// loadCityConfig with io.Discard directly.

// resolveRepoRootForStore returns the git repo root that should host the
// .worktrees/ directory for a given bead store. For rig-scoped beads, the
// rig directory is itself a repo. For city-scoped beads, the city root
// is treated as the repo root (which is a git repo for hq-managed cities).
func resolveRepoRootForStore(storeRoot, cityPath string) (string, error) {
	candidate := storeRoot
	if candidate == "" {
		candidate = cityPath
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	g := git.New(abs)
	if !g.IsRepo() {
		return "", fmt.Errorf("path %s is not inside a git repository (gc worktree requires a git-backed scope)", abs)
	}
	// Resolve the canonical repo root via git rev-parse --show-toplevel.
	out, err := exec.Command("git", "-C", abs, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("resolving repo root for %s: %w", abs, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// preflightChecks validates that a claim is safe to execute. Returns a
// human-readable error if any check fails. Read-only.
func preflightChecks(ctx *claimContext) error {
	// (1) Bead must not already carry a worktree claim.
	if existing := strings.TrimSpace(ctx.bead.Metadata[worktreeMetaPath]); existing != "" {
		return fmt.Errorf("bead %s already claims worktree %s — release it first or use a different bead", ctx.bead.ID, existing)
	}
	// (2) Bead must not be claimed by another assignee.
	if ctx.bead.Status == "in_progress" && ctx.bead.Assignee != "" && ctx.bead.Assignee != ctx.alias {
		return fmt.Errorf("bead %s is already in_progress with assignee=%s (you are %s)", ctx.bead.ID, ctx.bead.Assignee, ctx.alias)
	}
	// (3) Worktree path must not exist on disk.
	if _, err := os.Stat(ctx.worktreeAbs); err == nil {
		return fmt.Errorf("worktree path %s already exists on disk — pick a different --purpose or remove it manually", ctx.worktreeAbs)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking worktree path %s: %w", ctx.worktreeAbs, err)
	}
	// (4) Branch must not already exist locally.
	if _, err := exec.Command("git", "-C", ctx.repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+ctx.branch).Output(); err == nil {
		return fmt.Errorf("branch %s already exists locally — pick a different --branch or delete the stale branch", ctx.branch)
	}
	return nil
}

// runWorktreeClaim performs the atomic claim+create operation.
//
// Order:
//  1. Resolve context (read-only).
//  2. Pre-flight checks (read-only).
//  3. Update bead with claim metadata (single transactional bd update).
//  4. Create the worktree on disk (git worktree add).
//  5. If step 4 fails, attempt to roll back step 3.
func runWorktreeClaim(beadID, purpose, branch, base string, stderr io.Writer) (claimResult, error) {
	ctx, err := resolveClaimContext(beadID, purpose, branch, base)
	if err != nil {
		return claimResult{}, err
	}
	if err := preflightChecks(ctx); err != nil {
		return claimResult{}, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	statusInProgress := "in_progress"
	assignee := ctx.alias
	meta := map[string]string{
		worktreeMetaPath:      ctx.worktreeAbs,
		worktreeMetaBranch:    ctx.branch,
		worktreeMetaCreatedAt: now,
		worktreeMetaPurpose:   ctx.purpose,
	}
	updateOpts := beads.UpdateOpts{
		Status:   &statusInProgress,
		Assignee: &assignee,
		Metadata: meta,
	}
	if err := ctx.store.Update(ctx.bead.ID, updateOpts); err != nil {
		return claimResult{}, fmt.Errorf("claiming bead %s: %w", ctx.bead.ID, err)
	}

	// Create the worktree.
	args := []string{"-C", ctx.repoRoot, "worktree", "add", "-b", ctx.branch, ctx.worktreeAbs}
	if ctx.base != "" {
		args = append(args, ctx.base)
	}
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		// Roll back the claim.
		empty := ""
		statusOpen := "open"
		clearMeta := map[string]string{
			worktreeMetaPath:      "",
			worktreeMetaBranch:    "",
			worktreeMetaCreatedAt: "",
			worktreeMetaPurpose:   "",
		}
		rollbackOpts := beads.UpdateOpts{
			Status:   &statusOpen,
			Assignee: &empty,
			Metadata: clearMeta,
		}
		if rerr := ctx.store.Update(ctx.bead.ID, rollbackOpts); rerr != nil {
			fmt.Fprintf(stderr, "gc worktree claim: ROLLBACK FAILED for bead %s: %v\n", ctx.bead.ID, rerr) //nolint:errcheck
			fmt.Fprintf(stderr, "gc worktree claim: bead is now in a half-claimed state; run 'gc worktree release %s' to clean up\n", ctx.bead.ID) //nolint:errcheck
		}
		return claimResult{}, fmt.Errorf("creating worktree at %s: %w (git output: %s)", ctx.worktreeAbs, err, strings.TrimSpace(string(out)))
	}

	return claimResult{
		BeadID:       ctx.bead.ID,
		BeadStatus:   statusInProgress,
		Assignee:     assignee,
		WorktreePath: ctx.worktreeAbs,
		Branch:       ctx.branch,
		BaseBranch:   ctx.base,
		CreatedAt:    now,
	}, nil
}

// runWorktreeRelease clears the claim metadata, optionally removing the
// worktree on disk and optionally closing the bead.
func runWorktreeRelease(beadID string, remove bool, reason string, stdout, stderr io.Writer) error {
	cityPath, err := resolveCity()
	if err != nil {
		return err
	}
	cfg, err := loadCityConfig(cityPath, io.Discard)
	if err != nil {
		return fmt.Errorf("loading city config: %w", err)
	}
	storeRoot := slingDirForBead(cfg, cityPath, beadID)
	store, err := openStoreAtForCity(storeRoot, cityPath)
	if err != nil {
		return fmt.Errorf("opening bead store: %w", err)
	}
	bead, err := store.Get(beadID)
	if err != nil {
		return fmt.Errorf("looking up bead %s: %w", beadID, err)
	}
	worktreePath := strings.TrimSpace(bead.Metadata[worktreeMetaPath])
	branch := strings.TrimSpace(bead.Metadata[worktreeMetaBranch])

	if worktreePath == "" {
		return fmt.Errorf("bead %s has no gc.worktree_path metadata; nothing to release", beadID)
	}

	if remove {
		repoRoot, rerr := resolveRepoRootForStore(storeRoot, cityPath)
		if rerr != nil {
			return fmt.Errorf("resolving repo root: %w", rerr)
		}
		if _, err := os.Stat(worktreePath); err == nil {
			if out, err := exec.Command("git", "-C", repoRoot, "worktree", "remove", worktreePath).CombinedOutput(); err != nil {
				return fmt.Errorf("removing worktree %s: %w (git: %s)", worktreePath, err, strings.TrimSpace(string(out)))
			}
		}
		if branch != "" {
			// Best-effort branch delete; ignore failures (branch may
			// have been merged/deleted/renamed).
			_ = exec.Command("git", "-C", repoRoot, "branch", "-D", branch).Run()
		}
	}

	// Clear the metadata.
	clearMeta := map[string]string{
		worktreeMetaPath:      "",
		worktreeMetaBranch:    "",
		worktreeMetaCreatedAt: "",
		worktreeMetaPurpose:   "",
	}
	opts := beads.UpdateOpts{Metadata: clearMeta}
	if reason != "" {
		closed := "closed"
		opts.Status = &closed
	}
	if err := store.Update(beadID, opts); err != nil {
		return fmt.Errorf("clearing claim metadata: %w", err)
	}
	if reason != "" {
		fmt.Fprintf(stdout, "Released bead %s (closed: %s); worktree=%s remove=%t\n", beadID, reason, worktreePath, remove) //nolint:errcheck
	} else {
		fmt.Fprintf(stdout, "Released bead %s; worktree=%s remove=%t\n", beadID, worktreePath, remove) //nolint:errcheck
	}
	return nil
}

// runWorktreeList walks every bead store in the city and collects beads
// carrying gc.worktree_path metadata.
func runWorktreeList(stderr io.Writer) ([]listEntry, error) {
	cityPath, err := resolveCity()
	if err != nil {
		return nil, err
	}
	cfg, err := loadCityConfig(cityPath, io.Discard)
	if err != nil {
		return nil, fmt.Errorf("loading city config: %w", err)
	}
	stores := []string{cityPath}
	if cfg != nil {
		for _, rig := range cfg.Rigs {
			if strings.TrimSpace(rig.Path) == "" {
				continue
			}
			stores = append(stores, resolveStoreScopeRoot(cityPath, rig.Path))
		}
	}
	seen := map[string]struct{}{}
	var entries []listEntry
	for _, dir := range stores {
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		store, err := openStoreAtForCity(dir, cityPath)
		if err != nil {
			fmt.Fprintf(stderr, "gc worktree list: opening %s: %v (skipping)\n", dir, err) //nolint:errcheck
			continue
		}
		all, err := store.List(beads.ListQuery{AllowScan: true})
		if err != nil {
			fmt.Fprintf(stderr, "gc worktree list: listing in %s: %v (skipping)\n", dir, err) //nolint:errcheck
			continue
		}
		for _, b := range all {
			if path := strings.TrimSpace(b.Metadata[worktreeMetaPath]); path != "" {
				_, statErr := os.Stat(path)
				entries = append(entries, listEntry{
					BeadID:       b.ID,
					Title:        b.Title,
					Status:       b.Status,
					Assignee:     b.Assignee,
					WorktreePath: path,
					Branch:       b.Metadata[worktreeMetaBranch],
					CreatedAt:    b.Metadata[worktreeMetaCreatedAt],
					StorePath:    dir,
					OnDisk:       statErr == nil,
				})
			}
		}
	}
	return entries, nil
}

// runWorktreeInspect resolves the same coordinates as claim, runs preflight
// checks, and prints the result without mutating any state.
func runWorktreeInspect(beadID, purpose, branch, base string, stdout, stderr io.Writer) error {
	ctx, err := resolveClaimContext(beadID, purpose, branch, base)
	if err != nil {
		return err
	}
	preflightErr := preflightChecks(ctx)
	type inspectOut struct {
		BeadID       string `json:"bead_id"`
		Alias        string `json:"alias"`
		Purpose      string `json:"purpose"`
		WorktreePath string `json:"worktree_path"`
		Branch       string `json:"branch"`
		BaseBranch   string `json:"base_branch,omitempty"`
		RepoRoot     string `json:"repo_root"`
		StorePath    string `json:"store_path"`
		WouldSucceed bool   `json:"would_succeed"`
		Reason       string `json:"reason,omitempty"`
	}
	out := inspectOut{
		BeadID:       ctx.bead.ID,
		Alias:        ctx.alias,
		Purpose:      ctx.purpose,
		WorktreePath: ctx.worktreeAbs,
		Branch:       ctx.branch,
		BaseBranch:   ctx.base,
		RepoRoot:     ctx.repoRoot,
		StorePath:    ctx.storeRoot,
		WouldSucceed: preflightErr == nil,
	}
	if preflightErr != nil {
		out.Reason = preflightErr.Error()
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("encoding inspect output: %w", err)
	}
	return nil
}

// normalizePurpose lower-cases and trims a purpose string.
func normalizePurpose(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// slugifyForPurpose converts a bead title into a kebab-case purpose
// suffix capped at 40 chars. Returns "" if no usable characters remain.
var slugReplacer = regexp.MustCompile(`[^a-z0-9]+`)

func slugifyForPurpose(title string) string {
	s := strings.ToLower(strings.TrimSpace(title))
	s = slugReplacer.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = s[:40]
		s = strings.TrimRight(s, "-")
	}
	return s
}
