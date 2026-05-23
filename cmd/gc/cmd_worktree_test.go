package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// TestSlugifyForPurpose verifies the auto-slug fallback used when no
// --purpose is passed. Pure-function unit tests, no I/O.
func TestSlugifyForPurpose(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"Hello World", "hello-world"},
		{"  trim me  ", "trim-me"},
		{"snake_case_input", "snake-case-input"},
		{"non-ascii: ✨ 🚀", "non-ascii"},
		{"!!!---!!!", ""},
		{"a-very-long-title-that-exceeds-the-forty-char-cap", "a-very-long-title-that-exceeds-the-forty"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := slugifyForPurpose(c.in)
			if got != c.want {
				t.Fatalf("slugifyForPurpose(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestPurposeRegex documents the kebab-case constraint.
func TestPurposeRegex(t *testing.T) {
	good := []string{"foo", "foo-bar", "foo-123", "abc-def-ghi"}
	bad := []string{"", "Foo", "foo_bar", "foo--bar", "-foo", "foo-", "foo.bar", "foo bar"}
	for _, s := range good {
		if !purposeRegex.MatchString(s) {
			t.Errorf("expected %q to match purposeRegex", s)
		}
	}
	for _, s := range bad {
		if purposeRegex.MatchString(s) {
			t.Errorf("expected %q to NOT match purposeRegex", s)
		}
	}
}

// worktreeTestSetup spins up a city + a git-backed rig + a file-backed
// bead store containing one open bead. Returns the city path and the
// bead ID. All paths are temp dirs cleaned up by the test.
//
// The bead is created in the CITY store, so worktrees materialize at
// <cityPath>/.worktrees/. The cityPath is git-init'd so resolveRepoRoot
// can find a parent repo.
func worktreeTestSetup(t *testing.T) (cityPath, beadID, repoRoot string) {
	t.Helper()

	cityPath = t.TempDir()
	if err := os.MkdirAll(filepath.Join(cityPath, ".gc"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.gc): %v", err)
	}

	// Make the city dir itself a git repo so worktree creation works.
	runGitInTest(t, cityPath, "init", "-q")
	runGitInTest(t, cityPath, "config", "user.email", "test@test.com")
	runGitInTest(t, cityPath, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(cityPath, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatalf("WriteFile README: %v", err)
	}
	runGitInTest(t, cityPath, "add", "README.md")
	runGitInTest(t, cityPath, "commit", "-q", "-m", "init")
	repoRoot = cityPath

	// Minimal city.toml using the file-backed bead provider so we don't
	// need bd/dolt/mysql in tests.
	cityToml := `[workspace]
name = "wt-test-city"

[beads]
provider = "file"
`
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"), []byte(cityToml), 0o644); err != nil {
		t.Fatalf("WriteFile city.toml: %v", err)
	}

	// Seed a bead in the city store.
	store, err := openCityStoreAt(cityPath)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	bead, err := store.Create(beads.Bead{
		Title: "Test bead for worktree claim",
		Type:  "task",
	})
	if err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	beadID = bead.ID

	t.Setenv("GC_CITY", cityPath)
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_ALIAS", "test-agent")
	return cityPath, beadID, repoRoot
}

func TestRunWorktreeClaim_HappyPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	_, beadID, repoRoot := worktreeTestSetup(t)

	var stderr bytes.Buffer
	res, err := runWorktreeClaim(beadID, "test-purpose", "", "", &stderr)
	if err != nil {
		t.Fatalf("runWorktreeClaim: %v\nstderr: %s", err, stderr.String())
	}
	if res.BeadID != beadID {
		t.Errorf("BeadID = %q, want %q", res.BeadID, beadID)
	}
	if res.BeadStatus != "in_progress" {
		t.Errorf("BeadStatus = %q, want in_progress", res.BeadStatus)
	}
	if res.Assignee != "test-agent" {
		t.Errorf("Assignee = %q, want test-agent", res.Assignee)
	}
	wantPathRaw := filepath.Join(repoRoot, ".worktrees", beadID+"-test-purpose")
	// macOS resolves /tmp/ to /private/tmp/ via symlink; production code
	// runs through `git rev-parse --show-toplevel` which canonicalizes.
	wantPath, _ := filepath.EvalSymlinks(filepath.Dir(wantPathRaw))
	wantPath = filepath.Join(wantPath, filepath.Base(wantPathRaw))
	if res.WorktreePath != wantPath && res.WorktreePath != wantPathRaw {
		t.Errorf("WorktreePath = %q, want %q (or raw %q)", res.WorktreePath, wantPath, wantPathRaw)
	}
	wantBranch := "test-agent/" + beadID + "-test-purpose"
	if res.Branch != wantBranch {
		t.Errorf("Branch = %q, want %q", res.Branch, wantBranch)
	}

	// Verify the worktree actually exists on disk.
	if _, err := os.Stat(res.WorktreePath); err != nil {
		t.Errorf("worktree path %s should exist on disk: %v", res.WorktreePath, err)
	}
	// Verify the branch was created in the parent repo.
	if out, err := exec.Command("git", "-C", repoRoot, "show-ref", "--verify", "refs/heads/"+wantBranch).Output(); err != nil {
		t.Errorf("expected branch %s to exist: %v\nout: %s", wantBranch, err, out)
	}
}

func TestRunWorktreeClaim_AlreadyClaimedSameAlias(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	_, beadID, _ := worktreeTestSetup(t)

	var stderr bytes.Buffer
	if _, err := runWorktreeClaim(beadID, "first", "", "", &stderr); err != nil {
		t.Fatalf("first claim should succeed: %v", err)
	}
	// Second claim should fail because metadata.gc.worktree_path is set.
	_, err := runWorktreeClaim(beadID, "second", "", "", &stderr)
	if err == nil {
		t.Fatal("second claim should fail with already-claimed error")
	}
	if !strings.Contains(err.Error(), "already claims worktree") {
		t.Errorf("expected already-claims-worktree error, got: %v", err)
	}
}

func TestRunWorktreeClaim_AlreadyClaimedDifferentAlias(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	cityPath, beadID, _ := worktreeTestSetup(t)

	// Mark the bead as claimed by someone else, but DO NOT set
	// gc.worktree_path metadata — this exercises the assignee guard.
	store, err := openCityStoreAt(cityPath)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	otherAlias := "other-agent"
	statusInProgress := "in_progress"
	if err := store.Update(beadID, beads.UpdateOpts{
		Status:   &statusInProgress,
		Assignee: &otherAlias,
	}); err != nil {
		t.Fatalf("store.Update (set other claim): %v", err)
	}

	var stderr bytes.Buffer
	_, err = runWorktreeClaim(beadID, "purp", "", "", &stderr)
	if err == nil {
		t.Fatal("claim should fail when bead is in_progress with other assignee")
	}
	if !strings.Contains(err.Error(), "already in_progress") || !strings.Contains(err.Error(), otherAlias) {
		t.Errorf("expected already-in-progress error mentioning %q, got: %v", otherAlias, err)
	}
}

func TestRunWorktreeClaim_RejectsBadPurpose(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	_, beadID, _ := worktreeTestSetup(t)

	var stderr bytes.Buffer
	_, err := runWorktreeClaim(beadID, "Bad_Purpose", "", "", &stderr)
	if err == nil {
		t.Fatal("expected bad-purpose error")
	}
	if !strings.Contains(err.Error(), "kebab-case") {
		t.Errorf("expected kebab-case error, got: %v", err)
	}
}

func TestRunWorktreeClaim_BranchCollisionRefusesAndDoesNotClaim(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	cityPath, beadID, repoRoot := worktreeTestSetup(t)

	// Pre-create the branch that the claim would want.
	wantBranch := "test-agent/" + beadID + "-collision"
	runGitInTest(t, repoRoot, "branch", wantBranch)

	var stderr bytes.Buffer
	_, err := runWorktreeClaim(beadID, "collision", "", "", &stderr)
	if err == nil {
		t.Fatal("expected branch-collision error")
	}
	if !strings.Contains(err.Error(), "already exists locally") {
		t.Errorf("expected already-exists-locally error, got: %v", err)
	}
	// CRITICAL: the bead must NOT have been claimed since we failed
	// preflight before the bd update.
	store, err := openCityStoreAt(cityPath)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	bead, err := store.Get(beadID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if bead.Status == "in_progress" {
		t.Errorf("bead status should still be open (preflight should have failed before update), got %s", bead.Status)
	}
	if path := bead.Metadata[worktreeMetaPath]; path != "" {
		t.Errorf("bead metadata.gc.worktree_path should be empty, got %q", path)
	}
}

func TestRunWorktreeRelease_ClearsClaimAndRemoves(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	cityPath, beadID, _ := worktreeTestSetup(t)

	var stderr bytes.Buffer
	res, err := runWorktreeClaim(beadID, "rel-test", "", "", &stderr)
	if err != nil {
		t.Fatalf("setup claim: %v", err)
	}

	var stdout bytes.Buffer
	if err := runWorktreeRelease(beadID, true, "", &stdout, &stderr); err != nil {
		t.Fatalf("runWorktreeRelease: %v", err)
	}
	// Worktree should be gone.
	if _, err := os.Stat(res.WorktreePath); !os.IsNotExist(err) {
		t.Errorf("worktree path should be removed: stat err = %v", err)
	}
	// Bead metadata should be cleared.
	store, err := openCityStoreAt(cityPath)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	bead, err := store.Get(beadID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if path := bead.Metadata[worktreeMetaPath]; path != "" {
		t.Errorf("metadata.gc.worktree_path should be cleared, got %q", path)
	}
}

func TestRunWorktreeRelease_KeepLeavesWorktreeOnDisk(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	_, beadID, _ := worktreeTestSetup(t)

	var stderr, stdout bytes.Buffer
	res, err := runWorktreeClaim(beadID, "keep-test", "", "", &stderr)
	if err != nil {
		t.Fatalf("setup claim: %v", err)
	}

	if err := runWorktreeRelease(beadID, false, "", &stdout, &stderr); err != nil {
		t.Fatalf("runWorktreeRelease: %v", err)
	}
	// Worktree should still exist on disk.
	if _, err := os.Stat(res.WorktreePath); err != nil {
		t.Errorf("worktree path should still exist with --keep: stat err = %v", err)
	}
}

func TestRunWorktreeList_ShowsClaimedBeads(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	_, beadID, _ := worktreeTestSetup(t)

	var stderr bytes.Buffer
	if _, err := runWorktreeClaim(beadID, "list-test", "", "", &stderr); err != nil {
		t.Fatalf("setup claim: %v", err)
	}

	entries, err := runWorktreeList(&stderr)
	if err != nil {
		t.Fatalf("runWorktreeList: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.BeadID != beadID {
		t.Errorf("entry.BeadID = %q, want %q", e.BeadID, beadID)
	}
	if !e.OnDisk {
		t.Errorf("entry.OnDisk should be true (worktree just created)")
	}
}

func TestRunWorktreeList_DetectsMissingWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	_, beadID, _ := worktreeTestSetup(t)

	var stderr bytes.Buffer
	res, err := runWorktreeClaim(beadID, "drift-test", "", "", &stderr)
	if err != nil {
		t.Fatalf("setup claim: %v", err)
	}
	// Manually rm -rf the worktree dir to simulate drift (e.g. operator
	// deleted it without running gc worktree release).
	if err := os.RemoveAll(res.WorktreePath); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	entries, err := runWorktreeList(&stderr)
	if err != nil {
		t.Fatalf("runWorktreeList: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].OnDisk {
		t.Errorf("entry.OnDisk should be false after rm -rf'ing the worktree")
	}
}

func TestRunWorktreeInspect_DryRunReportsWouldSucceed(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	cityPath, beadID, _ := worktreeTestSetup(t)

	var stdout, stderr bytes.Buffer
	if err := runWorktreeInspect(beadID, "ins-test", "", "", &stdout, &stderr); err != nil {
		t.Fatalf("runWorktreeInspect: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, `"would_succeed": true`) {
		t.Errorf("expected would_succeed=true, got:\n%s", out)
	}

	// Bead must still be open — inspect is read-only.
	store, err := openCityStoreAt(cityPath)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	bead, err := store.Get(beadID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if bead.Status != "open" {
		t.Errorf("inspect should not mutate state; bead.Status = %q, want open", bead.Status)
	}
}