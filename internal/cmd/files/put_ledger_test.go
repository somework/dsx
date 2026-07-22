package files

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/somework/dsx/internal/clitest"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/syncer"
)

// put has no dir argument, so it cannot address a ledger. This pins the
// resulting shape: put's bytes land on the server without touching
// .dsx-state.json, and if the local copy was also hand-edited, both sync
// directions report a conflict even once the bytes agree everywhere — the
// ledger, not the bytes, is what a plan compares.
func TestPutLeavesTheLedgerBehindSoAnEditedPathConflictsBothWays(t *testing.T) {
	original := []byte("original\n")
	edited := []byte("edited\n")

	dir := t.TempDir()
	mkfile(t, dir, "a.css", string(original))
	clitest.SeedState(t, dir, syncer.State{
		ProjectID: "p1",
		Files: map[string]syncer.FileState{
			"a.css": {Etag: "e1", Size: int64(len(original)), SHA: syncer.SHA256Hex(original)},
		},
	})

	// The user edits the tracked file locally, then reaches for `put` instead
	// of `dsx sync push`. put reads its own [file] argument, not the ledger.
	if err := os.WriteFile(filepath.Join(dir, "a.css"), edited, 0o644); err != nil {
		t.Fatal(err)
	}

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "write_files":
			return fakeReply{Text: `{"etags":{"a.css":"e2"},"written":1}`}
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("a.css", "e2", int64(len(edited))))}
		case "read_file":
			return fakeReply{Text: clitest.EnvelopeFor("a.css", "e2", string(edited))}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})
	client := fakeClient(f)

	if err := cmdPut(t.Context(), client, []string{"p1", "a.css", filepath.Join(dir, "a.css")}); err != nil {
		t.Fatalf("put: %v", err)
	}

	// (a) the ledger is exactly as put left it: untouched.
	st, err := syncer.LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	got, ok := st.Files["a.css"]
	if !ok {
		t.Fatal("a.css dropped from the ledger")
	}
	if got.Etag != "e1" || got.SHA != syncer.SHA256Hex(original) {
		t.Fatalf("ledger entry = %+v, want it unchanged at {e1, sha(original)}", got)
	}

	// (b) push sees the ledger's old etag against the server's new one: conflict.
	// Push itself returns (rep, nil); the conflict error is Outcome()'s.
	pushRep, err := syncer.Push(context.Background(), client, syncer.PushOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4,
	})
	if err != nil {
		t.Fatalf("push after put returned an error directly: %v", err)
	}
	pushErr := dsxerr.Classify(pushRep.Outcome())
	if pushErr == nil || pushErr.Kind != dsxerr.KindConflict {
		t.Fatalf("push.Outcome() after put = %v (report %+v), want a KindConflict error", pushErr, pushRep)
	}
	found := false
	for _, p := range pushErr.Paths {
		if p == "a.css" {
			found = true
		}
	}
	if !found {
		t.Errorf("push conflict paths = %v, want a.css named", pushErr.Paths)
	}

	// (c) pull sees the same disagreement from its side: also a conflict.
	pullRep, err := syncer.Pull(context.Background(), client, syncer.PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4,
	})
	if err != nil {
		t.Fatalf("pull after put returned an error directly: %v", err)
	}
	pullErr := dsxerr.Classify(pullRep.Outcome())
	if pullErr == nil || pullErr.Kind != dsxerr.KindConflict {
		t.Fatalf("pull.Outcome() after put = %v (report %+v), want a KindConflict error", pullErr, pullRep)
	}

	// (d) --force resolves it by writing what is already there: no data lost.
	if _, err := syncer.Pull(context.Background(), client, syncer.PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4, Force: true,
	}); err != nil {
		t.Fatalf("pull --force: %v", err)
	}
	onDisk, err := os.ReadFile(filepath.Join(dir, "a.css"))
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != string(edited) {
		t.Errorf("a.css on disk = %q after --force pull, want it unchanged at %q", onDisk, edited)
	}
}
