package syncer

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

func TestPushSaysSomethingTrueAndActionableAboutABinaryConflict(t *testing.T) {
	rep := PushReport{
		Conflicts:       []string{"assets/hero.png"},
		BinaryConflicts: []string{"assets/hero.png"},
	}
	out := rep.Render(false)
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

	if !strings.Contains(strings.ToLower(out), "cannot") && !strings.Contains(out, "only copy") {
		t.Errorf("the message does not say the server's copy is the only one: %q", out)
	}
}

func TestBinaryConflictsStillReachTheExitCode(t *testing.T) {
	remote := map[string]RemoteEntry{"a.png": {Path: "a.png", Etag: "e1"}}
	local := map[string]localFile{"a.png": {Path: "a.png", SHA: "new"}}
	st := State{Files: map[string]FileState{"a.png": {Etag: "e1", Binary: true}}}

	d := planPush(remote, local, st, false, false)
	if len(d.Write) != 0 {
		t.Fatalf("the write was not refused: %+v", d.Write)
	}

	all := append(append([]string(nil), d.Conflicts...), d.BinaryConflicts...)
	if err := ConflictOutcome(all, false, "x"); err == nil {
		t.Error("a binary conflict no longer exits 3; an agent would carry on over it")
	}
}

func TestPullReportsAPruneFailureRatherThanTheLedgerSaveThatFollowedIt(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission semantics differ")
	}
	dir := t.TempDir()
	mkfile(t, dir, "locked/old.css", "old")
	syncSeedState(t, dir, State{
		ProjectID: "p1",
		Files:     map[string]FileState{"locked/old.css": {Etag: "e0", Size: 3, SHA: SHA256Hex([]byte("old"))}},
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

	_, err := Pull(t.Context(), fakeClient(f), PullOpts{ProjectID: "p1", Dir: dir, Concurrency: 1, Prune: true})
	if err == nil {
		t.Fatal("a failed prune delete was reported as success")
	}
	if !strings.Contains(err.Error(), "locked/old.css") && !strings.Contains(err.Error(), "permission") {
		t.Errorf("the reported error does not describe the prune failure: %v", err)
	}
}

func TestPullRefusesRemotePathsThatCollideOnThisFilesystem(t *testing.T) {
	dir := t.TempDir()
	if !caseInsensitiveDir(dir) {
		t.Skip("this filesystem is case-sensitive; the collision cannot happen here")
	}
	remote := map[string]RemoteEntry{
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

	if err := checkPathCollisions(map[string]RemoteEntry{
		"a.css": {Path: "a.css"}, "b.css": {Path: "b.css"},
	}, map[string]localFile{}, dir); err != nil {
		t.Errorf("a clean listing was refused: %v", err)
	}
}

func TestCaseProbeLeavesNothingBehindAndIsNeverSynced(t *testing.T) {
	dir := t.TempDir()
	_ = caseInsensitiveDir(dir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("the probe left %q behind", e.Name())
	}

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
