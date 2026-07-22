package syncer

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// TestABrokenLeaseReachesTheReportAndTheHint covers the layer between
// planPush and the caller, which nothing tested: every lease test drives
// planPush directly, so `rep.LeaseBroken = d.LeaseBroken` and
// `rep.PruneLeaseBroken = d.PruneLeaseBroken` could both be deleted and the
// whole suite stayed green.
//
// Dropping either is not a cosmetic loss. The field feeds except() in
// Outcome(), so a broken lease with no field falls into the `plain` bucket and
// renders as "server moved ahead; `dsx pull` first, or --force" — advising the
// blind flag that --force-with-lease exists to avoid, on exactly the path
// where someone else's write is the reason. Asserting the wording is therefore
// the point, not decoration: the exit code is 3 either way.
func TestABrokenLeaseReachesTheReportAndTheHint(t *testing.T) {
	t.Run("write lane", func(t *testing.T) {
		dir := t.TempDir()
		mkfile(t, dir, "a.css", "MINE\n")

		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			if name == "list_files" {
				return fakeReply{Text: listingFor(fileEntry("a.css", "SERVER-NEW", 5))}
			}
			return fakeReply{Text: "unexpected " + name, IsError: true}
		})
		c := fakeClient(f)

		seedLeaseFixture(t, dir, c.Endpoint(), nil, map[string]SnapshotEntry{
			"a.css": {Size: 5, Etag: "FETCHED"},
		})

		rep, err := Push(context.Background(), c, PushOpts{
			ProjectID: "p", Dir: dir, Concurrency: 1, Lease: true,
		})
		if err != nil {
			t.Fatalf("Push: %v", err)
		}
		if !slices.Equal(rep.LeaseBroken, []string{"a.css"}) {
			t.Errorf("LeaseBroken = %v, want [a.css] — the plan refused the write and the report lost it",
				rep.LeaseBroken)
		}
		assertLeaseHint(t, rep.Outcome(), "the lease does not cover it")
	})

	t.Run("delete lane", func(t *testing.T) {
		dir := t.TempDir()

		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			if name == "list_files" {
				return fakeReply{Text: listingFor(fileEntry("gone.css", "SERVER-NEW", 3))}
			}
			return fakeReply{Text: "unexpected " + name, IsError: true}
		})
		c := fakeClient(f)

		seedLeaseFixture(t, dir, c.Endpoint(), map[string]FileState{
			"gone.css": {Etag: "LEDGER", Size: 3, SHA: SHA256Hex([]byte("old"))},
		}, map[string]SnapshotEntry{
			"gone.css": {Size: 3, Etag: "FETCHED"},
		})

		rep, err := Push(context.Background(), c, PushOpts{
			ProjectID: "p", Dir: dir, Concurrency: 1, Lease: true, Prune: true,
		})
		if err != nil {
			t.Fatalf("Push: %v", err)
		}
		if !slices.Equal(rep.PruneLeaseBroken, []string{"gone.css"}) {
			t.Errorf("PruneLeaseBroken = %v, want [gone.css]", rep.PruneLeaseBroken)
		}
		if len(rep.Deleted) != 0 {
			t.Errorf("Deleted = %v, want none — the lease refused this delete", rep.Deleted)
		}
		assertLeaseHint(t, rep.Outcome(), "the lease does not cover deleting them")
	})
}

func seedLeaseFixture(t *testing.T, dir, endpoint string, files map[string]FileState, listing map[string]SnapshotEntry) {
	t.Helper()
	if files == nil {
		files = map[string]FileState{}
	}
	st := State{ProjectID: "p", Endpoint: endpoint, Files: files}
	if err := st.save(dir); err != nil {
		t.Fatal(err)
	}
	bl := Baseline{
		ProjectID: "p",
		Endpoint:  endpoint,
		Verified:  map[string]BaselineEntry{},
		Listing:   listing,
	}
	if err := bl.save(dir); err != nil {
		t.Fatal(err)
	}
}

// assertLeaseHint holds both halves: the lease's own wording must be there,
// and the blind flag must not — invariant 20's refusal names `dsx fetch` and
// never --force, because the lease was broken by someone else's write.
func assertLeaseHint(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatal("a refused lease produced no conflict error; exit 0 tells an agent to carry on")
	}
	msg := err.Error()
	if !strings.Contains(msg, want) {
		t.Errorf("hint %q does not carry %q", msg, want)
	}
	if !strings.Contains(msg, "dsx fetch") {
		t.Errorf("hint %q does not name `dsx fetch`, the only thing that can clear a broken lease", msg)
	}
	if strings.Contains(msg, "--force") {
		t.Errorf("hint %q offers --force for a lease someone else broke; that is the destruction "+
			"the flag was asked for to prevent", msg)
	}
}
