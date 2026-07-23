package syncer

import (
	"slices"
	"testing"
)

// The `!force` on the diverged and staleProof arms is what makes --force act
// at all: without it the path stays a conflict and --force silently stops
// overwriting. Each subtest pairs the forced case with its unforced twin, so
// deleting the whole arm fails the pair rather than passing half of it.
func TestForceFallsThroughTheBaselineConflictArms(t *testing.T) {
	const (
		remoteEtag = "e2"
		baseEtag   = "e1"
		diskSHA    = "sha-local"
		baseSHA    = "sha-base"
	)

	t.Run("pull: diverged", func(t *testing.T) {
		remote := remoteOf(RemoteEntry{Path: "x.css", Etag: remoteEtag, Size: 3})
		local := localOf(localFile{Path: "x.css", SHA: diskSHA, Size: 3})
		base := map[string]BaselineEntry{"x.css": {Etag: remoteEtag, SHA: baseSHA, Size: 3}}

		held := planPull(remote, local, stateOf(nil), base, false, false, false)
		if !slices.Equal(held.Diverged, []string{"x.css"}) {
			t.Fatalf("unforced Diverged = %v, want [x.css]; the fixture never reached the arm", held.Diverged)
		}
		if len(held.Fetch) != 0 {
			t.Errorf("unforced Fetch = %v, want none", held.Fetch)
		}

		forced := planPull(remote, local, stateOf(nil), base, true, false, false)
		if len(forced.Diverged) != 0 {
			t.Errorf("forced Diverged = %v, want none — --force must not leave it a conflict", forced.Diverged)
		}
		if !slices.Equal(forced.Fetch, []string{"x.css"}) {
			t.Errorf("forced Fetch = %v, want [x.css] — --force must overwrite", forced.Fetch)
		}
	})

	t.Run("pull: staleProof", func(t *testing.T) {
		remote := remoteOf(RemoteEntry{Path: "x.css", Etag: remoteEtag, Size: 3})
		local := localOf(localFile{Path: "x.css", SHA: diskSHA, Size: 3})
		base := map[string]BaselineEntry{"x.css": {Etag: baseEtag, SHA: diskSHA, Size: 3}}

		held := planPull(remote, local, stateOf(nil), base, false, false, false)
		if !slices.Equal(held.StaleProof, []string{"x.css"}) {
			t.Fatalf("unforced StaleProof = %v, want [x.css]; the fixture never reached the arm", held.StaleProof)
		}
		if len(held.Fetch) != 0 {
			t.Errorf("unforced Fetch = %v, want none", held.Fetch)
		}

		forced := planPull(remote, local, stateOf(nil), base, true, false, false)
		if len(forced.StaleProof) != 0 {
			t.Errorf("forced StaleProof = %v, want none — --force must not leave it a conflict", forced.StaleProof)
		}
		if !slices.Equal(forced.Fetch, []string{"x.css"}) {
			t.Errorf("forced Fetch = %v, want [x.css] — --force must overwrite", forced.Fetch)
		}
	})

	t.Run("push: diverged", func(t *testing.T) {
		remote := remoteOf(RemoteEntry{Path: "x.css", Etag: remoteEtag})
		local := localOf(localFile{Path: "x.css", SHA: diskSHA})
		base := map[string]BaselineEntry{"x.css": {Etag: remoteEtag, SHA: baseSHA}}

		held := planPush(remote, local, stateOf(nil), base, nil, forceNone, false)
		if !slices.Equal(held.Diverged, []string{"x.css"}) {
			t.Fatalf("unforced Diverged = %v, want [x.css]; the fixture never reached the arm", held.Diverged)
		}
		if len(held.Write) != 0 {
			t.Errorf("unforced Writes = %v, want none", writtenPaths(held))
		}

		forced := planPush(remote, local, stateOf(nil), base, nil, forceBlind, false)
		if len(forced.Diverged) != 0 {
			t.Errorf("forced Diverged = %v, want none — --force must not leave it a conflict", forced.Diverged)
		}
		if !slices.Equal(writtenPaths(forced), []string{"x.css"}) {
			t.Errorf("forced Writes = %v, want [x.css] — --force must overwrite", writtenPaths(forced))
		}
	})

	t.Run("push: staleProof", func(t *testing.T) {
		remote := remoteOf(RemoteEntry{Path: "x.css", Etag: remoteEtag})
		local := localOf(localFile{Path: "x.css", SHA: diskSHA})
		base := map[string]BaselineEntry{"x.css": {Etag: baseEtag, SHA: diskSHA}}

		held := planPush(remote, local, stateOf(nil), base, nil, forceNone, false)
		if !slices.Equal(held.StaleProof, []string{"x.css"}) {
			t.Fatalf("unforced StaleProof = %v, want [x.css]; the fixture never reached the arm", held.StaleProof)
		}
		if len(held.Write) != 0 {
			t.Errorf("unforced Writes = %v, want none", writtenPaths(held))
		}

		forced := planPush(remote, local, stateOf(nil), base, nil, forceBlind, false)
		if len(forced.StaleProof) != 0 {
			t.Errorf("forced StaleProof = %v, want none — --force must not leave it a conflict", forced.StaleProof)
		}
		if !slices.Equal(writtenPaths(forced), []string{"x.css"}) {
			t.Errorf("forced Writes = %v, want [x.css] — --force must overwrite", writtenPaths(forced))
		}
	})
}

// staleProof carries `b.Etag != r.Etag`, which breaks the equivalence
// TestEmptyEtagNeverProves records for proven: under `b.Etag == r.Etag` the two
// empty-etag halves either both hold or neither does, so no fixture can tell
// them apart, but under `!=` each is independently falsifiable. Both are
// guarded here, each with the same fixture filled in as its positive control.
func TestEmptyEtagNeverMakesAStaleProof(t *testing.T) {
	t.Run("pull: an empty baseline etag is not a stale proof", func(t *testing.T) {
		d := planPull(
			remoteOf(RemoteEntry{Path: "x.css", Etag: "e2", Size: 3}),
			localOf(localFile{Path: "x.css", SHA: "sha-x", Size: 3}),
			stateOf(nil),
			map[string]BaselineEntry{"x.css": {Etag: "", SHA: "sha-x", Size: 3}},
			false, false, false)

		if len(d.StaleProof) != 0 {
			t.Errorf("StaleProof = %v, want none — nothing was ever proved against a revision", d.StaleProof)
		}
		if !slices.Equal(d.Unverified, []string{"x.css"}) {
			t.Errorf("Unverified = %v, want [x.css]", d.Unverified)
		}
	})

	t.Run("pull: positive control — a real baseline etag is a stale proof", func(t *testing.T) {
		d := planPull(
			remoteOf(RemoteEntry{Path: "x.css", Etag: "e2", Size: 3}),
			localOf(localFile{Path: "x.css", SHA: "sha-x", Size: 3}),
			stateOf(nil),
			map[string]BaselineEntry{"x.css": {Etag: "e1", SHA: "sha-x", Size: 3}},
			false, false, false)

		if !slices.Equal(d.StaleProof, []string{"x.css"}) {
			t.Errorf("StaleProof = %v, want [x.css]", d.StaleProof)
		}
		if len(d.Unverified) != 0 {
			t.Errorf("Unverified = %v, want none", d.Unverified)
		}
	})

	t.Run("push: an empty listing etag is not a stale proof", func(t *testing.T) {
		d := planPush(
			remoteOf(),
			localOf(localFile{Path: "x.css", SHA: "sha-x"}),
			stateOf(nil),
			map[string]BaselineEntry{"x.css": {Etag: "e1", SHA: "sha-x"}}, nil, forceNone, false)

		if len(d.StaleProof) != 0 {
			t.Errorf("StaleProof = %v, want none — a path the server does not have is an upload, not a conflict", d.StaleProof)
		}
		if !slices.Equal(writtenPaths(d), []string{"x.css"}) {
			t.Errorf("Writes = %v, want [x.css]", writtenPaths(d))
		}
	})

	t.Run("push: positive control — a real listing etag is a stale proof", func(t *testing.T) {
		d := planPush(
			remoteOf(RemoteEntry{Path: "x.css", Etag: "e2"}),
			localOf(localFile{Path: "x.css", SHA: "sha-x"}),
			stateOf(nil),
			map[string]BaselineEntry{"x.css": {Etag: "e1", SHA: "sha-x"}}, nil, forceNone, false)

		if !slices.Equal(d.StaleProof, []string{"x.css"}) {
			t.Errorf("StaleProof = %v, want [x.css]", d.StaleProof)
		}
		if len(writtenPaths(d)) != 0 {
			t.Errorf("Writes = %v, want none", writtenPaths(d))
		}
	})
}
