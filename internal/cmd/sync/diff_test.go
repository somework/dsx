package synccmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/syncer"
)

// TestDiffWritesNothingWithoutOut is the positive-control-paired absence
// assertion the design demands: without --out, cmdDiff must classify every
// path correctly (the positive half) AND leave the working tree byte- and
// mtime-identical with no temp anywhere (the absence half). A stub that
// classifies nothing would fail the positive half; a stub that classifies
// correctly but "helpfully" refreshes the baseline would fail the absence
// half — see syncer.Diff's own doc comment ("fetch writes, diff reads").
func TestDiffWritesNothingWithoutOut(t *testing.T) {
	dir := t.TempDir()
	sameBody := "identical\n"
	localBody := "only here\n"
	remoteBody := "only there\n"
	maincliWriteFile(t, dir, "same.css", sameBody)
	maincliWriteFile(t, dir, "local.css", localBody)

	type snap struct {
		body  []byte
		mtime time.Time
	}
	before := map[string]snap{}
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		b, rErr := os.ReadFile(p)
		if rErr != nil {
			return rErr
		}
		before[p] = snap{body: b, mtime: fi.ModTime()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 2 {
		t.Fatalf("snapshot has %d files, want 2 — the test fixture is wrong", len(before))
	}

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(
				fileEntry("same.css", "e1", int64(len(sameBody))),
				fileEntry("remote.css", "e2", int64(len(remoteBody))),
			)}
		}
		p, _ := args["path"].(string)
		switch p {
		case "same.css":
			return fakeReply{Text: envelopeFor(p, "e1", sameBody)}
		}
		return fakeReply{Text: "unexpected read_file for " + p, IsError: true}
	})

	out, err := captureStdout(t, func() error {
		return cmdDiff(context.Background(), fakeClient(f), []string{"proj-A", dir, "--json"})
	})
	if err != nil {
		t.Fatalf("cmdDiff: %v", err)
	}

	// Positive half: the classification is correct.
	var rep syncer.DiffReport
	if uErr := json.Unmarshal([]byte(out), &rep); uErr != nil {
		t.Fatalf("cmdDiff --json did not print one JSON document: %v\n%s", uErr, out)
	}
	if len(rep.Same) != 1 || rep.Same[0] != "same.css" {
		t.Errorf("Same = %v, want [same.css]", rep.Same)
	}
	if len(rep.LocalOnly) != 1 || rep.LocalOnly[0] != "local.css" {
		t.Errorf("LocalOnly = %v, want [local.css]", rep.LocalOnly)
	}
	if len(rep.RemoteOnly) != 1 || rep.RemoteOnly[0] != "remote.css" {
		t.Errorf("RemoteOnly = %v, want [remote.css]", rep.RemoteOnly)
	}

	// Absence half: nothing on disk moved, and no baseline was written either
	// — diff reads a baseline if one exists, it never creates or refreshes one.
	after := map[string]snap{}
	err = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		b, rErr := os.ReadFile(p)
		if rErr != nil {
			return rErr
		}
		after[p] = snap{body: b, mtime: fi.ModTime()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("file count changed: before %d, after %d (%v)", len(before), len(after), after)
	}
	for p, b := range before {
		a, ok := after[p]
		if !ok {
			t.Errorf("%s disappeared", p)
			continue
		}
		if string(a.body) != string(b.body) {
			t.Errorf("%s content changed", p)
		}
		if !a.mtime.Equal(b.mtime) {
			t.Errorf("%s mtime changed: %v -> %v", p, b.mtime, a.mtime)
		}
	}
	if _, statErr := os.Stat(syncer.StateDir(dir)); !os.IsNotExist(statErr) {
		t.Errorf(".dsx/ was created by a plain diff with no --out: stat err = %v", statErr)
	}
}

// TestDiffOutRefusesANonEmptyDirectoryBeforeTheRoundTrip: --out follows
// clone's rule — refused before any network call, not after.
func TestDiffOutRefusesANonEmptyDirectoryBeforeTheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	maincliWriteFile(t, dir, "a.css", "a\n")

	out := t.TempDir()
	if err := os.WriteFile(filepath.Join(out, "stray.txt"), []byte("already here"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})
	_, err := captureStdout(t, func() error {
		return cmdDiff(context.Background(), fakeClient(f), []string{"proj-A", dir, "--out", out})
	})
	if err == nil {
		t.Fatal("diff --out accepted a non-empty target directory")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
	if got := f.CountTool("list_files"); got != 0 {
		t.Errorf("list_files called %d times, want 0 — --out must be checked before the round trip", got)
	}
	entries, rErr := os.ReadDir(out)
	if rErr != nil {
		t.Fatal(rErr)
	}
	if len(entries) != 1 || entries[0].Name() != "stray.txt" {
		t.Errorf("--out's refused target directory changed: %v", entries)
	}
}

// TestDiffRefusesAMissingDirectory: diff makes a round trip (unlike fetch and
// pin), but a typo'd directory must still be refused before it, for the same
// reason cmdSync's dry runs refuse one — an empty local scan is what makes
// push --prune read the whole server tree as user deletions.
func TestDiffRefusesAMissingDirectory(t *testing.T) {
	parent := t.TempDir()
	missing := filepath.Join(parent, "typo")

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})
	_, err := captureStdout(t, func() error {
		return cmdDiff(context.Background(), fakeClient(f), []string{"proj-A", missing})
	})
	if err == nil {
		t.Fatal("diff accepted a directory that does not exist")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
	if _, statErr := os.Stat(missing); statErr == nil {
		t.Error("diff created the directory it refused — it must leave no trace")
	}
	if got := f.CountTool("list_files"); got != 0 {
		t.Errorf("list_files called %d times, want 0", got)
	}
}

// TestDiffRefusesAForeignEndpointBeforeTheRoundTrip: invariant 13's binding
// is (project, endpoint), checked before any round trip, same as fetch.
func TestDiffRefusesAForeignEndpointBeforeTheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	syncSeedState(t, dir, syncer.State{
		ProjectID: "proj-A",
		Endpoint:  "https://elsewhere.example/mcp",
	})

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})
	_, err := captureStdout(t, func() error {
		return cmdDiff(context.Background(), fakeClient(f), []string{"proj-A", dir})
	})
	if err == nil {
		t.Fatal("diff accepted a directory bound to a different endpoint")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
	if got := f.CountTool("list_files"); got != 0 {
		t.Errorf("list_files called %d times, want 0 — the endpoint guard must run before the round trip", got)
	}
}

// TestDiffWritesTheReportAndSucceeds is the thin cmd-layer wiring check
// mirroring TestFetchWritesTheReportAndSucceeds: the syncer-level behaviour
// is already exhaustively covered in internal/syncer/diff_test.go, this only
// proves cmdDiff actually calls through and prints a report — the seam where
// the pin survivors lived (cmd-layer wiring untested while the units below it
// were well covered).
func TestDiffWritesTheReportAndSucceeds(t *testing.T) {
	dir := t.TempDir()
	body := "hello\n"
	maincliWriteFile(t, dir, "a.css", body)

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(fileEntry("a.css", "e1", int64(len(body))))}
		}
		p, _ := args["path"].(string)
		return fakeReply{Text: envelopeFor(p, "e1", body)}
	})
	out, err := captureStdout(t, func() error {
		return cmdDiff(context.Background(), fakeClient(f), []string{"proj-A", dir})
	})
	if err != nil {
		t.Fatalf("cmdDiff errored: %v", err)
	}
	if out == "" {
		t.Error("cmdDiff printed nothing")
	}
}

// TestDiffOutMaterialisesTheServerSideOfADifferingPathThroughTheCLI proves
// cmdDiff's --out wiring end to end, not just syncer.Diff's own unit test.
func TestDiffOutMaterialisesTheServerSideOfADifferingPathThroughTheCLI(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(t.TempDir(), "out")
	localBody := "mine\n"
	remoteBody := "theirs and different\n"
	maincliWriteFile(t, dir, "a.css", localBody)

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(fileEntry("a.css", "e1", int64(len(remoteBody))))}
		}
		p, _ := args["path"].(string)
		return fakeReply{Text: envelopeFor(p, "e1", remoteBody)}
	})
	if _, err := captureStdout(t, func() error {
		return cmdDiff(context.Background(), fakeClient(f), []string{"proj-A", dir, "--out", out})
	}); err != nil {
		t.Fatalf("cmdDiff: %v", err)
	}

	got, rErr := os.ReadFile(filepath.Join(out, "a.css"))
	if rErr != nil {
		t.Fatalf("out/a.css was not written: %v", rErr)
	}
	if string(got) != remoteBody {
		t.Errorf("out/a.css = %q, want the remote bytes %q", got, remoteBody)
	}
}
