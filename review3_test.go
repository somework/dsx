package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// Residuals the round-two audits reported but did not themselves reach. Each is
// a case the fix it belongs to still misses.

func TestPushSaysSomethingTrueAndActionableAboutABinaryConflict(t *testing.T) {
	// Every push conflict printed "server moved ahead; `dsx pull` first, or
	// --force". For a binary path whose etag has NOT moved, all of that is
	// false: the server did not move, and `dsx pull` cannot resolve it —
	// planPull classifies the path Binary, fetches nothing and leaves the ledger
	// untouched, so push conflicts identically forever. A measured livelock
	// whose only exit is the one unrecoverable command, reachable by guessing.
	// Conflicts is the union; BinaryConflicts discriminates. runPush builds it
	// that way so `conflicts` keeps meaning "everything a human must look at".
	rep := pushReport{
		Conflicts:       []string{"assets/hero.png"},
		BinaryConflicts: []string{"assets/hero.png"},
	}
	out := rep.render(false)
	if !strings.Contains(out, "assets/hero.png") {
		t.Fatalf("the path is not named: %q", out)
	}
	if strings.Contains(out, "server moved ahead") {
		t.Errorf("the message claims the server moved; for a binary conflict it has not: %q", out)
	}
	if strings.Contains(out, "`dsx pull` first") {
		t.Errorf("the message recommends a pull that provably cannot resolve this: %q", out)
	}
	if !strings.Contains(out, "--force") {
		t.Errorf("the only command that resolves it is not mentioned: %q", out)
	}
	// And it must say what --force costs, because here it is unrecoverable.
	if !strings.Contains(strings.ToLower(out), "cannot") && !strings.Contains(out, "only copy") {
		t.Errorf("the message does not say the server's copy is the only one: %q", out)
	}
}

func TestBinaryConflictsStillReachTheExitCode(t *testing.T) {
	remote := map[string]remoteEntry{"a.png": {Path: "a.png", Etag: "e1"}}
	local := map[string]localFile{"a.png": {Path: "a.png", SHA: "new"}}
	st := syncState{Files: map[string]fileState{"a.png": {Etag: "e1", Binary: true}}}

	d := planPush(remote, local, st, false, false)
	if len(d.Write) != 0 {
		t.Fatalf("the write was not refused: %+v", d.Write)
	}
	// It is still a conflict — a human must choose — just one with honest advice.
	all := append(append([]string(nil), d.Conflicts...), d.BinaryConflicts...)
	if err := conflictOutcome(all, false, "x"); err == nil {
		t.Error("a binary conflict no longer exits 3; an agent would carry on over it")
	}
}

func TestPullReportsAPruneFailureRatherThanTheLedgerSaveThatFollowedIt(t *testing.T) {
	// The prune error was recorded, then st.save ran, and its error was returned
	// first — so a save failure from any cause hid the prune failure entirely.
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission semantics differ")
	}
	dir := t.TempDir()
	mkfile(t, dir, "locked/old.css", "old")
	syncSeedState(t, dir, syncState{
		ProjectID: "p1",
		Files:     map[string]fileState{"locked/old.css": {Etag: "e0", Size: 3, SHA: sha256hex([]byte("old"))}},
	})
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor()}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})
	locked := filepath.Join(dir, "locked")
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	_, err := runPull(t.Context(), fakeClient(f), pullOpts{projectID: "p1", dir: dir, concurrency: 1, prune: true})
	if err == nil {
		t.Fatal("a failed prune delete was reported as success")
	}
	if !strings.Contains(err.Error(), "locked/old.css") && !strings.Contains(err.Error(), "permission") {
		t.Errorf("the reported error does not describe the prune failure: %v", err)
	}
}

func TestPullRefusesRemotePathsThatCollideOnThisFilesystem(t *testing.T) {
	// The server is case-sensitive and holds both Button.css and button.css.
	// macOS's default APFS is not, so both land in one inode: dsx reports
	// "fetched 2", the disk holds one file, and one file's bytes are gone with
	// no warning. Invariant 1's per-file size check passes on each write
	// individually, so it never fires. The next push --prune then deletes one
	// server file and overwrites the other with the wrong bytes — invariant 4's
	// stated harm, reached through a collision rather than a deletion.
	dir := t.TempDir()
	if !caseInsensitiveDir(dir) {
		t.Skip("this filesystem is case-sensitive; the collision cannot happen here")
	}
	remote := map[string]remoteEntry{
		"Button.css": {Path: "Button.css", Etag: "e1", Size: 1},
		"button.css": {Path: "button.css", Etag: "e2", Size: 1},
		"other.css":  {Path: "other.css", Etag: "e3", Size: 1},
	}
	err := checkPathCollisions(remote, map[string]localFile{}, dir)
	if err == nil {
		t.Fatal("two paths that are one file on this filesystem were accepted; " +
			"one of them would be silently destroyed")
	}
	for _, want := range []string{"Button.css", "button.css"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "other.css") {
		t.Errorf("an uninvolved path was named: %v", err)
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindConflict {
		t.Errorf("classified %q, want %q: a human must choose which file to keep", got, dsxerr.KindConflict)
	}

	// No collision, no complaint.
	if err := checkPathCollisions(map[string]remoteEntry{
		"a.css": {Path: "a.css"}, "b.css": {Path: "b.css"},
	}, map[string]localFile{}, dir); err != nil {
		t.Errorf("a clean listing was refused: %v", err)
	}
}

func TestCaseProbeLeavesNothingBehindAndIsNeverSynced(t *testing.T) {
	// The probe writes into the directory dsx is about to sync. A run killed
	// between the create and the remove would leave the file there, and the next
	// push would upload it — so the name is fixed rather than random, and it is
	// excluded like the ledger.
	dir := t.TempDir()
	_ = caseInsensitiveDir(dir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("the probe left %q behind", e.Name())
	}

	// And if a killed run did leave one, it is still never part of a sync.
	if err := os.WriteFile(filepath.Join(dir, caseProbeName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ig, err := loadIgnore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !ig.match(caseProbeName) {
		t.Error("a leftover probe is not excluded from the sync; push would upload it")
	}
	local, err := scanLocal(dir, ig)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := local[caseProbeName]; ok {
		t.Error("scanLocal picked the probe up")
	}
	if err := checkRemotePath(caseProbeName); err == nil {
		t.Error("a remote path named like the probe is accepted; pulling it would be pointless at best")
	}
}
