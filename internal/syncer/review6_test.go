package syncer

import (
	"os"
	"path/filepath"
	"testing"
)

// A remote path is untrusted (invariant 7). The fold-collision probe recreates
// remote paths on disk to ask the filesystem which ones collide; it must route
// them through safeJoin so an escaping name cannot plant a file outside the
// sync root. Before the guard, filepath.Join let "../../evil.txt" escape.
func TestRemoteFoldCollisionsNeverWritesOutsideTheSyncRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if !dirFolds(root) {
		t.Skip("filesystem folds nothing, so the probe never runs here")
	}

	remote := map[string]RemoteEntry{"../../evil.txt": {}}
	if err := checkPathCollisions(remote, map[string]localFile{}, root); err != nil {
		t.Fatalf("a lone escaping path yields no collision; unexpected error: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(base, "evil.txt")); err == nil {
		t.Fatal("the probe wrote a file outside the sync root through an escaping remote path")
	}
}
