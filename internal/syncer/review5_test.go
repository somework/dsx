package syncer

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// Finding #6: the remote×remote fold detector used strings.ToLower, which keeps
// NFC ("café", é composed) and NFD ("café", e+combining acute) as distinct byte
// strings even though APFS folds them onto one inode. Two such server paths would
// land in one file on a pull, destroying all but the last. Stdlib has no NFC/NFD
// normaliser, so the guard asks the filesystem (os.SameFile) instead of folding
// names in Go.
//
// This test's filesystem: on macOS APFS (the dev target) names are stored
// normalization-insensitively, so the NFC and NFD spellings resolve to one file;
// the test skips on filesystems that keep them distinct (e.g. ext4 on CI).
func TestCheckPathCollisionsCatchesUnicodeNormalizationFolds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink/normalization semantics differ on windows")
	}
	dir := t.TempDir()
	const nfc = "café.css"  // é as one code point
	const nfd = "café.css" // e + combining acute accent

	// Does this filesystem fold NFC and NFD together?
	probe := filepath.Join(dir, nfc)
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	_, sameErr := os.Stat(filepath.Join(dir, nfd))
	_ = os.Remove(probe)
	if sameErr != nil {
		t.Skip("this filesystem keeps NFC and NFD distinct; the fold cannot happen here")
	}

	remote := map[string]RemoteEntry{
		nfc:         {Path: nfc, Etag: "e1", Size: 1},
		nfd:         {Path: nfd, Etag: "e2", Size: 1},
		"other.css": {Path: "other.css", Etag: "e3", Size: 1},
	}
	err = checkPathCollisions(remote, map[string]localFile{}, dir)
	if err == nil {
		t.Fatal("two server paths that are one file on this filesystem (NFC vs NFD) were accepted; " +
			"a pull writes both onto one inode and destroys all but the last")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindConflict {
		t.Errorf("classified %q, want %q — a human must choose which spelling survives", got, dsxerr.KindConflict)
	}
	for _, want := range []string{nfc, nfd} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "other.css") {
		t.Errorf("an uninvolved path was named: %v", err)
	}
}

// Finding #1: filepath.WalkDir does not descend a symlinked root — Lstat sees a
// symlink, not a directory, so the walk stops after one call and scanLocal
// returns an empty map. An empty scan makes pull overwrite every real file
// through the link with no conflict, and push --prune delete the whole tracked
// tree. scanLocal must resolve a symlinked root and scan its contents.
func TestScanLocalDescendsASymlinkedRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	mkfile(t, real, "a.css", "body{}")
	mkfile(t, real, "tokens/colors.css", ":root{}")

	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	ig, err := loadIgnore(link)
	if err != nil {
		t.Fatal(err)
	}
	got, err := scanLocal(link, ig)
	if err != nil {
		t.Fatalf("scanLocal: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("scanned %d files (%v) through a symlinked root, want 2 — an empty scan is a data-loss trigger",
			len(got), SortedPaths(got))
	}
	if got["a.css"].SHA != SHA256Hex([]byte("body{}")) {
		t.Error("file scanned through the symlinked root has the wrong sha")
	}

	// With a non-empty scan, push --prune over the same tree must NOT schedule
	// the whole tracked tree for deletion.
	remote := remoteOf(
		RemoteEntry{Path: "a.css", Etag: "e1"},
		RemoteEntry{Path: "tokens/colors.css", Etag: "e2"},
	)
	st := stateOf(map[string]FileState{
		"a.css":             {Etag: "e1", SHA: got["a.css"].SHA},
		"tokens/colors.css": {Etag: "e2", SHA: got["tokens/colors.css"].SHA},
	})
	d := planPush(remote, got, st, nil, false, true)
	if len(d.Delete) != 0 {
		t.Fatalf("planPush(prune) scheduled %v for deletion over a symlinked root — the whole tree would be destroyed", d.Delete)
	}
}

// Finding #3: the machine-facing conflict envelope must carry a correct hint per
// conflict class. For a binary conflict "pull first" is impossible (read_file
// cannot fetch it), so the envelope must never recommend it; and a pull
// prune-conflict under --force DELETES the only copy, which the envelope must say.
func TestPushOutcomeEnvelopeNeverTellsAnAgentToPullABinary(t *testing.T) {
	rep := PushReport{
		Conflicts:       []string{"assets/hero.png"},
		BinaryConflicts: []string{"assets/hero.png"},
	}
	err := rep.Outcome(false)
	if err == nil {
		t.Fatal("a binary conflict produced no error; an agent would carry on over it")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindConflict {
		t.Errorf("classified %q, want conflict", got)
	}
	msg := err.Error()
	if strings.Contains(msg, "`dsx pull`") {
		t.Errorf("the machine hint recommends `dsx pull`, which provably cannot fetch a binary: %q", msg)
	}
	if !strings.Contains(msg, "--force") || !strings.Contains(strings.ToLower(msg), "only copy") {
		t.Errorf("the hint does not say --force overwrites the only copy: %q", msg)
	}
}

func TestPushOutcomeMixedConflictKeepsPullForTheTextOnes(t *testing.T) {
	rep := PushReport{
		Conflicts:       []string{"assets/hero.png", "styles.css"},
		BinaryConflicts: []string{"assets/hero.png"},
	}
	msg := rep.Outcome(false).Error()
	if !strings.Contains(msg, "pull") {
		t.Errorf("a text conflict is present but the hint drops the pull advice: %q", msg)
	}
	if !strings.Contains(msg, "assets/hero.png") {
		t.Errorf("the binary path is not named distinctly in the machine hint: %q", msg)
	}
}

func TestPullOutcomeEnvelopeSaysForceDeletesAPruneConflict(t *testing.T) {
	rep := PullReport{
		Conflicts:      []string{"gone.css"},
		PruneConflicts: []string{"gone.css"},
	}
	err := rep.Outcome(false)
	if err == nil {
		t.Fatal("a prune conflict produced no error")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindConflict {
		t.Errorf("classified %q, want conflict", got)
	}
	msg := err.Error()
	if !strings.Contains(msg, "--force") || !strings.Contains(strings.ToLower(msg), "only copy") {
		t.Errorf("the machine hint does not warn that --force DELETES the only copy: %q", msg)
	}
	if !strings.Contains(strings.ToUpper(msg), "DELETE") {
		t.Errorf("the machine hint does not use DELETE for the prune-conflict force semantics: %q", msg)
	}
}

// Finding #5: safeJoin refuses a symlinked leaf but let a write pass through an
// in-tree symlinked DIRECTORY component (root/subdir -> root/otherdir). The bytes
// land in the resolved target while the ledger keys off the symlink path, and
// scanLocal then finds the same bytes under both names — a perpetual "changed"
// churn. safeJoin must refuse an intermediate symlinked directory.
func TestSafeJoinRefusesWritingThroughAnInTreeSymlinkedDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "otherdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "otherdir"), filepath.Join(root, "subdir")); err != nil {
		t.Fatal(err)
	}

	if _, err := safeJoin(root, "subdir/file.css"); err == nil {
		t.Error("safeJoin allowed a write through an in-tree symlinked directory; " +
			"the same bytes then appear under two names and churn forever")
	}

	// A real (non-symlinked) nested directory must still be accepted.
	if err := os.MkdirAll(filepath.Join(root, "realdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := safeJoin(root, "realdir/file.css"); err != nil {
		t.Errorf("safeJoin refused an ordinary nested path: %v", err)
	}
}

// Finding #2b: when the server refuses a prune delete (it moved the path ahead
// between the listing and the delete), deletePaths must surface a dsxerr.Conflict
// like writeBatch does — not the raw JSON-in-text ToolError.
func TestDeletePathsClassifiesAServerConflict(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "finalize_plan":
			return fakeReply{Text: `{"plan_token":"tok"}`}
		case "delete_files":
			return fakeReply{
				IsError: true,
				Text:    `{"conflicts":[{"path":"gone.css","etag":"e9"}],"message":"moved ahead"}`,
			}
		}
		return fakeReply{Text: "[]"}
	})

	st := State{Files: map[string]FileState{"gone.css": {Etag: "e1"}}}
	err := deletePaths(context.Background(), fakeClient(f), "p1", []string{"gone.css"}, st)
	if err == nil {
		t.Fatal("a server refusal was reported as success")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindConflict {
		t.Errorf("classified %q, want %q — a raw ToolError leaks JSON and no errKind to branch on", got, dsxerr.KindConflict)
	}
}

// Finding #2a: push --prune must not delete a remote path that moved on the
// server after the last sync (etag differs from the ledger) — deleting it with a
// stale if_match would drop a copy the user never saw. Mirror planPull's SHA
// guard: it becomes a conflict, not a delete, unless --force.
func TestPlanPushPruneTreatsARemoteThatMovedAheadAsAConflict(t *testing.T) {
	d := planPush(
		remoteOf(RemoteEntry{Path: "gone.css", Etag: "e2"}),
		localOf(),
		stateOf(map[string]FileState{"gone.css": {Etag: "e1", SHA: "s"}}),
		nil, false, true)

	if len(d.Delete) != 0 {
		t.Errorf("delete=%v, want none — the server moved ahead, deleting it drops an unseen change", d.Delete)
	}
	if !slices.Equal(d.PruneConflicts, []string{"gone.css"}) {
		t.Errorf("pruneConflicts=%v, want [gone.css]", d.PruneConflicts)
	}
}

func TestPlanPushPruneForceStillDeletesAMovedRemote(t *testing.T) {
	d := planPush(
		remoteOf(RemoteEntry{Path: "gone.css", Etag: "e2"}),
		localOf(),
		stateOf(map[string]FileState{"gone.css": {Etag: "e1", SHA: "s"}}),
		nil, true, true)

	if !slices.Equal(d.Delete, []string{"gone.css"}) {
		t.Errorf("delete=%v, want [gone.css] under --force", d.Delete)
	}
	if len(d.PruneConflicts) != 0 {
		t.Errorf("pruneConflicts=%v, want none under --force", d.PruneConflicts)
	}
}

// Finding #4: a symlink (Irregular) that exists ONLY locally must still be
// reported, so the user sees dsx skipped it. Before the fix planPush dropped it
// from every report field when !onServer.
func TestPlanPushRecordsALocalOnlyIrregular(t *testing.T) {
	d := planPush(
		remoteOf(),
		localOf(localFile{Path: "link", Irregular: true}),
		stateOf(nil), nil, false, false)

	if !slices.Equal(d.Irregular, []string{"link"}) {
		t.Errorf("irregular=%v, want [link] — a local-only symlink was silently dropped", d.Irregular)
	}
	if len(d.Write) != 0 {
		t.Errorf("write=%v, want none — an irregular file is never pushable", d.Write)
	}
}
