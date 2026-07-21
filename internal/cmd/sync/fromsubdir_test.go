package synccmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/somework/dsx/internal/syncer"
)

// TestASyncVerbRunFromASubdirectoryResolvesTheRoot is the behaviour `git
// status` has from anywhere inside a repository and dsx did not: LoadState
// read exactly the directory it was handed, so standing one level in made
// every verb report the tree unbound.
func TestASyncVerbRunFromASubdirectoryResolvesTheRoot(t *testing.T) {
	root := t.TempDir()
	syncSeedState(t, root, syncer.State{ProjectID: "proj-A"})
	deep := filepath.Join(root, "components", "buttons")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(deep)

	project, dir, err := resolveSyncTarget("pull", nil, boundProject)
	if err != nil {
		t.Fatalf("a subdirectory of a synced tree did not resolve: %v", err)
	}
	if project != "proj-A" {
		t.Errorf("project = %q, want proj-A", project)
	}
	if !sameDir(t, dir, root) {
		t.Errorf("dir = %q, want the root %q — a verb must act on the tree, not the subdirectory", dir, root)
	}
}

// TestANamedRootKeepsTheCallerSpelling: discovery must not rewrite a path the
// caller typed into an absolute one. Every refusal below prints this string.
func TestANamedRootKeepsTheCallerSpelling(t *testing.T) {
	parent := t.TempDir()
	t.Chdir(parent)
	if err := os.Mkdir("design", 0o755); err != nil {
		t.Fatal(err)
	}
	syncSeedState(t, "design", syncer.State{ProjectID: "proj-A"})

	_, dir, err := resolveSyncTarget("pull", []string{"design"}, boundProject)
	if err != nil {
		t.Fatal(err)
	}
	if dir != "design" {
		t.Errorf("dir = %q, want the caller's own %q", dir, "design")
	}
}

// TestTheNearestLedgerWins: an inner binding was chosen by whoever made it;
// reaching past it would sync a tree they never named.
func TestTheNearestLedgerWins(t *testing.T) {
	outer := t.TempDir()
	syncSeedState(t, outer, syncer.State{ProjectID: "proj-OUTER"})
	inner := filepath.Join(outer, "vendor", "kit")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	syncSeedState(t, inner, syncer.State{ProjectID: "proj-INNER"})
	t.Chdir(inner)

	project, dir, err := resolveSyncTarget("push", nil, boundProject)
	if err != nil {
		t.Fatal(err)
	}
	if project != "proj-INNER" {
		t.Errorf("project = %q, want proj-INNER", project)
	}
	if !sameDir(t, dir, inner) {
		t.Errorf("dir = %q, want %q", dir, inner)
	}
}

// The positive control for discovery: a directory with nothing bound above it
// must still produce the ordinary refusal, or the walk would answer for every
// caller everywhere.
func TestNothingBoundAboveStillRefuses(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(deep)

	if _, _, err := resolveSyncTarget("pull", nil, boundProject); err == nil {
		t.Fatal("an unbound tree resolved")
	}
}

func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	fa, err := os.Stat(a)
	if err != nil {
		t.Fatalf("stat %s: %v", a, err)
	}
	fb, err := os.Stat(b)
	if err != nil {
		t.Fatalf("stat %s: %v", b, err)
	}
	return os.SameFile(fa, fb)
}
