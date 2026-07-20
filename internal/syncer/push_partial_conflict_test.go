package syncer

import (
	"context"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// Push plans once and then executes every non-conflicting write and
// prune-delete unconditionally; the conflict error is computed afterwards,
// from the report, by Outcome — Push itself returns (rep, nil). This test
// pins that shape: exit 3 (via Outcome) must not mean nothing happened. See
// README.md's "For agents" section, immediately after the exit-code table.
func TestPushReportsTheWritesThatLandedAlongsideAConflict(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "fresh.css", "new{}")
	mkfile(t, dir, "taken.css", "mine{}")
	// The ledger disagrees with both sides: the local file was edited after the
	// last sync (disk holds "mine{}", the ledger still the old "orig{}" sha),
	// and the server moved too (e1 -> e2). Both-sides-changed is the canonical
	// conflict (invariant 2) — an etag-only comparison could not see it.
	syncSeedState(t, dir, State{ProjectID: "p1", Files: map[string]FileState{
		"taken.css": {Etag: "e1", Size: 6, SHA: SHA256Hex([]byte("orig{}"))},
	}})

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			// taken.css moved on the server since the ledger last agreed with
			// it (e1 -> e2): planPush must route it to Conflicts, never Write.
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
		t.Fatalf("Push returned an error directly (it should not — Outcome computes that): %v", err)
	}

	if len(rep.Written) != 1 || rep.Written[0] != "fresh.css" {
		t.Fatalf("Written = %v, want exactly [fresh.css] — the non-conflicting write must land", rep.Written)
	}
	if rep.Bytes == 0 {
		t.Error("Bytes = 0, want it to count fresh.css's payload")
	}
	if got := f.CountTool("write_files"); got != 1 {
		t.Errorf("write_files called %d times, want 1 — fresh.css's write must have actually been sent", got)
	}

	outcomeErr := dsxerr.Classify(rep.Outcome(false))
	if outcomeErr == nil || outcomeErr.Kind != dsxerr.KindConflict {
		t.Fatalf("rep.Outcome(false) = %v, want a KindConflict error naming taken.css", outcomeErr)
	}
	if len(outcomeErr.Paths) != 1 || outcomeErr.Paths[0] != "taken.css" {
		t.Errorf("conflict paths = %v, want exactly [taken.css]", outcomeErr.Paths)
	}

	// The write that landed must be in the ledger too, or a rerun would treat
	// fresh.css as a fresh conflict candidate next time (invariant 5).
	st, err := LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if _, ok := st.Files["fresh.css"]; !ok {
		t.Error("fresh.css missing from the saved ledger despite a successful write")
	}
}
