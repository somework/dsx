package syncer

import (
	"context"
	"slices"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// A field naming an ACT ("these were deleted", "these bytes moved") is
// assigned when the act completes, never when it is only planned (invariant 12).

// pruneFixture stages one new local file and one tracked server-only file, so
// planPush yields exactly one Write and one prune Delete.
func pruneFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mkfile(t, dir, "fresh.css", "new{}")
	syncSeedState(t, dir, State{ProjectID: "p1", Files: map[string]FileState{
		"gone.css": {Etag: "e1", Size: 3, SHA: SHA256Hex([]byte("old"))},
	}})
	return dir
}

func pruneListing() string {
	return listingFor(fileEntry("gone.css", "e1", 3))
}

// A write failure returns before the deletes are ever attempted.
func TestPushDeletedIsEmptyWhenTheWriteFailedBeforeTheDeletes(t *testing.T) {
	dir := pruneFixture(t)
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: pruneListing()}
		case "write_files":
			return fakeReply{Text: `{"conflicts":[{"path":"fresh.css","etag":"e9"}]}`, IsError: true}
		}
		return fakeReply{Text: `{"plan_token":"tok"}`}
	})

	rep, err := Push(context.Background(), fakeClient(f), PushOpts{
		ProjectID: "p1", Dir: dir, Prune: true,
	})
	if err == nil {
		t.Fatal("Push returned nil error, want the write conflict")
	}
	if got := f.CountTool("delete_files"); got != 0 {
		t.Fatalf("delete_files called %d times, want 0", got)
	}
	if len(rep.Deleted) != 0 {
		t.Errorf("Deleted = %v, want empty — no delete was attempted", rep.Deleted)
	}
	if rep.Bytes != 0 {
		t.Errorf("Bytes = %d, want 0 — nothing was written", rep.Bytes)
	}
}

// The delete call itself failing is the same falsification, one step later.
func TestPushDeletedIsEmptyWhenTheDeleteCallFailed(t *testing.T) {
	dir := pruneFixture(t)
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: pruneListing()}
		case "write_files":
			return fakeReply{Text: `{"etags":{"fresh.css":"e-fresh"},"written":1}`}
		case "finalize_plan":
			return fakeReply{Text: `{"plan_token":"tok"}`}
		case "delete_files":
			return fakeReply{Text: `{"conflicts":[{"path":"gone.css","etag":"e2"}]}`, IsError: true}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	rep, err := Push(context.Background(), fakeClient(f), PushOpts{
		ProjectID: "p1", Dir: dir, Prune: true,
	})
	if err == nil {
		t.Fatal("Push returned nil error, want the delete conflict")
	}
	if len(rep.Deleted) != 0 {
		t.Errorf("Deleted = %v, want empty — the delete was refused", rep.Deleted)
	}
	// The inverse face: a run that failed for no reason but a conflict must not
	// emit "conflicts":null.
	if !slices.Contains(rep.Conflicts, "gone.css") {
		t.Errorf("Conflicts = %v, want it to carry gone.css", rep.Conflicts)
	}
}

// The successful run must still report what it did, and the ledger entry must go.
func TestPushDeletedIsReportedWhenTheDeleteSucceeded(t *testing.T) {
	dir := pruneFixture(t)
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: pruneListing()}
		case "write_files":
			return fakeReply{Text: `{"etags":{"fresh.css":"e-fresh"},"written":1}`}
		case "finalize_plan":
			return fakeReply{Text: `{"plan_token":"tok"}`}
		case "delete_files":
			return fakeReply{Text: `{"deleted":1}`}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	rep, err := Push(context.Background(), fakeClient(f), PushOpts{
		ProjectID: "p1", Dir: dir, Prune: true,
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if len(rep.Deleted) != 1 || rep.Deleted[0] != "gone.css" {
		t.Errorf("Deleted = %v, want [gone.css]", rep.Deleted)
	}
	if rep.Bytes != int64(len("new{}")) {
		t.Errorf("Bytes = %d, want %d", rep.Bytes, len("new{}"))
	}
	st, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Files["gone.css"]; ok {
		t.Error("gone.css still in the ledger after a successful prune")
	}
}

// DryRun: the plan is the requested outcome (invariant 12).
func TestPushDryRunStillPreviewsTheDeletesAndBytes(t *testing.T) {
	dir := pruneFixture(t)
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: pruneListing()}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	rep, err := Push(context.Background(), fakeClient(f), PushOpts{
		ProjectID: "p1", Dir: dir, Prune: true, DryRun: true,
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if len(rep.Deleted) != 1 || rep.Deleted[0] != "gone.css" {
		t.Errorf("Deleted = %v, want [gone.css]", rep.Deleted)
	}
	if rep.Bytes != int64(len("new{}")) {
		t.Errorf("Bytes = %d, want %d", rep.Bytes, len("new{}"))
	}
}

// The ledger's Binary marker must survive a forced push.
func TestForcedPushKeepsTheBinaryMarker(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "og.png", "\x89PNGbytes")
	syncSeedState(t, dir, State{ProjectID: "p1", Files: map[string]FileState{
		"og.png": {Etag: "e1", Binary: true},
	}})

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("og.png", "e1", 9))}
		case "write_files":
			return fakeReply{Text: `{"etags":{"og.png":"e2"},"written":1}`}
		}
		return fakeReply{Text: `{"plan_token":"tok"}`}
	})

	if _, err := Push(context.Background(), fakeClient(f), PushOpts{
		ProjectID: "p1", Dir: dir, Force: true,
	}); err != nil {
		t.Fatalf("Push: %v", err)
	}

	st, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Files["og.png"].Binary {
		t.Fatalf("ledger Binary = false after a forced push")
	}

	// End to end: the guard must still hold on the next plain prune.
	d := planPush(map[string]RemoteEntry{"og.png": fileEntry("og.png", "e2", 9)},
		map[string]localFile{}, st, false, true)
	if slices.Contains(d.Delete, "og.png") {
		t.Errorf("planPush routed a tracked binary to Delete: %v", d.Delete)
	}
}

// A write conflict is the whole reason the run failed; it must reach the report.
func TestPushReportsAServerSideWriteConflict(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "a.css", "a{}")

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor()}
		case "write_files":
			return fakeReply{Text: `{"conflicts":[{"path":"a.css","etag":"e9"}]}`, IsError: true}
		}
		return fakeReply{Text: `{"plan_token":"tok"}`}
	})

	rep, err := Push(context.Background(), fakeClient(f), PushOpts{ProjectID: "p1", Dir: dir})
	classified := dsxerr.Classify(err)
	if classified == nil || classified.Kind != dsxerr.KindConflict {
		t.Fatalf("Push error = %v, want a KindConflict", err)
	}
	if !slices.Contains(rep.Conflicts, "a.css") {
		t.Errorf("Conflicts = %v, want it to carry a.css", rep.Conflicts)
	}
	if !slices.IsSorted(rep.Conflicts) {
		t.Errorf("Conflicts = %v, want sorted", rep.Conflicts)
	}
}
