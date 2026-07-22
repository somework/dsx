package syncer

import (
	"slices"
	"testing"
)

func leaseFixture(serverEtag, snapshotEtag string) (map[string]RemoteEntry, map[string]localFile, State, map[string]SnapshotEntry) {
	remote := map[string]RemoteEntry{"a.css": {Path: "a.css", Size: 3, Etag: serverEtag}}
	local := map[string]localFile{"a.css": onDisk("a.css", "MINE\n")}
	st := State{ProjectID: "p", Files: map[string]FileState{}}
	snap := map[string]SnapshotEntry{"a.css": {Size: 3, Etag: snapshotEtag}}
	return remote, local, st, snap
}

// TestALeaseWritesWithThePreconditionItLeasedAgainst is the difference from a
// blind --force in one line: the write still carries an etag, so the server
// rejects it if anything landed between the listing and the write. A blind
// force sends no precondition at all and cannot be rejected.
func TestALeaseWritesWithThePreconditionItLeasedAgainst(t *testing.T) {
	remote, local, st, snap := leaseFixture("e1", "e1")

	d := planPush(remote, local, st, map[string]BaselineEntry{}, snap, forceLease, false)

	if len(d.Write) != 1 {
		t.Fatalf("Write = %+v, want the one path", d.Write)
	}
	if got := d.Write[0].IfMatch; got != "e1" {
		t.Errorf("IfMatch = %q, want %q — a lease writes against what the last fetch saw", got, "e1")
	}
	if len(d.LeaseBroken) != 0 {
		t.Errorf("LeaseBroken = %v, want empty", d.LeaseBroken)
	}
}

// TestABrokenLeaseRefusesInsteadOfOverwriting is the whole point. Someone
// wrote to the path after our fetch, so the etag the snapshot remembers is no
// longer the one the server shows — exactly the third-party change a blind
// --force destroys without a word.
func TestABrokenLeaseRefusesInsteadOfOverwriting(t *testing.T) {
	remote, local, st, snap := leaseFixture("e2-THEIRS", "e1")

	d := planPush(remote, local, st, map[string]BaselineEntry{}, snap, forceLease, false)

	if !slices.Equal(d.LeaseBroken, []string{"a.css"}) {
		t.Errorf("LeaseBroken = %v, want [a.css]", d.LeaseBroken)
	}
	if len(d.Write) != 0 {
		t.Errorf("Write = %+v, want empty — the lease was not held, so nothing may be written", d.Write)
	}
}

// TestABlindForceStillWritesWhereALeaseRefuses pins the two apart. Without a
// positive control here, a lease that quietly behaved like --force would pass
// every assertion above.
func TestABlindForceStillWritesWhereALeaseRefuses(t *testing.T) {
	remote, local, st, snap := leaseFixture("e2-THEIRS", "e1")

	blind := planPush(remote, local, st, map[string]BaselineEntry{}, snap, forceBlind, false)

	if len(blind.Write) != 1 {
		t.Fatalf("--force refused a write: %+v", blind)
	}
	if got := blind.Write[0].IfMatch; got != "" {
		t.Errorf("--force sent IfMatch %q; a blind force carries no precondition, "+
			"which is the hazard --force-with-lease exists to avoid", got)
	}
	if len(blind.LeaseBroken) != 0 {
		t.Errorf("--force reported LeaseBroken = %v; it leases nothing", blind.LeaseBroken)
	}
}

// TestALeaseOnAPathNeitherSideHadAssertsItIsNew: absent at fetch time and
// absent now is a lease held — nobody took the name while we were away — and
// "0" is the assertion the server checks it against.
func TestALeaseOnAPathNeitherSideHadAssertsItIsNew(t *testing.T) {
	local := map[string]localFile{"fresh.css": onDisk("fresh.css", "x\n")}
	st := State{ProjectID: "p", Files: map[string]FileState{}}

	d := planPush(map[string]RemoteEntry{}, local, st, map[string]BaselineEntry{},
		map[string]SnapshotEntry{}, forceLease, false)

	if len(d.Write) != 1 {
		t.Fatalf("Write = %+v, want the new path", d.Write)
	}
	if got := d.Write[0].IfMatch; got != "0" {
		t.Errorf("IfMatch = %q, want \"0\" — the lease is that the name was free and still is", got)
	}
}

// TestALeaseRefusesAPathThatAppearedAfterTheFetch: absent when we looked,
// present now. Writing it with "0" would be rejected by the server anyway,
// but the refusal must name the reason rather than let the caller read a
// precondition failure as a transport fault.
func TestALeaseRefusesAPathThatAppearedAfterTheFetch(t *testing.T) {
	remote := map[string]RemoteEntry{"a.css": {Path: "a.css", Size: 3, Etag: "e-NEW"}}
	local := map[string]localFile{"a.css": onDisk("a.css", "MINE\n")}
	st := State{ProjectID: "p", Files: map[string]FileState{}}

	d := planPush(remote, local, st, map[string]BaselineEntry{},
		map[string]SnapshotEntry{}, forceLease, false)

	if !slices.Equal(d.LeaseBroken, []string{"a.css"}) {
		t.Errorf("LeaseBroken = %v, want [a.css] — the server gained this path after we looked", d.LeaseBroken)
	}
}

// TestALeaseNeverWidensWhatAPlainPushWouldWrite: forceNone is untouched by
// any of this. A lease may only ever turn a blind overwrite into a checked
// one — never a conflict into a write.
func TestALeaseNeverWidensWhatAPlainPushWouldWrite(t *testing.T) {
	remote, local, st, snap := leaseFixture("e1", "e1")

	plain := planPush(remote, local, st, map[string]BaselineEntry{}, snap, forceNone, false)

	if len(plain.Write) != 0 {
		t.Errorf("a plain push wrote %+v; an untracked path on the server is a conflict", plain.Write)
	}
	if !slices.Equal(plain.Unverified, []string{"a.css"}) {
		t.Errorf("Unverified = %v, want [a.css]", plain.Unverified)
	}
	if len(plain.LeaseBroken) != 0 {
		t.Errorf("a plain push reported LeaseBroken = %v; it leases nothing", plain.LeaseBroken)
	}
}

// pushMode adapts the bool the older plan tests carry. They predate the lease
// and assert nothing about it, so blind is the faithful reading of their
// `force`: it is the mode they were written against.
func pushMode(force bool) pushForce {
	if force {
		return forceBlind
	}
	return forceNone
}

// TestAnEmptyEtagNeverHoldsALease is the sibling of
// TestEmptyEtagNeverMakesAStaleProof, and it caught a live hole: with the
// non-empty halves dropped, a path whose snapshot and listing entries both
// carry an empty etag satisfies `snap.Etag == r.Etag` and leases. The write
// then goes out with IfMatch "" — a blind force wearing the lease's name,
// which is the one outcome --force-with-lease exists to make impossible.
func TestAnEmptyEtagNeverHoldsALease(t *testing.T) {
	for _, tc := range []struct{ name, serverEtag, snapEtag string }{
		{"both empty", "", ""},
		{"server empty", "", "e1"},
		{"snapshot empty", "e1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remote, local, st, snap := leaseFixture(tc.serverEtag, tc.snapEtag)

			d := planPush(remote, local, st, map[string]BaselineEntry{}, snap, forceLease, false)

			if len(d.Write) != 0 {
				t.Errorf("wrote %+v with IfMatch %q; an empty etag is not something to lease against",
					d.Write, d.Write[0].IfMatch)
			}
			if !slices.Equal(d.LeaseBroken, []string{"a.css"}) {
				t.Errorf("LeaseBroken = %v, want [a.css]", d.LeaseBroken)
			}
		})
	}
}
