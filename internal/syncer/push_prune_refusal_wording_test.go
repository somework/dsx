package syncer

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// A prune delete the SERVER refuses (it moved the path ahead between the
// listing and the delete) reaches the report only through addConflicts, which
// merged it into Conflicts alone. Render's ladder is BinaryConflicts ->
// PruneConflicts -> generic, so such a path fell through to the generic line
// and stdout told the user to `dsx pull` first — while the returned error, from
// deletePaths, said something else. Two channels, opposite advice.
//
// planPush cannot produce this state (a remote whose etag matches the ledger is
// routed to Delete, not PruneConflicts), so only a runtime refusal reaches it.
func TestRefusedPruneDeleteRendersThePruneRemedyNotThePullOne(t *testing.T) {
	dir := t.TempDir()
	// No local files: gone.css exists on the server and in the ledger only, so
	// planPush's prune loop routes it to Delete. The etag MATCHES the ledger,
	// which is what keeps it out of d.PruneConflicts at plan time.
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
		t.Errorf("Deleted = %v, want empty — the server deleted nothing", rep.Deleted)
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
		t.Errorf("stdout offers the pull remedy for a refused PRUNE delete, "+
			"contradicting the error text: %q", line)
	}
	if !strings.Contains(line, "DELETE") {
		t.Errorf("stdout does not warn that --force DELETES the server's newer copy: %q", line)
	}
	// The machine-facing slice must carry it too, or --json readers keep the
	// same wrong classification stdout just shed.
	if !slices.Contains(rep.PruneConflicts, "gone.css") {
		t.Errorf("PruneConflicts = %v, want it to name gone.css", rep.PruneConflicts)
	}
}
