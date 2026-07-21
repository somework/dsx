package syncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// TestFetchChecksLedgerHomeBeforeTheRoundTrip mirrors
// TestPullRefusesASymlinkedDsxBeforeFetchingAnything: checkLedgerHome is
// wired into Fetch independently of Pull and Push, and only a test against
// Fetch itself proves it — a shared helper proves nothing about wiring.
// Paired with a positive control in the same function so the absence
// assertion (list_files called 0 times) cannot pass against a Fetch that
// simply never calls list_files at all.
func TestFetchChecksLedgerHomeBeforeTheRoundTrip(t *testing.T) {
	t.Run("blocked: symlinked .dsx", func(t *testing.T) {
		dir, target := symlinkedDsxDir(t)
		mkfile(t, dir, "a.css", "abc")

		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			return fakeReply{Text: listingFor(fileEntry("a.css", "e1", 3))}
		})

		_, err := Fetch(context.Background(), fakeClient(f), FetchOpts{
			ProjectID: "proj-A", Dir: dir, Concurrency: 1,
		})
		if err == nil {
			t.Fatal("Fetch accepted a symlinked .dsx")
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
			t.Errorf("Fetch wrote through the symlink: %v", entries)
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

		if _, err := Fetch(context.Background(), fakeClient(f), FetchOpts{
			ProjectID: "proj-A", Dir: dir, Concurrency: 1,
		}); err != nil {
			t.Fatalf("Fetch errored against an unblocked .dsx: %v", err)
		}
		if got := f.CountTool("list_files"); got != 1 {
			t.Errorf("list_files called %d times, want 1", got)
		}
		if _, err := os.Stat(filepath.Join(StateDir(dir), baselineBaseName)); err != nil {
			t.Errorf("baseline.json was not written: %v", err)
		}
	})
}
