package syncer

import (
	"strings"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// TestCheckOutCollisionsRefusesRemotePathsThatCollideOnThisFilesystem: two
// remote paths differing only by case, both candidates for --out (i.e. both
// present-both and differing), land on one inode if --out's filesystem folds
// names — the same hazard checkPathCollisions guards for pull, unguarded on
// diff's only write path. Mirrors TestPullRefusesRemotePathsThatCollideOnThisFilesystem.
func TestCheckOutCollisionsRefusesRemotePathsThatCollideOnThisFilesystem(t *testing.T) {
	out := t.TempDir()
	if !caseInsensitiveDir(out) {
		t.Skip("this filesystem is case-sensitive; the collision cannot happen here")
	}
	remote := map[string]RemoteEntry{
		"Config.json": {Path: "Config.json", Etag: "e1", Size: 1},
		"config.json": {Path: "config.json", Etag: "e2", Size: 1},
		"other.json":  {Path: "other.json", Etag: "e3", Size: 1},
	}
	err := checkOutCollisions([]string{"Config.json", "config.json"}, remote, out)
	if err == nil {
		t.Fatal("two candidate paths that are one file on this filesystem were accepted; " +
			"--out would silently hold only the last writer's bytes")
	}
	for _, want := range []string{"Config.json", "config.json"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "other.json") {
		t.Errorf("an uninvolved path was named: %v", err)
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindConflict {
		t.Errorf("classified %q, want %q — a human must choose which file to keep", got, dsxerr.KindConflict)
	}
}

// TestCheckOutCollisionsAcceptsCleanCandidates is the positive control: two
// candidates that do not fold together must not be refused.
func TestCheckOutCollisionsAcceptsCleanCandidates(t *testing.T) {
	out := t.TempDir()
	remote := map[string]RemoteEntry{
		"a.css": {Path: "a.css", Etag: "e1"},
		"b.css": {Path: "b.css", Etag: "e2"},
	}
	if err := checkOutCollisions([]string{"a.css", "b.css"}, remote, out); err != nil {
		t.Errorf("clean candidates were refused: %v", err)
	}
}
