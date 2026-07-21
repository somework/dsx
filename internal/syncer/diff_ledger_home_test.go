package syncer

import (
	"context"
	"os"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// TestDiffChecksLedgerHomeBeforeTheRoundTrip mirrors
// TestFetchChecksLedgerHomeBeforeTheRoundTrip: checkLedgerHome is wired into
// Diff independently of Fetch/Pull/Push, and only a test against Diff itself
// proves it. Paired with a positive control in the same function so the
// absence assertion (list_files called 0 times) cannot pass against a Diff
// that simply never calls list_files at all.
func TestDiffChecksLedgerHomeBeforeTheRoundTrip(t *testing.T) {
	t.Run("blocked: symlinked .dsx", func(t *testing.T) {
		dir, target := symlinkedDsxDir(t)
		mkfile(t, dir, "a.css", "abc")

		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			return fakeReply{Text: listingFor(fileEntry("a.css", "e1", 3))}
		})

		_, err := Diff(context.Background(), fakeClient(f), DiffOpts{
			ProjectID: "proj-A", Dir: dir, Concurrency: 1,
		})
		if err == nil {
			t.Fatal("Diff accepted a symlinked .dsx")
		}
		if got := dsxerr.Classify(err).Kind; got != dsxerr.KindLocal {
			t.Errorf("kind=%v, want %v", got, dsxerr.KindLocal)
		}
		if got := f.CountTool("list_files"); got != 0 {
			t.Errorf("list_files called %d times, want 0 — checkLedgerHome must run before the round trip", got)
		}
		entries, rdErr := os.ReadDir(target)
		if rdErr != nil {
			t.Fatal(rdErr)
		}
		if len(entries) != 0 {
			t.Errorf("Diff wrote through the symlink: %v", entries)
		}
	})

	t.Run("positive control: ordinary directory", func(t *testing.T) {
		dir := t.TempDir()
		mkfile(t, dir, "a.css", "abc")

		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			if name == "list_files" {
				return fakeReply{Text: listingFor(fileEntry("a.css", "e1", 3))}
			}
			p, _ := args["path"].(string)
			return fakeReply{Text: envelopeFor(p, "e1", "abc")}
		})

		if _, err := Diff(context.Background(), fakeClient(f), DiffOpts{
			ProjectID: "proj-A", Dir: dir, Concurrency: 1,
		}); err != nil {
			t.Fatalf("Diff errored against an unblocked .dsx: %v", err)
		}
		if got := f.CountTool("list_files"); got != 1 {
			t.Errorf("list_files called %d times, want 1", got)
		}
	})
}
