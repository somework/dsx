package syncer

import (
	"context"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// Push returns (rep, nil); the conflict error is Outcome's. Exit 3 must not
// mean nothing happened.
func TestPushReportsTheWritesThatLandedAlongsideAConflict(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "fresh.css", "new{}")
	mkfile(t, dir, "taken.css", "mine{}")
	// The ledger disagrees with both sides: the local file was edited after the
	// last sync (disk holds "mine{}", the ledger still the old "orig{}" sha),
	// and the server moved too (e1 -> e2). Both-sides-changed (invariant 2).
	syncSeedState(t, dir, State{ProjectID: "p1", Files: map[string]FileState{
		"taken.css": {Etag: "e1", Size: 6, SHA: SHA256Hex([]byte("orig{}"))},
	}})

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			// taken.css moved on the server since the ledger last agreed with it.
			return fakeReply{Text: listingFor(fileEntry("taken.css", "e2", 6))}
		case "write_files":
			return fakeReply{Text: `{"etags":{"fresh.css":"e-fresh"},"written":1}`}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	rep, err := Push(context.Background(), fakeClient(f), PushOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4,
	})
	if err != nil {
		t.Fatalf("Push returned an error directly: %v", err)
	}

	if len(rep.Written) != 1 || rep.Written[0] != "fresh.css" {
		t.Fatalf("Written = %v, want exactly [fresh.css]", rep.Written)
	}
	if rep.Bytes == 0 {
		t.Error("Bytes = 0, want it to count fresh.css's payload")
	}
	if got := f.CountTool("write_files"); got != 1 {
		t.Errorf("write_files called %d times, want 1", got)
	}

	outcomeErr := dsxerr.Classify(rep.Outcome(false))
	if outcomeErr == nil || outcomeErr.Kind != dsxerr.KindConflict {
		t.Fatalf("rep.Outcome(false) = %v, want a KindConflict error naming taken.css", outcomeErr)
	}
	if len(outcomeErr.Paths) != 1 || outcomeErr.Paths[0] != "taken.css" {
		t.Errorf("conflict paths = %v, want exactly [taken.css]", outcomeErr.Paths)
	}

	// The write that landed must be in the ledger too (invariant 5).
	st, err := LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if _, ok := st.Files["fresh.css"]; !ok {
		t.Error("fresh.css missing from the saved ledger despite a successful write")
	}
}
