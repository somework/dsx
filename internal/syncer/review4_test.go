package syncer

import (
	"strings"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

func TestPullRefusesARemotePathThatFoldsOntoADifferentLocalName(t *testing.T) {
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
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "other.css") {
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
	s := mustParseIgnore(t, "dist/\n!dist/keep.css\n")
	for _, p := range []string{
		"node_modules", "packages/app/node_modules", "vendor/lib/.git", "a/b/c/.git",
	} {
		if !s.canSkipDir(p) {
			t.Errorf("canSkipDir(%q) = false; a built-in is never negotiable and must be pruned "+
				"whole, at any depth, whatever the user wrote", p)
		}
	}

	if s.canSkipDir("dist") {
		t.Error("dist was pruned whole despite !dist/keep.css")
	}
}
