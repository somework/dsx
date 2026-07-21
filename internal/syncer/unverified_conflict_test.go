package syncer

import (
	"context"
	"strings"
	"testing"
)

// TestAStaleBaselineConflictDoesNotClaimDivergence: an untracked path whose
// baseline etag no longer matches the listing (the server revision moved —
// e.g. a same-content resave) but whose baseline sha still matches the local
// file. Nothing is known to differ, dsx just never re-verified against the
// new revision, so the conflict text must not claim "local differs" /
// "server moved ahead" and must name `dsx fetch` as the way to check.
func TestAStaleBaselineConflictDoesNotClaimDivergence(t *testing.T) {
	body := []byte("hello world")

	t.Run("pull", func(t *testing.T) {
		dir := t.TempDir()
		mkfile(t, dir, "a.css", string(body))

		bl := Baseline{ProjectID: "proj-A", Verified: map[string]BaselineEntry{
			"a.css": {Etag: "e1", Size: int64(len(body)), SHA: SHA256Hex(body)},
		}}
		if err := bl.save(dir); err != nil {
			t.Fatal(err)
		}

		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			if name == "list_files" {
				return fakeReply{Text: listingFor(fileEntry("a.css", "e2", int64(len(body))))}
			}
			return fakeReply{Text: envelopeFor("a.css", "e2", string(body))}
		})

		rep, err := Pull(context.Background(), fakeClient(f), PullOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 2})
		if err != nil {
			t.Fatalf("Pull errored instead of reporting: %v", err)
		}
		out := rep.Render(false)
		if strings.Contains(out, "local differs") {
			t.Errorf("claims a divergence nothing proves: %q", out)
		}
		if !strings.Contains(out, "dsx fetch") {
			t.Errorf("does not name the way to check without writing: %q", out)
		}
	})

	t.Run("push", func(t *testing.T) {
		dir := t.TempDir()
		mkfile(t, dir, "a.css", string(body))

		bl := Baseline{ProjectID: "proj-A", Verified: map[string]BaselineEntry{
			"a.css": {Etag: "e1", Size: int64(len(body)), SHA: SHA256Hex(body)},
		}}
		if err := bl.save(dir); err != nil {
			t.Fatal(err)
		}

		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			if name == "list_files" {
				return fakeReply{Text: listingFor(fileEntry("a.css", "e2", int64(len(body))))}
			}
			return fakeReply{Text: "unexpected " + name, IsError: true}
		})

		rep, err := Push(context.Background(), fakeClient(f), PushOpts{ProjectID: "proj-A", Dir: dir})
		if err != nil {
			t.Fatalf("Push errored instead of reporting: %v", err)
		}
		out := rep.Render(false)
		if strings.Contains(out, "server moved ahead") {
			t.Errorf("claims a divergence nothing proves: %q", out)
		}
		if !strings.Contains(out, "dsx fetch") {
			t.Errorf("does not name the way to check without writing: %q", out)
		}
	})
}

// TestATrackedConflictKeepsItsAccurateWording is the positive control: a real
// ledger entry proves the last common state, so "local differs" / "server
// moved ahead" stays a true claim there and must not be softened.
func TestATrackedConflictKeepsItsAccurateWording(t *testing.T) {
	t.Run("pull", func(t *testing.T) {
		dir := t.TempDir()
		mkfile(t, dir, "a.css", "edited")
		syncSeedState(t, dir, State{ProjectID: "proj-A", Files: map[string]FileState{
			"a.css": {Etag: "e1", Size: 3, SHA: SHA256Hex([]byte("old"))},
		}})

		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			return fakeReply{Text: listingFor(fileEntry("a.css", "e1", 3))}
		})

		rep, err := Pull(context.Background(), fakeClient(f), PullOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 2})
		if err != nil {
			t.Fatalf("Pull errored instead of reporting: %v", err)
		}
		out := rep.Render(false)
		if !strings.Contains(out, "local differs; --force to overwrite") {
			t.Errorf("a genuine tracked divergence lost its accurate wording: %q", out)
		}
	})

	t.Run("push", func(t *testing.T) {
		dir := t.TempDir()
		mkfile(t, dir, "a.css", "edited")
		syncSeedState(t, dir, State{ProjectID: "proj-A", Files: map[string]FileState{
			"a.css": {Etag: "e1", Size: 3, SHA: SHA256Hex([]byte("old"))},
		}})

		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			return fakeReply{Text: listingFor(fileEntry("a.css", "e2", 3))}
		})

		rep, err := Push(context.Background(), fakeClient(f), PushOpts{ProjectID: "proj-A", Dir: dir})
		if err != nil {
			t.Fatalf("Push errored instead of reporting: %v", err)
		}
		out := rep.Render(false)
		if !strings.Contains(out, "server moved ahead; `dsx pull` first, or --force") {
			t.Errorf("a genuine tracked divergence lost its accurate wording: %q", out)
		}
	})
}
