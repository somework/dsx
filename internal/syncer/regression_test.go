package syncer

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestPlanPullBothSidesChangedIsAConflict(t *testing.T) {
	d := planPull(
		remoteOf(RemoteEntry{Path: "a.css", Etag: "2"}),
		localOf(localFile{Path: "a.css", SHA: "edited"}),
		stateOf(map[string]FileState{"a.css": {Etag: "1", SHA: "sha1"}}),
		nil, false, false)

	if len(d.Fetch) != 0 {
		t.Errorf("fetch=%v, want none — fetching here overwrites the local edit", d.Fetch)
	}
	if !slices.Equal(d.Conflicts, []string{"a.css"}) {
		t.Errorf("conflicts=%v, want [a.css]", d.Conflicts)
	}
}

func TestPlanPullBothSidesChangedFetchesUnderForce(t *testing.T) {
	d := planPull(
		remoteOf(RemoteEntry{Path: "a.css", Etag: "2"}),
		localOf(localFile{Path: "a.css", SHA: "edited"}),
		stateOf(map[string]FileState{"a.css": {Etag: "1", SHA: "sha1"}}),
		nil, true, false)

	if !slices.Equal(d.Fetch, []string{"a.css"}) {
		t.Errorf("fetch=%v, want [a.css] under --force", d.Fetch)
	}
}

func TestPlanPullRemoteOnlyChangeStillFetches(t *testing.T) {
	d := planPull(
		remoteOf(RemoteEntry{Path: "a.css", Etag: "2"}),
		localOf(localFile{Path: "a.css", SHA: "sha1"}),
		stateOf(map[string]FileState{"a.css": {Etag: "1", SHA: "sha1"}}),
		nil, false, false)

	if !slices.Equal(d.Fetch, []string{"a.css"}) {
		t.Errorf("fetch=%v, want [a.css] — untouched locally, safe to update", d.Fetch)
	}
	if len(d.Conflicts) != 0 {
		t.Errorf("conflicts=%v, want none", d.Conflicts)
	}
}

func TestPlanPullPruneKeepsLocallyEditedFile(t *testing.T) {
	d := planPull(
		remoteOf(),
		localOf(localFile{Path: "gone.css", SHA: "edited"}),
		stateOf(map[string]FileState{"gone.css": {Etag: "1", SHA: "sha1"}}),
		nil, false, true)

	if len(d.Delete) != 0 {
		t.Errorf("delete=%v, want none — the local edit is the only copy left", d.Delete)
	}

	if !slices.Equal(d.PruneConflicts, []string{"gone.css"}) {
		t.Errorf("pruneConflicts=%v, want [gone.css]", d.PruneConflicts)
	}
	if len(d.Conflicts) != 0 {
		t.Errorf("conflicts=%v — this is a delete-conflict, not an overwrite-conflict", d.Conflicts)
	}
}

func TestPlanPushPruneKeepsLocallyEditedFile(t *testing.T) {
	d := planPush(
		remoteOf(RemoteEntry{Path: "a.css", Etag: "1"}),
		localOf(),
		stateOf(map[string]FileState{"a.css": {Etag: "1", SHA: "sha1"}}),
		nil, false, true)

	if !slices.Equal(d.Delete, []string{"a.css"}) {
		t.Errorf("delete=%v, want [a.css]", d.Delete)
	}
}

func TestSafeJoinRefusesSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := safeJoin(root, "link/evil.txt"); err == nil {
		t.Error("safeJoin allowed a write through a symlink pointing outside the root")
	}
}

func TestSafeJoinRefusesSymlinkedLeaf(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(target, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "innocent.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := safeJoin(root, "innocent.txt"); err == nil {
		t.Error("safeJoin allowed a write through a symlinked leaf")
	}
}

func TestRemotePathsCannotTouchVCSOrLedger(t *testing.T) {
	for _, p := range []string{
		".git/config",
		".git/hooks/pre-commit",
		oldStateFileName,
		"node_modules/x/index.js",
		"a/.git/config",
	} {
		t.Run(p, func(t *testing.T) {
			if err := checkRemotePath(p); err == nil {
				t.Errorf("checkRemotePath(%q) = nil, want refusal", p)
			}
		})
	}
}

func TestRemotePathsAllowNormalFiles(t *testing.T) {
	for _, p := range []string{"a.css", "tokens/colors.css", "components/atoms/Button.jsx", ".thumbnail"} {
		if err := checkRemotePath(p); err != nil {
			t.Errorf("checkRemotePath(%q) = %v, want nil", p, err)
		}
	}
}

func TestBareDsxSegmentIsRefusedAsARemotePath(t *testing.T) {
	for _, p := range []string{".dsx", "a/.dsx", ".dsx/state.json", "a/.dsx/state.json", ".DSX/x"} {
		t.Run(p, func(t *testing.T) {
			if err := checkRemotePath(p); err == nil {
				t.Errorf("checkRemotePath(%q) = nil, want refusal", p)
			}
		})
	}
}
