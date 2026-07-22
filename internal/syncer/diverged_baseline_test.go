package syncer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAFreshBaselineProvenToDifferIsNotClaimedUnverified: the local file was
// edited after the last `dsx fetch`, so the baseline sha no longer matches
// on-disk, but the baseline etag still matches the current listing — the
// server has not moved since that fetch. This is not "never verified": the
// fetch that populated the baseline checked exactly this server revision and
// found the bytes to differ. Claiming "never verified against the server;
// `dsx fetch` checks" is false (it was checked) and points at a remedy the
// user has already run.
func TestAFreshBaselineProvenToDifferIsNotClaimedUnverified(t *testing.T) {
	fetchedBody := []byte("server bytes")
	editedBody := []byte("local bytes, edited after fetch")

	t.Run("pull", func(t *testing.T) {
		dir := t.TempDir()
		mkfile(t, dir, "a.css", string(editedBody))

		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			if name == "list_files" {
				return fakeReply{Text: listingFor(fileEntry("a.css", "e1", int64(len(fetchedBody))))}
			}
			return fakeReply{Text: envelopeFor("a.css", "e1", string(fetchedBody))}
		})
		c := fakeClient(f)

		bl := Baseline{ProjectID: "proj-A", Endpoint: c.Endpoint(), Verified: map[string]BaselineEntry{
			"a.css": {Etag: "e1", Size: int64(len(fetchedBody)), SHA: SHA256Hex(fetchedBody)},
		}}
		if err := bl.save(dir); err != nil {
			t.Fatal(err)
		}

		rep, err := Pull(context.Background(), c, PullOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 2})
		if err != nil {
			t.Fatalf("Pull errored instead of reporting: %v", err)
		}
		out := rep.Render(false)
		if strings.Contains(out, "never verified") {
			t.Errorf("claims dsx never checked, but the last fetch proved these bytes differ: %q", out)
		}
		if !strings.Contains(out, "differs from the server") {
			t.Errorf("does not state the proven divergence: %q", out)
		}
		if !strings.Contains(out, "--force") {
			t.Errorf("does not name the remedy: %q", out)
		}
	})

	t.Run("push", func(t *testing.T) {
		dir := t.TempDir()
		mkfile(t, dir, "a.css", string(editedBody))

		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			if name == "list_files" {
				return fakeReply{Text: listingFor(fileEntry("a.css", "e1", int64(len(fetchedBody))))}
			}
			return fakeReply{Text: "unexpected " + name, IsError: true}
		})
		c := fakeClient(f)

		bl := Baseline{ProjectID: "proj-A", Endpoint: c.Endpoint(), Verified: map[string]BaselineEntry{
			"a.css": {Etag: "e1", Size: int64(len(fetchedBody)), SHA: SHA256Hex(fetchedBody)},
		}}
		if err := bl.save(dir); err != nil {
			t.Fatal(err)
		}

		rep, err := Push(context.Background(), c, PushOpts{ProjectID: "proj-A", Dir: dir})
		if err != nil {
			t.Fatalf("Push errored instead of reporting: %v", err)
		}
		out := rep.Render(false)
		if strings.Contains(out, "never verified") {
			t.Errorf("claims dsx never checked, but the last fetch proved these bytes differ: %q", out)
		}
		if !strings.Contains(out, "differs from the server") {
			t.Errorf("does not state the proven divergence: %q", out)
		}
		if !strings.Contains(out, "--force") {
			t.Errorf("does not name the remedy: %q", out)
		}
	})
}

// TestADivergedConflictStillExitsNonZero: softening the wording must not
// soften the exit code — a proven divergence is still a conflict, `push`/
// `pull` must still refuse to move bytes without --force.
func TestADivergedConflictStillExitsNonZero(t *testing.T) {
	fetchedBody := []byte("server bytes")
	editedBody := []byte("edited after fetch")

	dir := t.TempDir()
	mkfile(t, dir, "a.css", string(editedBody))

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(fileEntry("a.css", "e1", int64(len(fetchedBody))))}
		}
		return fakeReply{Text: envelopeFor("a.css", "e1", string(fetchedBody))}
	})
	c := fakeClient(f)

	bl := Baseline{ProjectID: "proj-A", Endpoint: c.Endpoint(), Verified: map[string]BaselineEntry{
		"a.css": {Etag: "e1", Size: int64(len(fetchedBody)), SHA: SHA256Hex(fetchedBody)},
	}}
	if err := bl.save(dir); err != nil {
		t.Fatal(err)
	}

	rep, err := Pull(context.Background(), c, PullOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 2})
	if err != nil {
		t.Fatalf("Pull errored instead of reporting: %v", err)
	}
	if err := rep.Outcome(); err == nil {
		t.Fatalf("a proven divergence reported success")
	}
	if b, readErr := os.ReadFile(filepath.Join(dir, "a.css")); readErr != nil || string(b) != string(editedBody) {
		t.Fatalf("local file was overwritten: %q, %v", b, readErr)
	}
}
