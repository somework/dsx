package syncer

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
)

func remoteOf(entries ...RemoteEntry) map[string]RemoteEntry {
	m := map[string]RemoteEntry{}
	for _, e := range entries {
		e.Type = "file"
		m[e.Path] = e
	}
	return m
}

func localOf(files ...localFile) map[string]localFile {
	m := map[string]localFile{}
	for _, f := range files {
		m[f.Path] = f
	}
	return m
}

func stateOf(entries map[string]FileState) State {
	if entries == nil {
		entries = map[string]FileState{}
	}
	return State{ProjectID: "p", Files: entries}
}

func TestPlanPull(t *testing.T) {
	t.Run("etag match and sha match costs nothing", func(t *testing.T) {
		d := planPull(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "1", Size: 3}),
			localOf(localFile{Path: "a.css", SHA: "sha1"}),
			stateOf(map[string]FileState{"a.css": {Etag: "1", SHA: "sha1"}}),
			nil, false, false)

		if d.Unchanged != 1 || len(d.Fetch) != 0 {
			t.Errorf("unchanged=%d fetch=%v, want 1 and none", d.Unchanged, d.Fetch)
		}
	})

	t.Run("new etag is fetched", func(t *testing.T) {
		d := planPull(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "2"}),
			localOf(localFile{Path: "a.css", SHA: "sha1"}),
			stateOf(map[string]FileState{"a.css": {Etag: "1", SHA: "sha1"}}),
			nil, false, false)

		if !slices.Equal(d.Fetch, []string{"a.css"}) {
			t.Errorf("fetch=%v, want [a.css]", d.Fetch)
		}
	})

	t.Run("untracked remote file is fetched", func(t *testing.T) {
		d := planPull(
			remoteOf(RemoteEntry{Path: "new.css", Etag: "1"}),
			localOf(), stateOf(nil), nil, false, false)

		if !slices.Equal(d.Fetch, []string{"new.css"}) {
			t.Errorf("fetch=%v, want [new.css]", d.Fetch)
		}
	})

	t.Run("local edit at same etag is a conflict, not an overwrite", func(t *testing.T) {
		d := planPull(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "1"}),
			localOf(localFile{Path: "a.css", SHA: "edited"}),
			stateOf(map[string]FileState{"a.css": {Etag: "1", SHA: "sha1"}}),
			nil, false, false)

		if !slices.Equal(d.Conflicts, []string{"a.css"}) {
			t.Errorf("conflicts=%v, want [a.css]", d.Conflicts)
		}
		if len(d.Fetch) != 0 {
			t.Errorf("fetch=%v, want none — a conflict must not silently overwrite", d.Fetch)
		}
	})

	t.Run("force overrides a local edit", func(t *testing.T) {
		d := planPull(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "1"}),
			localOf(localFile{Path: "a.css", SHA: "edited"}),
			stateOf(map[string]FileState{"a.css": {Etag: "1", SHA: "sha1"}}),
			nil, true, false)

		if !slices.Equal(d.Fetch, []string{"a.css"}) {
			t.Errorf("fetch=%v, want [a.css] under --force", d.Fetch)
		}
	})

	t.Run("untracked local collision is unverified, not a claimed conflict", func(t *testing.T) {
		d := planPull(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "1"}),
			localOf(localFile{Path: "a.css", SHA: "mine"}),
			stateOf(nil), nil, false, false)

		if len(d.Conflicts) != 0 || !slices.Equal(d.Unverified, []string{"a.css"}) {
			t.Errorf("conflicts=%v unverified=%v, want none and [a.css] — no ledger entry ever confirmed these bytes, so it must not claim they differ", d.Conflicts, d.Unverified)
		}
	})

	t.Run("known binary at same etag is not re-requested", func(t *testing.T) {
		d := planPull(
			remoteOf(RemoteEntry{Path: "og.png", Etag: "1"}),
			localOf(),
			stateOf(map[string]FileState{"og.png": {Etag: "1", Binary: true}}),
			nil, false, false)

		if !slices.Equal(d.Binary, []string{"og.png"}) {
			t.Errorf("binary=%v, want [og.png]", d.Binary)
		}
		if len(d.Fetch) != 0 {
			t.Errorf("fetch=%v, want none — a known binary must cost no request", d.Fetch)
		}
	})

	t.Run("binary with a new etag is retried", func(t *testing.T) {
		d := planPull(
			remoteOf(RemoteEntry{Path: "og.png", Etag: "2"}),
			localOf(),
			stateOf(map[string]FileState{"og.png": {Etag: "1", Binary: true}}),
			nil, false, false)

		if !slices.Equal(d.Fetch, []string{"og.png"}) {
			t.Errorf("fetch=%v, want [og.png] — a changed etag may no longer be binary", d.Fetch)
		}
	})

	t.Run("prune removes tracked local files gone from the server", func(t *testing.T) {
		d := planPull(
			remoteOf(),
			localOf(localFile{Path: "gone.css", SHA: "s"}),
			stateOf(map[string]FileState{"gone.css": {Etag: "1", SHA: "s"}}),
			nil, false, true)

		if !slices.Equal(d.Delete, []string{"gone.css"}) {
			t.Errorf("delete=%v, want [gone.css]", d.Delete)
		}
	})

	t.Run("prune never touches untracked local files", func(t *testing.T) {
		d := planPull(
			remoteOf(),
			localOf(localFile{Path: "mine.txt", SHA: "s"}),
			stateOf(nil), nil, false, true)

		if len(d.Delete) != 0 {
			t.Errorf("delete=%v, want none — an untracked file was never ours", d.Delete)
		}
	})
}

func TestPlanPush(t *testing.T) {
	t.Run("unchanged file is not sent", func(t *testing.T) {
		d := planPush(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "1"}),
			localOf(localFile{Path: "a.css", SHA: "s"}),
			stateOf(map[string]FileState{"a.css": {Etag: "1", SHA: "s"}}),
			nil, false, false)

		if d.Unchanged != 1 || len(d.Write) != 0 {
			t.Errorf("unchanged=%d write=%v, want 1 and none", d.Unchanged, d.Write)
		}
	})

	t.Run("local edit is sent guarded by if_match", func(t *testing.T) {
		d := planPush(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "1"}),
			localOf(localFile{Path: "a.css", SHA: "edited"}),
			stateOf(map[string]FileState{"a.css": {Etag: "1", SHA: "s"}}),
			nil, false, false)

		if len(d.Write) != 1 || d.Write[0].IfMatch != "1" {
			t.Fatalf("write=%+v, want one entry guarded by etag 1", d.Write)
		}
	})

	t.Run("new file asserts non-existence", func(t *testing.T) {
		d := planPush(
			remoteOf(),
			localOf(localFile{Path: "new.css", SHA: "s"}),
			stateOf(nil), nil, false, false)

		if len(d.Write) != 1 || d.Write[0].IfMatch != "0" {
			t.Fatalf(`write=%+v, want one entry with if_match "0"`, d.Write)
		}
	})

	t.Run("remote moved ahead is a conflict", func(t *testing.T) {
		d := planPush(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "2"}),
			localOf(localFile{Path: "a.css", SHA: "edited"}),
			stateOf(map[string]FileState{"a.css": {Etag: "1", SHA: "s"}}),
			nil, false, false)

		if !slices.Equal(d.Conflicts, []string{"a.css"}) {
			t.Errorf("conflicts=%v, want [a.css]", d.Conflicts)
		}
		if len(d.Write) != 0 {
			t.Errorf("write=%v, want none — must not clobber a newer server copy", d.Write)
		}
	})

	t.Run("force sends unconditionally", func(t *testing.T) {
		d := planPush(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "2"}),
			localOf(localFile{Path: "a.css", SHA: "edited"}),
			stateOf(map[string]FileState{"a.css": {Etag: "1", SHA: "s"}}),
			nil, true, false)

		if len(d.Write) != 1 || d.Write[0].IfMatch != "" {
			t.Fatalf("write=%+v, want one unguarded entry under --force", d.Write)
		}
	})

	t.Run("untracked collision is unverified, not a claimed conflict", func(t *testing.T) {
		d := planPush(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "1"}),
			localOf(localFile{Path: "a.css", SHA: "mine"}),
			stateOf(nil), nil, false, false)

		if len(d.Conflicts) != 0 || !slices.Equal(d.Unverified, []string{"a.css"}) {
			t.Errorf("conflicts=%v unverified=%v, want none and [a.css] — no ledger entry ever confirmed these bytes, so it must not claim they differ", d.Conflicts, d.Unverified)
		}
	})

	// The pull-side equivalent is TestAForcedFirstPullStillWrites — --force
	// must override the Unverified guard exactly as it overrides Conflicts,
	// not just for tracked collisions ("force sends unconditionally" above).
	t.Run("force sends an untracked unverified collision unconditionally", func(t *testing.T) {
		d := planPush(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "1"}),
			localOf(localFile{Path: "a.css", SHA: "mine"}),
			stateOf(nil), nil, true, false)

		if len(d.Unverified) != 0 {
			t.Errorf("unverified=%v, want none — --force must clear it", d.Unverified)
		}
		if len(d.Write) != 1 || d.Write[0].Path != "a.css" {
			t.Errorf("write=%+v, want one entry for a.css — --force must still send it", d.Write)
		}
	})

	t.Run("server ahead with no local change is left to pull", func(t *testing.T) {
		d := planPush(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "2"}),
			localOf(localFile{Path: "a.css", SHA: "s"}),
			stateOf(map[string]FileState{"a.css": {Etag: "1", SHA: "s"}}),
			nil, false, false)

		if d.Unchanged != 1 || len(d.Write) != 0 || len(d.Conflicts) != 0 {
			t.Errorf("unchanged=%d write=%v conflicts=%v, want 1/none/none",
				d.Unchanged, d.Write, d.Conflicts)
		}
	})

	t.Run("prune deletes tracked remote files gone locally", func(t *testing.T) {
		d := planPush(
			remoteOf(RemoteEntry{Path: "gone.css", Etag: "1"}),
			localOf(),
			stateOf(map[string]FileState{"gone.css": {Etag: "1", SHA: "s"}}),
			nil, false, true)

		if !slices.Equal(d.Delete, []string{"gone.css"}) {
			t.Errorf("delete=%v, want [gone.css]", d.Delete)
		}
	})

	t.Run("prune must never delete a binary the server would not serve", func(t *testing.T) {
		d := planPush(
			remoteOf(RemoteEntry{Path: "assets/og.png", Etag: "1"}),
			localOf(),
			stateOf(map[string]FileState{"assets/og.png": {Etag: "1", Binary: true}}),
			nil, false, true)

		if len(d.Delete) != 0 {
			t.Fatalf("delete=%v, want none — pruning an unpullable binary destroys it", d.Delete)
		}
	})

	t.Run("prune leaves untracked remote files alone", func(t *testing.T) {
		d := planPush(
			remoteOf(RemoteEntry{Path: "theirs.css", Etag: "1"}),
			localOf(), stateOf(nil), nil, false, true)

		if len(d.Delete) != 0 {
			t.Errorf("delete=%v, want none", d.Delete)
		}
	})
}

func TestSyncStateWithFileIsImmutable(t *testing.T) {
	orig := stateOf(map[string]FileState{"a": {Etag: "1"}})
	next := orig.withFile("b", FileState{Etag: "2"})

	if _, leaked := orig.Files["b"]; leaked {
		t.Error("withFile mutated the receiver")
	}
	if next.Files["a"].Etag != "1" || next.Files["b"].Etag != "2" {
		t.Errorf("copy = %+v, want both entries carried over", next.Files)
	}
}

// TestAProvenByteMatchIsVerifiedNotAConflict is C7's positive fixture: an
// untracked path, present on disk, in the listing, whose baseline etag
// matches the listing and whose baseline sha matches the local file. Without
// a baseline this exact shape is "untracked local collision is a conflict"
// (see TestPlanPull / TestPlanPush above). With a matching baseline it must
// be Verified instead, and Conflicts must be empty.
func TestAProvenByteMatchIsVerifiedNotAConflict(t *testing.T) {
	t.Run("pull", func(t *testing.T) {
		d := planPull(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "e1", Size: 3}),
			localOf(localFile{Path: "a.css", SHA: "sha1", Size: 3}),
			stateOf(nil),
			map[string]BaselineEntry{"a.css": {Etag: "e1", SHA: "sha1", Size: 3}},
			false, false)

		if len(d.Conflicts) != 0 || d.Verified != 1 {
			t.Errorf("conflicts=%v verified=%d, want none and 1", d.Conflicts, d.Verified)
		}
	})

	t.Run("push", func(t *testing.T) {
		d := planPush(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "e1", Size: 3}),
			localOf(localFile{Path: "a.css", SHA: "sha1", Size: 3}),
			stateOf(nil),
			map[string]BaselineEntry{"a.css": {Etag: "e1", SHA: "sha1", Size: 3}},
			false, false)

		if len(d.Conflicts) != 0 || d.Verified != 1 {
			t.Errorf("conflicts=%v verified=%d, want none and 1", d.Conflicts, d.Verified)
		}
	})
}

// TestStaleBaselineIsIgnored: the listing moved since the baseline was
// recorded (etags disagree). Trusting the recorded sha as current would be a
// silent correctness regression strictly worse than today's over-cautious
// false conflict, so a stale entry must be treated as if it did not exist —
// it lands in Unverified, not Conflicts: dsx never re-checked the new
// revision, so it must not claim the bytes differ.
func TestStaleBaselineIsIgnored(t *testing.T) {
	t.Run("pull", func(t *testing.T) {
		d := planPull(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "e2", Size: 3}),
			localOf(localFile{Path: "a.css", SHA: "sha1", Size: 3}),
			stateOf(nil),
			map[string]BaselineEntry{"a.css": {Etag: "e1", SHA: "sha1", Size: 3}},
			false, false)

		if d.Verified != 0 || len(d.Conflicts) != 0 || !slices.Equal(d.Unverified, []string{"a.css"}) {
			t.Errorf("verified=%d conflicts=%v unverified=%v, want 0, none and [a.css]", d.Verified, d.Conflicts, d.Unverified)
		}
	})

	t.Run("push", func(t *testing.T) {
		d := planPush(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "e2", Size: 3}),
			localOf(localFile{Path: "a.css", SHA: "sha1", Size: 3}),
			stateOf(nil),
			map[string]BaselineEntry{"a.css": {Etag: "e1", SHA: "sha1", Size: 3}},
			false, false)

		if d.Verified != 0 || len(d.Conflicts) != 0 || !slices.Equal(d.Unverified, []string{"a.css"}) {
			t.Errorf("verified=%d conflicts=%v unverified=%v, want 0, none and [a.css]", d.Verified, d.Conflicts, d.Unverified)
		}
	})
}

// TestEmptyEtagNeverProves isolates b.SHA != "" — the one conjunct in the
// "empty halves never prove" group a fixture can actually falsify. Given the
// surrounding `b.Etag == r.Etag`, b.Etag != "" and r.Etag != "" are
// logically equivalent to each other (either both hold or neither does), so
// no fixture can tell those two apart; b.SHA != "" has no such twin and is
// the conjunct genuinely at risk of an implementation dropping it silently.
// Both subtests hold the etag halves fixed at a real, matching, non-empty
// value and vary only the sha, so a fixture failing because of the etag
// halves is impossible here. A positive control (matching etag AND sha) is
// included so this test cannot pass against a stub with the whole feature
// deleted, or against a proven := false stand-in.
func TestEmptyEtagNeverProves(t *testing.T) {
	t.Run("push: empty baseline sha never proves, even with a real matching etag", func(t *testing.T) {
		d := planPush(
			remoteOf(RemoteEntry{Path: "x.css", Etag: "e1"}),
			localOf(localFile{Path: "x.css", SHA: ""}),
			stateOf(nil),
			map[string]BaselineEntry{"x.css": {Etag: "e1", SHA: ""}},
			false, false)

		if d.Verified != 0 {
			t.Errorf("verified=%d, want 0 — an empty baseline sha must never prove a byte match", d.Verified)
		}
		if len(d.Conflicts) != 0 || !slices.Equal(d.Unverified, []string{"x.css"}) {
			t.Errorf("conflicts=%v unverified=%v, want none and [x.css] — untracked and on the server, so it must still be held back, just not as a claimed conflict", d.Conflicts, d.Unverified)
		}
	})

	t.Run("push: positive control — matching etag and sha is proven", func(t *testing.T) {
		d := planPush(
			remoteOf(RemoteEntry{Path: "x.css", Etag: "e1"}),
			localOf(localFile{Path: "x.css", SHA: "sha-x"}),
			stateOf(nil),
			map[string]BaselineEntry{"x.css": {Etag: "e1", SHA: "sha-x"}},
			false, false)

		if d.Verified != 1 || len(d.Conflicts) != 0 {
			t.Errorf("verified=%d conflicts=%v, want 1 and none", d.Verified, d.Conflicts)
		}
	})

	t.Run("pull: empty baseline sha never proves, even with a real matching etag", func(t *testing.T) {
		d := planPull(
			remoteOf(RemoteEntry{Path: "x.css", Etag: "e1", Size: 3}),
			localOf(localFile{Path: "x.css", SHA: "", Size: 3}),
			stateOf(nil),
			map[string]BaselineEntry{"x.css": {Etag: "e1", SHA: "", Size: 3}},
			false, false)

		if d.Verified != 0 {
			t.Errorf("verified=%d, want 0 — an empty baseline sha must never prove a byte match", d.Verified)
		}
		if len(d.Conflicts) != 0 || !slices.Equal(d.Unverified, []string{"x.css"}) {
			t.Errorf("conflicts=%v unverified=%v, want none and [x.css] — present and untracked, so it must still be held back, just not as a claimed conflict", d.Conflicts, d.Unverified)
		}
	})

	t.Run("pull: positive control — matching etag and sha is proven", func(t *testing.T) {
		d := planPull(
			remoteOf(RemoteEntry{Path: "x.css", Etag: "e1", Size: 3}),
			localOf(localFile{Path: "x.css", SHA: "sha-x", Size: 3}),
			stateOf(nil),
			map[string]BaselineEntry{"x.css": {Etag: "e1", SHA: "sha-x", Size: 3}},
			false, false)

		if d.Verified != 1 || len(d.Conflicts) != 0 {
			t.Errorf("verified=%d conflicts=%v, want 1 and none", d.Verified, d.Conflicts)
		}
	})
}

// TestBaselineNeverOverridesARealLedgerEntry: invariant 2's both-sides-changed
// case (TestPlanPullBothSidesChangedIsAConflict) must stay a conflict even
// when a fabricated baseline entry would otherwise "prove" the same etag and
// sha. A real ledger entry always wins — !tracked is not belt-and-braces.
func TestBaselineNeverOverridesARealLedgerEntry(t *testing.T) {
	t.Run("pull", func(t *testing.T) {
		d := planPull(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "2", Size: 3}),
			localOf(localFile{Path: "a.css", SHA: "edited", Size: 3}),
			stateOf(map[string]FileState{"a.css": {Etag: "1", SHA: "sha1"}}),
			map[string]BaselineEntry{"a.css": {Etag: "2", SHA: "edited", Size: 3}},
			false, false)

		if d.Verified != 0 || !slices.Equal(d.Conflicts, []string{"a.css"}) {
			t.Errorf("verified=%d conflicts=%v, want 0 and [a.css] — a tracked path must ignore the baseline", d.Verified, d.Conflicts)
		}
	})

	t.Run("push", func(t *testing.T) {
		d := planPush(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "2", Size: 3}),
			localOf(localFile{Path: "a.css", SHA: "edited", Size: 3}),
			stateOf(map[string]FileState{"a.css": {Etag: "1", SHA: "sha1"}}),
			map[string]BaselineEntry{"a.css": {Etag: "2", SHA: "edited", Size: 3}},
			false, false)

		if d.Verified != 0 || !slices.Equal(d.Conflicts, []string{"a.css"}) {
			t.Errorf("verified=%d conflicts=%v, want 0 and [a.css] — a tracked path must ignore the baseline", d.Verified, d.Conflicts)
		}
	})
}

// TestBaselineOnlyEverMovesAPathFromConflictsToVerified is the whole safety
// envelope in one sentence: a property test over generated four-way inputs
// (local x remote x ledger x baseline) asserting Delete, PruneConflicts and
// Irregular are byte-identical with and without the baseline, and that Fetch
// and Write only ever shrink, never grow — proven never touches a tracked
// path (Delete/PruneConflicts require tracked, so those slices cannot move at
// all), and it can only remove a path from Fetch/Write, never add one.
//
// Fetch/Write are not asserted byte-identical: under --force a proven path is
// deliberately skipped rather than re-fetched or re-pushed (an accepted cost,
// §7 of DESIGN-dsxdir.md) — without --force the same path would already have
// been a Conflict, never a Fetch/Write, so the shrink-only property is the
// precise claim in both cases. It is not sufficient alone — it stays green
// if the feature is deleted — which is why the positive fixture tests above
// are required alongside it.
func TestBaselineOnlyEverMovesAPathFromConflictsToVerified(t *testing.T) {
	etags := []string{"", "e1", "e2"}
	shas := []string{"", "s1", "s2"}
	rng := rand.New(rand.NewPCG(7, 13))

	const numPaths = 12
	const trials = 40

	for trial := range trials {
		remote := map[string]RemoteEntry{}
		local := map[string]localFile{}
		files := map[string]FileState{}
		baseline := map[string]BaselineEntry{}

		for i := range numPaths {
			p := fmt.Sprintf("p%02d.css", i)

			if rng.IntN(2) == 0 {
				remote[p] = RemoteEntry{Path: p, Type: "file", Etag: etags[rng.IntN(len(etags))], Size: 1}
			}

			switch rng.IntN(3) {
			case 0:
				// stays absent locally
			case 1:
				local[p] = localFile{Path: p, Irregular: true}
			default:
				local[p] = localFile{Path: p, SHA: shas[rng.IntN(len(shas))], Size: 1}
			}

			if rng.IntN(2) == 0 {
				files[p] = FileState{
					Etag:   etags[rng.IntN(len(etags))],
					SHA:    shas[rng.IntN(len(shas))],
					Binary: rng.IntN(8) == 0,
				}
			}

			if rng.IntN(2) == 0 {
				baseline[p] = BaselineEntry{
					Etag: etags[rng.IntN(len(etags))],
					SHA:  shas[rng.IntN(len(shas))],
					Size: 1,
				}
			}
		}

		st := State{ProjectID: "p", Files: files}

		for _, force := range []bool{false, true} {
			for _, prune := range []bool{false, true} {
				name := fmt.Sprintf("trial=%d/force=%v/prune=%v", trial, force, prune)

				t.Run(name+"/pull", func(t *testing.T) {
					without := planPull(remote, local, st, map[string]BaselineEntry{}, force, prune)
					with := planPull(remote, local, st, baseline, force, prune)

					if !isSubsetOf(with.Fetch, without.Fetch) {
						t.Errorf("Fetch grew: without=%v with=%v", without.Fetch, with.Fetch)
					}
					if !slices.Equal(without.Delete, with.Delete) {
						t.Errorf("Delete changed: without=%v with=%v", without.Delete, with.Delete)
					}
					if !slices.Equal(without.PruneConflicts, with.PruneConflicts) {
						t.Errorf("PruneConflicts changed: without=%v with=%v", without.PruneConflicts, with.PruneConflicts)
					}
					if !slices.Equal(without.Irregular, with.Irregular) {
						t.Errorf("Irregular changed: without=%v with=%v", without.Irregular, with.Irregular)
					}
				})

				t.Run(name+"/push", func(t *testing.T) {
					without := planPush(remote, local, st, map[string]BaselineEntry{}, force, prune)
					with := planPush(remote, local, st, baseline, force, prune)

					withoutWrite := writtenPaths(without)
					withWrite := writtenPaths(with)
					if !isSubsetOf(withWrite, withoutWrite) {
						t.Errorf("Write grew: without=%v with=%v", withoutWrite, withWrite)
					}
					if !slices.Equal(without.Delete, with.Delete) {
						t.Errorf("Delete changed: without=%v with=%v", without.Delete, with.Delete)
					}
					if !slices.Equal(without.PruneConflicts, with.PruneConflicts) {
						t.Errorf("PruneConflicts changed: without=%v with=%v", without.PruneConflicts, with.PruneConflicts)
					}
					if !slices.Equal(without.Irregular, with.Irregular) {
						t.Errorf("Irregular changed: without=%v with=%v", without.Irregular, with.Irregular)
					}
				})
			}
		}
	}
}

func writtenPaths(d pushDecision) []string {
	out := make([]string, 0, len(d.Write))
	for _, w := range d.Write {
		out = append(out, w.Path)
	}
	return out
}

// isSubsetOf reports whether every element of sub appears in all.
func isSubsetOf(sub, all []string) bool {
	for _, p := range sub {
		if !slices.Contains(all, p) {
			return false
		}
	}
	return true
}
