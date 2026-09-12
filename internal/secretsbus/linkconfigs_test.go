package secretsbus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// materialized writes a carried config where MaterializeFiles would put it.
func materialized(t *testing.T, home, slug string) string {
	t.Helper()
	dir := filepath.Join(agentcookieRoot(home), slug)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte("carried = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func planFor(t *testing.T, home, slug string) LinkPlanEntry {
	t.Helper()
	plan, err := PlanConfigLinks(home)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	for _, e := range plan {
		if e.Slug == slug {
			return e
		}
	}
	t.Fatalf("no plan entry for %q; plan: %#v", slug, plan)
	return LinkPlanEntry{}
}

func TestPlanConfigLinks_AbsentDestinationIsLinkable(t *testing.T) {
	home := t.TempDir()
	materialized(t, home, "demo-pp-cli")

	e := planFor(t, home, "demo-pp-cli")
	if e.Action != LinkActionLink {
		t.Errorf("absent destination should be linkable, got %q (%s)", e.Action, e.Reason)
	}
}

// The user's own config is never replaced.
func TestPlanConfigLinks_ExistingRegularFileRefused(t *testing.T) {
	home := t.TempDir()
	materialized(t, home, "demo-pp-cli")
	dst := filepath.Join(home, ".config", "demo-pp-cli")
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(dst, "config.toml")
	if err := os.WriteFile(real, []byte("mine = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	e := planFor(t, home, "demo-pp-cli")
	if e.Action != LinkActionRefuse {
		t.Fatalf("existing regular file must be refused, got %q", e.Action)
	}

	// And applying the plan must leave it byte-identical.
	if _, errs := ApplyConfigLinks(home, []LinkPlanEntry{e}); len(errs) == 0 {
		t.Error("applying a refused entry should report an error")
	}
	got, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "mine = true\n" {
		t.Errorf("existing config was modified: %q", got)
	}
}

// A symlink we previously created is ours to re-point.
func TestPlanConfigLinks_OwnedSymlinkIsRelinkable(t *testing.T) {
	home := t.TempDir()
	src := materialized(t, home, "demo-pp-cli")
	dst := filepath.Join(home, ".config", "demo-pp-cli")
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(src, filepath.Join(dst, "config.toml")); err != nil {
		t.Fatal(err)
	}

	e := planFor(t, home, "demo-pp-cli")
	if e.Action != LinkActionAlreadyLinked {
		t.Errorf("a symlink into ~/.agentcookie/ is ours, got %q (%s)", e.Action, e.Reason)
	}
}

// A symlink pointing somewhere else is not ours; never write through it.
func TestPlanConfigLinks_ForeignSymlinkRefused(t *testing.T) {
	home := t.TempDir()
	materialized(t, home, "demo-pp-cli")
	outside := filepath.Join(t.TempDir(), "elsewhere.toml")
	if err := os.WriteFile(outside, []byte("elsewhere = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(home, ".config", "demo-pp-cli")
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dst, "config.toml")); err != nil {
		t.Fatal(err)
	}

	e := planFor(t, home, "demo-pp-cli")
	if e.Action != LinkActionRefuse {
		t.Fatalf("foreign symlink must be refused, got %q", e.Action)
	}
	ApplyConfigLinks(home, []LinkPlanEntry{e})
	// The symlink target must not have been written through.
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "elsewhere = true\n" {
		t.Errorf("wrote through a foreign symlink: %q", got)
	}
}

func TestApplyConfigLinks_CreatesWorkingSymlink(t *testing.T) {
	home := t.TempDir()
	src := materialized(t, home, "demo-pp-cli")

	plan, err := PlanConfigLinks(home)
	if err != nil {
		t.Fatal(err)
	}
	applied, errs := ApplyConfigLinks(home, plan)
	if len(errs) != 0 {
		t.Fatalf("apply: %v", errs)
	}
	if applied != 1 {
		t.Errorf("applied = %d, want 1", applied)
	}
	dst := filepath.Join(home, ".config", "demo-pp-cli", "config.toml")
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("destination not readable: %v", err)
	}
	if string(got) != "carried = true\n" {
		t.Errorf("destination content: %q", got)
	}
	resolved, err := filepath.EvalSymlinks(dst)
	if err != nil {
		t.Fatal(err)
	}
	wantResolved, err := filepath.EvalSymlinks(src)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != wantResolved {
		t.Errorf("resolved to %q, want %q", resolved, wantResolved)
	}
}

// Planning must never mutate the filesystem -- that is what makes dry-run safe.
func TestPlanConfigLinks_IsReadOnly(t *testing.T) {
	home := t.TempDir()
	materialized(t, home, "demo-pp-cli")

	if _, err := PlanConfigLinks(home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".config", "demo-pp-cli", "config.toml")); !os.IsNotExist(err) {
		t.Errorf("planning created the destination; it must be read-only (err=%v)", err)
	}
}

// Only carried config.toml files are link candidates.
func TestPlanConfigLinks_IgnoresNonConfigCarriedFiles(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(agentcookieRoot(home), "demo-pp-cli")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cookies.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanConfigLinks(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 0 {
		t.Errorf("non-config carried files must not be link candidates: %#v", plan)
	}
}

// A directory whose name is not a valid CLI slug is never turned into a path.
func TestPlanConfigLinks_RejectsInvalidSlug(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(agentcookieRoot(home), "Not A Slug")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("x = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanConfigLinks(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range plan {
		if e.Slug == "Not A Slug" {
			t.Errorf("invalid slug must not produce a plan entry: %#v", e)
		}
	}
}

// configDirSymlink points ~/.config/<slug> at target, creating ~/.config as a
// real directory first so only the CLI's own directory is a symlink.
func configDirSymlink(t *testing.T, home, slug, target string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, ".config", slug)); err != nil {
		t.Fatal(err)
	}
}

// An absent destination leaf says nothing about its parents. A symlink at
// ~/.config/<slug> would carry the mkdir and the symlink into whatever tree it
// points at, leaving the config outside ~/.config while the plan claimed
// otherwise, so it is refused rather than followed.
func TestPlanConfigLinks_SymlinkedParentDirectoryRefused(t *testing.T) {
	home := t.TempDir()
	materialized(t, home, "demo-pp-cli")
	outside := t.TempDir()
	configDirSymlink(t, home, "demo-pp-cli", outside)

	e := planFor(t, home, "demo-pp-cli")
	if e.Action != LinkActionRefuse {
		t.Fatalf("a symlinked config directory must be refused, got %q (%s)", e.Action, e.Reason)
	}
	if !strings.Contains(e.Reason, outside) {
		t.Errorf("reason should name where the parent points; got %q", e.Reason)
	}

	applied, errs := ApplyConfigLinks(home, []LinkPlanEntry{e})
	if applied != 0 {
		t.Errorf("applied = %d, want 0", applied)
	}
	if len(errs) == 0 {
		t.Error("applying a refused entry should report an error")
	}
	if _, err := os.Lstat(filepath.Join(outside, "config.toml")); !os.IsNotExist(err) {
		t.Errorf("wrote through the symlinked parent into %s (err=%v)", outside, err)
	}
}

// A relative symlink target is resolved, not pattern-matched: climbing out of
// ~/.config with ../ is the same escape as naming an absolute path.
func TestPlanConfigLinks_ParentSymlinkClimbingOutOfConfigRefused(t *testing.T) {
	home := t.TempDir()
	materialized(t, home, "demo-pp-cli")
	escape := filepath.Join(home, "elsewhere")
	if err := os.MkdirAll(escape, 0o700); err != nil {
		t.Fatal(err)
	}
	configDirSymlink(t, home, "demo-pp-cli", filepath.Join("..", "elsewhere"))

	e := planFor(t, home, "demo-pp-cli")
	if e.Action != LinkActionRefuse {
		t.Fatalf("a parent symlink climbing out of ~/.config must be refused, got %q (%s)", e.Action, e.Reason)
	}
	ApplyConfigLinks(home, []LinkPlanEntry{e})
	if _, err := os.Lstat(filepath.Join(escape, "config.toml")); !os.IsNotExist(err) {
		t.Errorf("wrote into %s (err=%v)", escape, err)
	}
}

// A plan describes a filesystem that may have changed by the time it is
// applied, and the verdict being re-checked here is "absent, safe to create" --
// precisely the one a symlink swapped in afterwards would exploit.
func TestApplyConfigLinks_RefusesParentSymlinkAppearingAfterPlanning(t *testing.T) {
	home := t.TempDir()
	materialized(t, home, "demo-pp-cli")

	plan, err := PlanConfigLinks(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 1 || plan[0].Action != LinkActionLink {
		t.Fatalf("expected one linkable entry before the swap, got %#v", plan)
	}

	outside := t.TempDir()
	configDirSymlink(t, home, "demo-pp-cli", outside)

	applied, errs := ApplyConfigLinks(home, plan)
	if applied != 0 {
		t.Errorf("applied = %d, want 0", applied)
	}
	if len(errs) == 0 {
		t.Error("expected an error once the parent became a symlink")
	}
	if _, err := os.Lstat(filepath.Join(outside, "config.toml")); !os.IsNotExist(err) {
		t.Errorf("wrote into %s after the parent was swapped for a symlink (err=%v)", outside, err)
	}
}

// ~/.config is an ancestor like any other: pointed out of the home directory,
// it no longer describes where a linked config would land.
func TestPlanConfigLinks_ConfigRootSymlinkedOutsideHomeRefused(t *testing.T) {
	home := t.TempDir()
	materialized(t, home, "demo-pp-cli")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(home, ".config")); err != nil {
		t.Fatal(err)
	}

	e := planFor(t, home, "demo-pp-cli")
	if e.Action != LinkActionRefuse {
		t.Fatalf("a config root outside the home directory must be refused, got %q (%s)", e.Action, e.Reason)
	}
	ApplyConfigLinks(home, []LinkPlanEntry{e})
	if _, err := os.Lstat(filepath.Join(outside, "demo-pp-cli")); !os.IsNotExist(err) {
		t.Errorf("created a CLI directory in %s (err=%v)", outside, err)
	}
}

// Pointing the whole config tree at a dotfiles checkout is a normal
// arrangement, and the CLI reads its config through that symlink too, so
// linking follows it while it stays inside the home directory.
func TestApplyConfigLinks_FollowsConfigRootSymlinkedInsideHome(t *testing.T) {
	home := t.TempDir()
	src := materialized(t, home, "demo-pp-cli")
	dotfiles := filepath.Join(home, "dotfiles", "config")
	if err := os.MkdirAll(dotfiles, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dotfiles, filepath.Join(home, ".config")); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanConfigLinks(home)
	if err != nil {
		t.Fatal(err)
	}
	applied, errs := ApplyConfigLinks(home, plan)
	if len(errs) != 0 {
		t.Fatalf("apply: %v", errs)
	}
	if applied != 1 {
		t.Fatalf("applied = %d, want 1", applied)
	}
	got, err := os.ReadFile(filepath.Join(dotfiles, "demo-pp-cli", "config.toml"))
	if err != nil {
		t.Fatalf("link not readable through the resolved config root: %v", err)
	}
	if string(got) != "carried = true\n" {
		t.Errorf("linked content: %q", got)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(home, ".config", "demo-pp-cli", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	wantResolved, err := filepath.EvalSymlinks(src)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != wantResolved {
		t.Errorf("resolved to %q, want %q", resolved, wantResolved)
	}
}

// A plan is a request, not a decision already made: a caller can build one by
// hand, so applying re-checks that the link points at a carried config.
func TestApplyConfigLinks_RefusesSourceOutsideBusRoot(t *testing.T) {
	home := t.TempDir()
	foreign := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(foreign, []byte("theirs = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	applied, errs := ApplyConfigLinks(home, []LinkPlanEntry{{
		Slug:         "demo-pp-cli",
		Materialized: foreign,
		Destination:  filepath.Join(home, ".config", "demo-pp-cli", "config.toml"),
		Action:       LinkActionLink,
	}})
	if applied != 0 {
		t.Errorf("applied = %d, want 0", applied)
	}
	if len(errs) == 0 {
		t.Error("expected an error for a source outside ~/.agentcookie/")
	}
	if _, err := os.Lstat(filepath.Join(home, ".config", "demo-pp-cli", "config.toml")); !os.IsNotExist(err) {
		t.Errorf("linked a source outside ~/.agentcookie/ (err=%v)", err)
	}
}

// The slug composes a write path, so a hand-built plan cannot smuggle
// traversal through it.
func TestApplyConfigLinks_RefusesTraversalSlug(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()

	applied, errs := ApplyConfigLinks(home, []LinkPlanEntry{{
		Slug:         filepath.Join("..", "..", filepath.Base(outside)),
		Materialized: filepath.Join(agentcookieRoot(home), "x", "config.toml"),
		Destination:  filepath.Join(outside, "config.toml"),
		Action:       LinkActionLink,
	}})
	if applied != 0 {
		t.Errorf("applied = %d, want 0", applied)
	}
	if len(errs) == 0 {
		t.Error("expected an error for a traversal slug")
	}
	if _, err := os.Lstat(filepath.Join(outside, "config.toml")); !os.IsNotExist(err) {
		t.Errorf("wrote into %s (err=%v)", outside, err)
	}
}

// The bus's own directories are not CLI config candidates.
func TestPlanConfigLinks_SkipsReservedBusDirectories(t *testing.T) {
	home := t.TempDir()
	for _, reserved := range []string{"secrets", "manifests", "file-optin"} {
		dir := filepath.Join(agentcookieRoot(home), reserved)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("x = 1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	plan, err := PlanConfigLinks(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 0 {
		t.Errorf("reserved bus directories must be skipped: %#v", plan)
	}
}
