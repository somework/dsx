package syncer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// Round four. Round three's own collision guard folded paths with
// strings.ToLower while the filesystem folds by rules Go does not model, and it
// only ever compared remote paths against each other. Both gaps were measured.

func TestPullRefusesARemotePathThatFoldsOntoADifferentLocalName(t *testing.T) {
	// The critical one, measured end-to-end by three independent skeptics.
	//
	// The server renames readme.md -> README.md. planPull sees README.md absent
	// from the scan (present=false, so localDirty=false) and fetches it; the
	// prune loop sees readme.md gone from the listing, tracked, sha unchanged,
	// and deletes it. On APFS both names are one inode: the fetch overwrites it
	// and the delete then removes it. "pulled 1, deleted 1", exit 0, file gone.
	// The next push --prune deletes it from the server too, with a matching
	// if_match. Invariant 4's proof was about path strings; the filesystem's
	// identity function folds them.
	//
	// The equivalence question is answered by asking the filesystem, not by
	// folding in Go: strings.ToLower models neither APFS's case rules nor its
	// Unicode normalisation.
	dir := t.TempDir()
	if !caseInsensitiveDir(dir) {
		t.Skip("case-sensitive volume; the fold cannot happen here")
	}
	mkfile(t, dir, "readme.md", "MINE")

	ig, err := loadIgnore(dir)
	if err != nil {
		t.Fatal(err)
	}
	local, err := scanLocal(dir, ig)
	if err != nil {
		t.Fatal(err)
	}
	remote := map[string]RemoteEntry{
		"README.md": {Path: "README.md", Etag: "e2", Size: 7},
		"other.css": {Path: "other.css", Etag: "e3", Size: 1},
	}

	err = checkPathCollisions(remote, local, dir)
	if err == nil {
		t.Fatal("a remote path that is the same file as a different local name was accepted; " +
			"pulling it overwrites the local file and --prune then deletes it from both sides")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindConflict {
		t.Errorf("classified %q, want %q — which name survives is the user's call", got, dsxerr.KindConflict)
	}
	for _, want := range []string{"README.md", "readme.md"} {
		if !contains(err.Error(), want) {
			t.Errorf("the error does not name %q: %v", want, err)
		}
	}
	if contains(err.Error(), "other.css") {
		t.Errorf("an uninvolved path was named: %v", err)
	}
}

func TestPullAcceptsARemotePathThatMatchesItsLocalNameExactly(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "readme.md", "MINE")
	ig, _ := loadIgnore(dir)
	local, err := scanLocal(dir, ig)
	if err != nil {
		t.Fatal(err)
	}
	remote := map[string]RemoteEntry{"readme.md": {Path: "readme.md", Etag: "e1"}}
	if err := checkPathCollisions(remote, local, dir); err != nil {
		t.Fatalf("an exact match was refused: %v", err)
	}
}

func TestPullAcceptsARemotePathWithNothingOnDiskToFoldOnto(t *testing.T) {
	dir := t.TempDir()
	ig, _ := loadIgnore(dir)
	local, err := scanLocal(dir, ig)
	if err != nil {
		t.Fatal(err)
	}
	remote := map[string]RemoteEntry{
		"README.md": {Path: "README.md", Etag: "e1"},
		"a/b/c.css": {Path: "a/b/c.css", Etag: "e2"},
	}
	if err := checkPathCollisions(remote, local, dir); err != nil {
		t.Fatalf("a fresh directory was refused: %v", err)
	}
}

func TestNestedBuiltInDirectoriesAreStillPrunedWhenAUserRuleExists(t *testing.T) {
	// matchesBuiltinDir's `return false` at the builtins bound exited the whole
	// function rather than the inner loop, so once ANY user rule existed only
	// the first path segment was ever tested against the built-ins. A monorepo's
	// packages/app/node_modules was then walked entry by entry — 121x the work
	// measured — and an unreadable directory inside it aborts the whole sync with
	// "permission denied", on a tree that is built-in-ignored and must never be
	// read. The fix's own comment promised the opposite.
	//
	// The suite passed with and without the bug because no test paired a
	// built-in below the root with a user rule.
	s := mustParseIgnore(t, "dist/\n!dist/keep.css\n")
	for _, p := range []string{
		"node_modules", "packages/app/node_modules", "vendor/lib/.git", "a/b/c/.git",
	} {
		if !s.canSkipDir(p) {
			t.Errorf("canSkipDir(%q) = false; a built-in is never negotiable and must be pruned "+
				"whole, at any depth, whatever the user wrote", p)
		}
	}
	// A user-excluded directory with a live negation still must not be pruned.
	if s.canSkipDir("dist") {
		t.Error("dist was pruned whole despite !dist/keep.css")
	}
}

// Round four's fourth defect -- the union of two sorted lists is not sorted, and
// the sort sat after the dry-run early return, so `status` emitted an unsorted
// list while a real pull emitted a sorted one -- is guarded by
// report_order_test.go, which drives Pull and Push in both modes.
//
// The test that stood here did not guard it. It hand-built a PullReport, called
// the sort on it inside the test body, then asserted the result was sorted: it
// exercised the stdlib and never reached Pull. Measured before removing it:
// reversing every sort in pull.go left all 604 tests green. It was named for a
// defect it could not have caught -- the shape this file exists to record.

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

var _ = os.Stat
var _ = filepath.Join
