package syncer

import (
	"os"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// TestUnpinReleasesADirectoryThatHasSyncedNothing is unpin's whole reason to
// exist: pin refuses to rebind (pin.go's project guard), so without a release
// a mistyped project id is repairable only by deleting .dsx by hand. The
// directory must come back to exactly its pre-pin condition — LoadState's
// missing-file branch — not to a ledger naming no project, which is a shape
// the loader has to reason about separately.
func TestUnpinReleasesADirectoryThatHasSyncedNothing(t *testing.T) {
	dir := t.TempDir()
	if err := Pin(PinOpts{ProjectID: "proj-A", Endpoint: "https://home.example/mcp", Dir: dir}); err != nil {
		t.Fatal(err)
	}

	if err := Unpin(UnpinOpts{Dir: dir}); err != nil {
		t.Fatalf("unpin refused a directory that had synced nothing: %v", err)
	}
	if _, err := os.Lstat(StatePath(dir)); err == nil {
		t.Error("the ledger is still on disk, so the directory is still bound")
	}
	st, err := LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState after unpin: %v", err)
	}
	if st.ProjectID != "" {
		t.Errorf("still bound to %q", st.ProjectID)
	}
}

// TestUnpinRebindsAfterATypo is the end-to-end shape unpin was added for, and
// the one a caller actually types. Without it the test above proves only that
// a file was removed, never that the removal buys back the thing pin refuses.
func TestUnpinRebindsAfterATypo(t *testing.T) {
	dir := t.TempDir()
	const endpoint = "https://home.example/mcp"
	if err := Pin(PinOpts{ProjectID: "proj-TYPO", Endpoint: endpoint, Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if err := Pin(PinOpts{ProjectID: "proj-RIGHT", Endpoint: endpoint, Dir: dir}); err == nil {
		t.Fatal("pin rebound without an unpin; this test guards the wrong thing now")
	}
	if err := Unpin(UnpinOpts{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if err := Pin(PinOpts{ProjectID: "proj-RIGHT", Endpoint: endpoint, Dir: dir}); err != nil {
		t.Fatalf("pin still refused after an unpin: %v", err)
	}
	st, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.ProjectID != "proj-RIGHT" {
		t.Errorf("bound to %q, want proj-RIGHT", st.ProjectID)
	}
}

// TestUnpinRefusesADirectoryThatTracksFiles: invariant 13 says neither bind
// guard's refusal may suggest deleting the ledger, because clearing it makes
// every path untracked and planPush then leaves IfMatch empty under --force —
// a write with no precondition at all. unpin IS that deletion, so it may only
// ever release a binding that has synced nothing. This is the same line pin
// draws, from the other side.
func TestUnpinRefusesADirectoryThatTracksFiles(t *testing.T) {
	dir := t.TempDir()
	seeded := State{ProjectID: "proj-A", Files: map[string]FileState{
		"a.css": {Etag: "e1", Size: 3, SHA: SHA256Hex([]byte("abc"))},
	}}
	if err := seeded.save(dir); err != nil {
		t.Fatal(err)
	}
	before := readBack(t, StatePath(dir))

	err := Unpin(UnpinOpts{Dir: dir})
	if err == nil {
		t.Fatal("unpin released a directory holding a real sync; every tracked etag is gone")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
	if got := readBack(t, StatePath(dir)); got != before {
		t.Error("unpin changed a ledger it refused to release")
	}
	if !strings.Contains(err.Error(), "1 file") {
		t.Errorf("the refusal does not say how much would have been lost: %q", err)
	}
}

// TestUnpinRefusesADirectoryThatWasNeverBound: a no-op that reports success
// would tell a caller who mistyped the DIRECTORY that the unpin worked.
func TestUnpinRefusesADirectoryThatWasNeverBound(t *testing.T) {
	dir := t.TempDir()
	err := Unpin(UnpinOpts{Dir: dir})
	if err == nil {
		t.Fatal("unpin reported success for a directory that was never bound")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
}

// TestUnpinRefusesThroughASymlinkedLedgerHome: checkLedgerHome exists because
// MkdirAll follows a symlinked component silently. unpin REMOVES rather than
// creates, which makes the same symlink worse, not better — it would delete a
// file outside the tree.
func TestUnpinRefusesThroughASymlinkedLedgerHome(t *testing.T) {
	dir := t.TempDir()
	elsewhere := t.TempDir()
	if err := (State{ProjectID: "proj-A", Files: map[string]FileState{}}).save(elsewhere); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(StateDir(elsewhere), StateDir(dir)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := Unpin(UnpinOpts{Dir: dir}); err == nil {
		t.Error("unpin followed a symlinked .dsx and would have deleted a ledger outside the tree")
	}
	if _, err := os.Lstat(StatePath(elsewhere)); err != nil {
		t.Errorf("the ledger outside the tree was removed: %v", err)
	}
}
