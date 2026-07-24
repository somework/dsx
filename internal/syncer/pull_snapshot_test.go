package syncer

import (
	"context"
	"os"
	"slices"
	"testing"
)

// snapshotFake answers one listing and serves every path in it.
func snapshotFake(t *testing.T, bodies map[string]string, etags map[string]string) *fakeMCP {
	t.Helper()
	var paths []string
	for p := range bodies {
		paths = append(paths, p)
	}
	slices.Sort(paths)

	var entries []RemoteEntry
	for _, p := range paths {
		entries = append(entries, fileEntry(p, etags[p], int64(len(bodies[p]))))
	}
	return newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(entries...)}
		case "read_file":
			p, _ := args["path"].(string)
			body, ok := bodies[p]
			if !ok {
				return fakeReply{Text: "unexpected path " + p, IsError: true}
			}
			return fakeReply{Text: envelopeFor(p, etags[p], body)}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})
}

// TestPullRecordsTheListingSnapshot closes the seam this was written for: a
// pull walks the whole tree and then threw the walk away, so `status` — which
// answers from the snapshot alone (invariant 19) — refused immediately after
// a clone that had just downloaded every byte. The listing is already in
// hand after survey, so recording it costs no request.
func TestPullRecordsTheListingSnapshot(t *testing.T) {
	dir := t.TempDir()
	bodies := map[string]string{"a.css": "a{}\n", "sub/b.css": "b{}\n"}
	etags := map[string]string{"a.css": "e1", "sub/b.css": "e2"}

	if _, err := Pull(context.Background(), fakeClient(snapshotFake(t, bodies, etags)), PullOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 2,
	}); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	bl, err := loadBaseline(dir)
	if err != nil {
		t.Fatal(err)
	}
	if bl.Listing == nil {
		t.Fatal("pull recorded no listing; `status` right after a pull still has nothing to answer from")
	}
	for path, body := range bodies {
		got, ok := bl.Listing[path]
		if !ok {
			t.Fatalf("listing has no entry for %s", path)
		}
		if got.Etag != etags[path] || got.Size != int64(len(body)) {
			t.Errorf("listing[%s] = %+v, want etag %q size %d", path, got, etags[path], len(body))
		}
	}
	if bl.ProjectID != "proj-A" {
		t.Errorf("baseline project = %q, want proj-A", bl.ProjectID)
	}

	// The seam itself, asserted end to end rather than by inspecting bytes.
	if _, err := Status(StatusOpts{ProjectID: "proj-A", Dir: dir}); err != nil {
		t.Fatalf("status refused after a pull that walked the whole tree: %v", err)
	}
}

// TestPullDoesNotTouchTheProof is the narrow half of the change. Verified is
// a claim about BYTES dsx downloaded and compared; pull records only the
// listing beside it. Adding pull's own paths there would be dead weight by
// construction — `proven` requires !tracked and pull tracks everything it
// writes — and rewriting the map with anything else would put a proof under
// a run that never verified one.
func TestPullDoesNotTouchTheProof(t *testing.T) {
	dir := t.TempDir()
	body := "kept\n"
	mkfile(t, dir, "kept.css", body)

	bodies := map[string]string{"kept.css": body, "new.css": "n{}\n"}
	etags := map[string]string{"kept.css": "eKept", "new.css": "eNew"}
	c := fakeClient(snapshotFake(t, bodies, etags))

	proof := BaselineEntry{Etag: "eKept", Size: int64(len(body)), SHA: SHA256Hex([]byte(body))}
	prior := Baseline{
		ProjectID: "proj-A",
		Endpoint:  c.Endpoint(),
		Verified:  map[string]BaselineEntry{"kept.css": proof},
		Listing:   map[string]SnapshotEntry{"kept.css": {Size: int64(len(body)), Etag: "eKept"}},
	}
	if err := prior.save(dir); err != nil {
		t.Fatal(err)
	}

	rep, err := Pull(context.Background(), c, PullOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 2,
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	// Without this the whole test passes on the first-contact escape, which
	// writes nothing at all and leaves the prior baseline in place.
	if !slices.Contains(rep.Fetched, "new.css") || rep.Verified != 1 {
		t.Fatalf("the pull did not reach its act: Fetched=%v Verified=%d Conflicts=%v",
			rep.Fetched, rep.Verified, rep.Conflicts)
	}

	bl, err := loadBaseline(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := bl.Verified["kept.css"]; got != proof {
		t.Errorf("Verified[kept.css] = %+v, want the fetch's own proof %+v", got, proof)
	}
	if len(bl.Verified) != 1 {
		t.Errorf("Verified = %+v, want only the entry the fetch put there", bl.Verified)
	}
	if _, ok := bl.Listing["new.css"]; !ok {
		t.Error("the listing pull recorded does not name new.css")
	}
}

// TestAPullRefreshesABoundSnapshotSoStatusDoesNotCryServerAhead is the case
// every other test here misses: they all start from no baseline at all, so
// they can only show that `status` stops REFUSING. The interesting half is a
// snapshot that already exists and is stale — the ledger moves to the new
// etag when the pull lands, and if the snapshot does not move with it,
// `classifyStatus` compares one map that updated against one that did not and
// reports `server ahead` for a file that was just pulled to the server's
// current revision. That was dsx's unconditional behaviour before this
// feature; a review found it reachable again through a failed snapshot write,
// which is what makes the positive control worth pinning.
func TestAPullRefreshesABoundSnapshotSoStatusDoesNotCryServerAhead(t *testing.T) {
	dir := t.TempDir()
	oldBody, newBody := "old{}\n", "new{}\n"
	mkfile(t, dir, "a.css", oldBody)

	bodies := map[string]string{"a.css": newBody}
	etags := map[string]string{"a.css": "e-NEW"}
	c := fakeClient(snapshotFake(t, bodies, etags))

	st := State{ProjectID: "proj-A", Endpoint: c.Endpoint(), Files: map[string]FileState{
		"a.css": {Etag: "e-OLD", Size: int64(len(oldBody)), SHA: SHA256Hex([]byte(oldBody))},
	}}
	if err := st.save(dir); err != nil {
		t.Fatal(err)
	}
	prior := Baseline{
		ProjectID: "proj-A",
		Endpoint:  c.Endpoint(),
		Verified:  map[string]BaselineEntry{},
		Listing:   map[string]SnapshotEntry{"a.css": {Size: int64(len(oldBody)), Etag: "e-OLD"}},
	}
	if err := prior.save(dir); err != nil {
		t.Fatal(err)
	}

	rep, err := Pull(context.Background(), c, PullOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 2})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !slices.Contains(rep.Fetched, "a.css") {
		t.Fatalf("the fixture no longer fetches: %+v", rep)
	}

	got, err := Status(StatusOpts{ProjectID: "proj-A", Dir: dir})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(got.ServerAhead) != 0 {
		t.Errorf("status says the server is ahead of %v, which this pull just fetched to the "+
			"revision the listing showed", got.ServerAhead)
	}
	if len(got.Modified) != 0 || len(got.GoneFromServer) != 0 || len(got.Untracked) != 0 {
		t.Errorf("status = %+v, want clean after a pull that reconciled everything", got)
	}
}

// TestADryRunRecordsNoSnapshot: -n changes nothing on disk, and .dsx is disk.
// A snapshot left by a preview would silently arm a lease and answer a later
// `status` from a run the caller asked not to happen.
func TestADryRunRecordsNoSnapshot(t *testing.T) {
	dir := t.TempDir()
	bodies := map[string]string{"a.css": "a{}\n"}
	etags := map[string]string{"a.css": "e1"}

	if _, err := Pull(context.Background(), fakeClient(snapshotFake(t, bodies, etags)), PullOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 2, DryRun: true,
	}); err != nil {
		t.Fatalf("Pull -n: %v", err)
	}

	if _, err := os.Stat(BaselinePath(dir)); !os.IsNotExist(err) {
		t.Fatalf("pull -n wrote %s (stat err %v)", BaselinePath(dir), err)
	}
}

// TestAFirstContactRefusalRecordsNoSnapshot: a refusal leaves no trace and
// lands before the act it refuses (invariant 16). First contact into a
// populated directory that disagrees writes nothing at all — the snapshot is
// part of "nothing".
func TestAFirstContactRefusalRecordsNoSnapshot(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "a.css", "MINE\n")

	bodies := map[string]string{"a.css": "THEIRS\n"}
	etags := map[string]string{"a.css": "e1"}

	rep, err := Pull(context.Background(), fakeClient(snapshotFake(t, bodies, etags)), PullOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 2,
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(rep.Conflicts) == 0 {
		t.Fatal("fixture no longer conflicts on first contact")
	}
	if _, err := os.Stat(BaselinePath(dir)); !os.IsNotExist(err) {
		t.Fatalf("a first-contact refusal wrote %s (stat err %v)", BaselinePath(dir), err)
	}
}

// TestAPullThatFailedPartwayStillRecordedTheListing: the listing is an
// observation, and a download failing later does not make it less true — so a
// run that got halfway still leaves `status` able to answer instead of
// refusing. What this holds is that the write is above the error RETURNS, not
// that it is on the exact line it is on: moving it down to just after the
// download fan-out was tried as a mutation and passed everything here, which
// is honest — the two placements differ only in cases nothing here reaches.
// Moving it below `return rep, errs[0]` is what this catches.
func TestAPullThatFailedPartwayStillRecordedTheListing(t *testing.T) {
	dir := t.TempDir()
	good, bad := "good.css", "bad.css"

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(
				fileEntry(good, "e1", 4), fileEntry(bad, "e2", 4))}
		case "read_file":
			p, _ := args["path"].(string)
			if p == bad {
				return fakeReply{Text: "the server refused this one", IsError: true}
			}
			return fakeReply{Text: envelopeFor(p, "e1", "a{}\n")}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	if _, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 1,
	}); err == nil {
		t.Fatal("the fixture no longer fails the pull")
	}

	bl, err := loadBaseline(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, named := bl.Listing[bad]; !named {
		t.Errorf("Listing = %+v, want both paths — the walk succeeded, only a download failed", bl.Listing)
	}
	if _, err := Status(StatusOpts{ProjectID: "proj-A", Dir: dir}); err != nil {
		t.Fatalf("status refused after a pull that walked the whole tree and failed halfway: %v", err)
	}
}

// TestPullDiscardsAForeignProofWhenItRecordsTheListing: an unbound baseline's
// Verified is already discarded for planning. Re-saving it under this run's
// project id would turn a discarded cache into a proof about a server it was
// never gathered from.
func TestPullDiscardsAForeignProofWhenItRecordsTheListing(t *testing.T) {
	dir := t.TempDir()
	foreign := Baseline{
		ProjectID: "proj-OTHER",
		Verified:  map[string]BaselineEntry{"a.css": {Etag: "eForeign", Size: 4, SHA: "deadbeef"}},
		Listing:   map[string]SnapshotEntry{"a.css": {Size: 4, Etag: "eForeign"}},
	}
	if err := foreign.save(dir); err != nil {
		t.Fatal(err)
	}

	bodies := map[string]string{"a.css": "a{}\n"}
	etags := map[string]string{"a.css": "e1"}
	if _, err := Pull(context.Background(), fakeClient(snapshotFake(t, bodies, etags)), PullOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 2,
	}); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	bl, err := loadBaseline(dir)
	if err != nil {
		t.Fatal(err)
	}
	if bl.ProjectID != "proj-A" {
		t.Errorf("baseline project = %q, want proj-A", bl.ProjectID)
	}
	if len(bl.Verified) != 0 {
		t.Errorf("Verified = %+v, want empty — proj-OTHER's proof says nothing about proj-A", bl.Verified)
	}
	if got := bl.Listing["a.css"].Etag; got != "e1" {
		t.Errorf("listing[a.css].Etag = %q, want e1", got)
	}
}

// TestASnapshotThatCannotBeRecordedDoesNotBlockThePull is the write-side half
// of TestACorruptBaselineDoesNotBlockASync. The same hand-made damage — a
// directory where baseline.json belongs — must cost a pull nothing, because
// the baseline is a cache and pull's product is bytes. The state left behind
// is the one that existed before pull recorded anything, and `status` refuses
// out loud from it.
func TestASnapshotThatCannotBeRecordedDoesNotBlockThePull(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(BaselinePath(dir), 0o755); err != nil {
		t.Fatal(err)
	}

	bodies := map[string]string{"a.css": "a{}\n"}
	etags := map[string]string{"a.css": "e1"}

	rep, err := Pull(context.Background(), fakeClient(snapshotFake(t, bodies, etags)), PullOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 2,
	})
	if err != nil {
		t.Fatalf("Pull failed because it could not record its snapshot: %v", err)
	}
	if !slices.Contains(rep.Fetched, "a.css") {
		t.Errorf("Fetched = %v, want a.css — the bytes are the product", rep.Fetched)
	}
	got, rerr := os.ReadFile(dir + "/a.css")
	if rerr != nil || string(got) != bodies["a.css"] {
		t.Errorf("a.css on disk = %q (err %v), want %q", got, rerr, bodies["a.css"])
	}
}

// TestThePullSnapshotIsRecordedAlreadyFiltered mirrors fetch's rule and for
// the same reason: no reader can filter a snapshot (filterRemote takes
// RemoteEntry, and only survey may touch the ignore machinery — invariant 9),
// so an ignored path recorded once is named forever.
func TestThePullSnapshotIsRecordedAlreadyFiltered(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, ".dsxignore", "ignored.css\n")

	bodies := map[string]string{"a.css": "a{}\n", "ignored.css": "i{}\n"}
	etags := map[string]string{"a.css": "e1", "ignored.css": "e2"}

	if _, err := Pull(context.Background(), fakeClient(snapshotFake(t, bodies, etags)), PullOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 2,
	}); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	bl, err := loadBaseline(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, named := bl.Listing["ignored.css"]; named {
		t.Error("the snapshot names a path .dsxignore excludes from both sides")
	}
	if _, named := bl.Listing["a.css"]; !named {
		t.Error("the snapshot dropped a path that is not ignored")
	}
}
