package syncer

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// A prune delete the SERVER refuses (it moved the path ahead between the
// listing and the delete) reaches the report through addPruneConflicts. Its
// rendered line must carry the prune remedy, not the pull one.
func TestRefusedPruneDeleteRendersThePruneRemedyNotThePullOne(t *testing.T) {
	dir := t.TempDir()
	// No local files: gone.css exists on the server and in the ledger only, and
	// its etag MATCHES the ledger, so it is planned as a Delete.
	syncSeedState(t, dir, State{ProjectID: "p1", Files: map[string]FileState{
		"gone.css": {Etag: "e1", Size: 6, SHA: SHA256Hex([]byte("orig{}"))},
	}})

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("gone.css", "e1", 6))}
		case "finalize_plan":
			return fakeReply{Text: `{"plan_token":"tok"}`}
		case "delete_files":
			// The server moved gone.css ahead after the listing dsx planned on.
			return fakeReply{
				IsError: true,
				Text:    `{"conflicts":[{"path":"gone.css","etag":"e9"}],"message":"moved ahead"}`,
			}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	rep, err := Push(context.Background(), fakeClient(f), PushOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4, Prune: true,
	})
	if err == nil {
		t.Fatal("Push returned nil after the server refused the prune delete")
	}
	if len(rep.Deleted) != 0 {
		t.Errorf("Deleted = %v, want empty", rep.Deleted)
	}

	out := rep.Render(false)
	line := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "gone.css") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("Render printed no line for the refused path:\n%s", out)
	}
	if strings.Contains(line, "dsx pull") {
		t.Errorf("stdout offers the pull remedy for a refused PRUNE delete: %q", line)
	}
	if !strings.Contains(line, "DELETE") {
		t.Errorf("stdout does not warn that --force DELETES the server's newer copy: %q", line)
	}
	// The machine-facing slice must carry it too.
	if !slices.Contains(rep.PruneConflicts, "gone.css") {
		t.Errorf("PruneConflicts = %v, want it to name gone.css", rep.PruneConflicts)
	}
}
