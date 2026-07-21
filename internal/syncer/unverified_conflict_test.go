package syncer

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// TestAStaleBaselineConflictIsAStaleProofNotUnverified: an untracked path
// whose baseline etag no longer matches the listing (the server revision
// moved — e.g. a same-content resave, which the measured fact makes the
// common case: every write rotates the etag) but whose baseline sha still
// matches the local file. This is neither "never verified" (a baseline DID
// check these bytes once) nor a proven divergence against the current
// revision (dsx never re-checked THIS revision) — the conflict text must
// name what actually happened: verified, but against an earlier revision,
// and must not claim "local differs" / "server moved ahead" / "never
// verified".
func TestAStaleBaselineConflictIsAStaleProofNotUnverified(t *testing.T) {
	body := []byte("hello world")

	t.Run("pull", func(t *testing.T) {
		dir := t.TempDir()
		mkfile(t, dir, "a.css", string(body))

		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			if name == "list_files" {
				return fakeReply{Text: listingFor(fileEntry("a.css", "e2", int64(len(body))))}
			}
			return fakeReply{Text: envelopeFor("a.css", "e2", string(body))}
		})
		c := fakeClient(f)

		// Endpoint must match c.Endpoint(), or bound() discards Verified
		// wholesale and this test would pass vacuously with an empty baseline.
		bl := Baseline{ProjectID: "proj-A", Endpoint: c.Endpoint(), Verified: map[string]BaselineEntry{
			"a.css": {Etag: "e1", Size: int64(len(body)), SHA: SHA256Hex(body)},
		}}
		if err := bl.save(dir); err != nil {
			t.Fatal(err)
		}

		rep, err := Pull(context.Background(), c, PullOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 2})
		if err != nil {
			t.Fatalf("Pull errored instead of reporting: %v", err)
		}
		if !slices.Equal(rep.StaleProof, []string{"a.css"}) {
			t.Fatalf("StaleProof = %v, want [a.css]", rep.StaleProof)
		}
		if len(rep.Unverified) != 0 || len(rep.Diverged) != 0 {
			t.Errorf("unverified=%v diverged=%v, want both empty — the path belongs to StaleProof alone", rep.Unverified, rep.Diverged)
		}
		out := rep.Render(false)
		if strings.Contains(out, "local differs") {
			t.Errorf("claims a divergence nothing proves: %q", out)
		}
		if strings.Contains(out, "never verified") {
			t.Errorf("claims dsx never checked, but the baseline proved these bytes once: %q", out)
		}
		if !strings.Contains(out, "dsx fetch") {
			t.Errorf("does not name the way to re-check the current revision: %q", out)
		}
	})

	t.Run("push", func(t *testing.T) {
		dir := t.TempDir()
		mkfile(t, dir, "a.css", string(body))

		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			if name == "list_files" {
				return fakeReply{Text: listingFor(fileEntry("a.css", "e2", int64(len(body))))}
			}
			return fakeReply{Text: "unexpected " + name, IsError: true}
		})
		c := fakeClient(f)

		// Endpoint must match c.Endpoint(), or bound() discards Verified
		// wholesale and this test would pass vacuously with an empty baseline.
		bl := Baseline{ProjectID: "proj-A", Endpoint: c.Endpoint(), Verified: map[string]BaselineEntry{
			"a.css": {Etag: "e1", Size: int64(len(body)), SHA: SHA256Hex(body)},
		}}
		if err := bl.save(dir); err != nil {
			t.Fatal(err)
		}

		rep, err := Push(context.Background(), c, PushOpts{ProjectID: "proj-A", Dir: dir})
		if err != nil {
			t.Fatalf("Push errored instead of reporting: %v", err)
		}
		if !slices.Equal(rep.StaleProof, []string{"a.css"}) {
			t.Fatalf("StaleProof = %v, want [a.css]", rep.StaleProof)
		}
		if len(rep.Unverified) != 0 || len(rep.Diverged) != 0 {
			t.Errorf("unverified=%v diverged=%v, want both empty — the path belongs to StaleProof alone", rep.Unverified, rep.Diverged)
		}
		out := rep.Render(false)
		if strings.Contains(out, "server moved ahead") {
			t.Errorf("claims a divergence nothing proves: %q", out)
		}
		if strings.Contains(out, "never verified") {
			t.Errorf("claims dsx never checked, but the baseline proved these bytes once: %q", out)
		}
		if !strings.Contains(out, "dsx fetch") {
			t.Errorf("does not name the way to re-check the current revision: %q", out)
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
