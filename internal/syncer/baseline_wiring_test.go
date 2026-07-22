package syncer

import (
	"context"
	"os"
	"testing"

	"slices"
)

// TestACorruptBaselineDoesNotBlockASync: baseline.json is dsx's own cache of
// what fetch downloaded, not a claim of ownership (see Baseline's doc
// comment). An undecodable, unreadable, or wrong-shaped baseline must be
// treated exactly like a missing one — an empty Baseline — so a sync
// proceeds with a re-verify cost, never a hard stop that points the user at
// `rm -rf .dsx`, which would take state.json down with it.
func TestACorruptBaselineDoesNotBlockASync(t *testing.T) {
	emptyListingFake := func(t *testing.T) *fakeMCP {
		t.Helper()
		return newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			if name == "list_files" {
				return fakeReply{Text: listingFor()}
			}
			return fakeReply{Text: "unexpected " + name, IsError: true}
		})
	}

	t.Run("pull: not json at all", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(StateDir(dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(BaselinePath(dir), []byte("not json at all"), 0o600); err != nil {
			t.Fatal(err)
		}

		rep, err := Pull(context.Background(), fakeClient(emptyListingFake(t)), PullOpts{
			ProjectID: "proj-A", Dir: dir, Concurrency: 2,
		})
		if err != nil {
			t.Fatalf("Pull errored on a corrupt baseline cache: %v", err)
		}
		if rep.Verified != 0 {
			t.Errorf("Verified = %d, want 0", rep.Verified)
		}
	})

	t.Run("push: not json at all", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(StateDir(dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(BaselinePath(dir), []byte("not json at all"), 0o600); err != nil {
			t.Fatal(err)
		}

		rep, err := Push(context.Background(), fakeClient(emptyListingFake(t)), PushOpts{
			ProjectID: "proj-A", Dir: dir,
		})
		if err != nil {
			t.Fatalf("Push errored on a corrupt baseline cache: %v", err)
		}
		if rep.Verified != 0 {
			t.Errorf("Verified = %d, want 0", rep.Verified)
		}
	})

	t.Run("pull: type mismatch, a number where the map belongs", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(StateDir(dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(BaselinePath(dir), []byte(`{"verified": 5}`), 0o600); err != nil {
			t.Fatal(err)
		}

		rep, err := Pull(context.Background(), fakeClient(emptyListingFake(t)), PullOpts{
			ProjectID: "proj-A", Dir: dir, Concurrency: 2,
		})
		if err != nil {
			t.Fatalf("Pull errored on a type-mismatched baseline cache: %v", err)
		}
		if rep.Verified != 0 {
			t.Errorf("Verified = %d, want 0", rep.Verified)
		}
	})

	t.Run("pull: baseline.json is a directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(BaselinePath(dir), 0o755); err != nil {
			t.Fatal(err)
		}

		rep, err := Pull(context.Background(), fakeClient(emptyListingFake(t)), PullOpts{
			ProjectID: "proj-A", Dir: dir, Concurrency: 2,
		})
		if err != nil {
			t.Fatalf("Pull errored when baseline.json is a directory: %v", err)
		}
		if rep.Verified != 0 {
			t.Errorf("Verified = %d, want 0", rep.Verified)
		}
	})
}

// TestPushConsumesABaselineOnDisk closes the coverage gap named in the
// review: every integration-level baseline guard ran through Pull, none
// through Push. Mirrors TestFirstContactStillRefusesWhenABaselineExists but
// for Push — an untracked local file whose bytes match a fresh baseline must
// be reported Verified and never re-uploaded.
func TestPushConsumesABaselineOnDisk(t *testing.T) {
	dir := t.TempDir()
	body := []byte("shared bytes\n")
	mkfile(t, dir, "readme.md", string(body))

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(fileEntry("readme.md", "e1", int64(len(body))))}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})
	c := fakeClient(f)

	bl := Baseline{
		ProjectID: "proj-A",
		Endpoint:  c.Endpoint(),
		Verified: map[string]BaselineEntry{
			"readme.md": {Etag: "e1", Size: int64(len(body)), SHA: SHA256Hex(body)},
		},
	}
	if err := bl.save(dir); err != nil {
		t.Fatal(err)
	}

	rep, err := Push(context.Background(), c, PushOpts{
		ProjectID: "proj-A", Dir: dir,
	})
	if err != nil {
		t.Fatalf("Push errored instead of reporting: %v", err)
	}
	if rep.Verified != 1 {
		t.Errorf("Verified = %d, want 1 for the baselined path", rep.Verified)
	}
	if slices.Contains(rep.Conflicts, "readme.md") {
		t.Errorf("Conflicts = %v, readme.md must not be reported as a conflict — it is proven", rep.Conflicts)
	}
	if got := f.CountTool("write_files"); got != 0 {
		t.Errorf("write_files called %d times, want 0 — a proven path must not be re-uploaded", got)
	}
}

// TestPullDiscardsAForeignBaseline and TestPushDiscardsAForeignBaseline prove
// invariant 13's (project, endpoint) binding is actually wired through
// bl.bound(...) in Pull/Push, not merely unit-tested on Baseline.bound in
// isolation. Each records a baseline against a foreign project or a foreign
// endpoint and asserts the run treats it as absent: the path stays a
// conflict and Verified stays 0.
func TestPullDiscardsAForeignBaseline(t *testing.T) {
	fakeFor := func(t *testing.T, body []byte) *fakeMCP {
		t.Helper()
		return newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			if name == "list_files" {
				return fakeReply{Text: listingFor(fileEntry("readme.md", "e1", int64(len(body))))}
			}
			return fakeReply{Text: "unexpected " + name, IsError: true}
		})
	}

	t.Run("foreign project", func(t *testing.T) {
		dir := t.TempDir()
		body := []byte("shared bytes\n")
		mkfile(t, dir, "readme.md", string(body))

		f := fakeFor(t, body)
		c := fakeClient(f)

		bl := Baseline{
			ProjectID: "proj-OTHER",
			Endpoint:  c.Endpoint(),
			Verified: map[string]BaselineEntry{
				"readme.md": {Etag: "e1", Size: int64(len(body)), SHA: SHA256Hex(body)},
			},
		}
		if err := bl.save(dir); err != nil {
			t.Fatal(err)
		}

		rep, err := Pull(context.Background(), c, PullOpts{
			ProjectID: "proj-A", Dir: dir, Concurrency: 2,
		})
		if err != nil {
			t.Fatalf("Pull errored: %v", err)
		}
		if rep.Verified != 0 {
			t.Errorf("Verified = %d, want 0 — a baseline recorded for a different project must be discarded", rep.Verified)
		}
		if !slices.Contains(rep.Conflicts, "readme.md") {
			t.Errorf("Conflicts = %v, want readme.md present", rep.Conflicts)
		}
	})

	t.Run("foreign endpoint", func(t *testing.T) {
		dir := t.TempDir()
		body := []byte("shared bytes\n")
		mkfile(t, dir, "readme.md", string(body))

		f := fakeFor(t, body)
		c := fakeClient(f)

		bl := Baseline{
			ProjectID: "proj-A",
			Endpoint:  "https://evil.example/mcp",
			Verified: map[string]BaselineEntry{
				"readme.md": {Etag: "e1", Size: int64(len(body)), SHA: SHA256Hex(body)},
			},
		}
		if err := bl.save(dir); err != nil {
			t.Fatal(err)
		}

		rep, err := Pull(context.Background(), c, PullOpts{
			ProjectID: "proj-A", Dir: dir, Concurrency: 2,
		})
		if err != nil {
			t.Fatalf("Pull errored: %v", err)
		}
		if rep.Verified != 0 {
			t.Errorf("Verified = %d, want 0 — a baseline recorded for a different endpoint must be discarded", rep.Verified)
		}
		if !slices.Contains(rep.Conflicts, "readme.md") {
			t.Errorf("Conflicts = %v, want readme.md present", rep.Conflicts)
		}
	})
}

func TestPushDiscardsAForeignBaseline(t *testing.T) {
	fakeFor := func(t *testing.T, body []byte) *fakeMCP {
		t.Helper()
		return newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			if name == "list_files" {
				return fakeReply{Text: listingFor(fileEntry("readme.md", "e1", int64(len(body))))}
			}
			return fakeReply{Text: "unexpected " + name, IsError: true}
		})
	}

	t.Run("foreign project", func(t *testing.T) {
		dir := t.TempDir()
		body := []byte("shared bytes\n")
		mkfile(t, dir, "readme.md", string(body))

		f := fakeFor(t, body)
		c := fakeClient(f)

		bl := Baseline{
			ProjectID: "proj-OTHER",
			Endpoint:  c.Endpoint(),
			Verified: map[string]BaselineEntry{
				"readme.md": {Etag: "e1", Size: int64(len(body)), SHA: SHA256Hex(body)},
			},
		}
		if err := bl.save(dir); err != nil {
			t.Fatal(err)
		}

		rep, err := Push(context.Background(), c, PushOpts{
			ProjectID: "proj-A", Dir: dir,
		})
		if err != nil {
			t.Fatalf("Push errored: %v", err)
		}
		if rep.Verified != 0 {
			t.Errorf("Verified = %d, want 0 — a baseline recorded for a different project must be discarded", rep.Verified)
		}
		if !slices.Contains(rep.Conflicts, "readme.md") {
			t.Errorf("Conflicts = %v, want readme.md present", rep.Conflicts)
		}
	})

	t.Run("foreign endpoint", func(t *testing.T) {
		dir := t.TempDir()
		body := []byte("shared bytes\n")
		mkfile(t, dir, "readme.md", string(body))

		f := fakeFor(t, body)
		c := fakeClient(f)

		bl := Baseline{
			ProjectID: "proj-A",
			Endpoint:  "https://evil.example/mcp",
			Verified: map[string]BaselineEntry{
				"readme.md": {Etag: "e1", Size: int64(len(body)), SHA: SHA256Hex(body)},
			},
		}
		if err := bl.save(dir); err != nil {
			t.Fatal(err)
		}

		rep, err := Push(context.Background(), c, PushOpts{
			ProjectID: "proj-A", Dir: dir,
		})
		if err != nil {
			t.Fatalf("Push errored: %v", err)
		}
		if rep.Verified != 0 {
			t.Errorf("Verified = %d, want 0 — a baseline recorded for a different endpoint must be discarded", rep.Verified)
		}
		if !slices.Contains(rep.Conflicts, "readme.md") {
			t.Errorf("Conflicts = %v, want readme.md present", rep.Conflicts)
		}
	})
}

// TestBaselineDoesNotSkipAModifiedTrackedFile isolates planPush's !tracked
// conjunct from case ordering. TestBaselineNeverOverridesARealLedgerEntry's
// push subtest reddens only via remoteMoved (the earlier `case !force &&
// remoteMoved:` arm intercepts it before `case proven` is ever reached), so
// it does not actually exercise !tracked. This fixture holds remoteMoved
// false (the listing etag still matches the ledger's) while the local file
// has genuinely changed and a baseline "proves" the new bytes — the only
// shape that isolates !tracked from the rest of the switch.
func TestBaselineDoesNotSkipAModifiedTrackedFile(t *testing.T) {
	d := planPush(
		remoteOf(RemoteEntry{Path: "a.css", Etag: "e1"}),
		localOf(localFile{Path: "a.css", SHA: "new"}),
		stateOf(map[string]FileState{"a.css": {Etag: "e1", SHA: "old"}}),
		map[string]BaselineEntry{"a.css": {Etag: "e1", SHA: "new"}}, nil, forceNone, false)

	if d.Verified != 0 {
		t.Errorf("Verified = %d, want 0 — a real ledger entry must ignore the baseline", d.Verified)
	}
	if len(d.Write) != 1 || d.Write[0].Path != "a.css" || d.Write[0].IfMatch != "e1" {
		t.Fatalf("Write = %+v, want one entry for a.css guarded by etag e1 — "+
			"the locally edited, tracked file must still be uploaded", d.Write)
	}
}
