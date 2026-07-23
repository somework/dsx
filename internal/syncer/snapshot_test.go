package syncer

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"
)

// TestFetchRecordsTheWholeListingNotOnlyWhatItVerified is the feature: an
// offline status has to answer "what does the server hold", and Verified
// cannot answer it. Verified is deliberately narrow — present on disk, not
// tracked, regular, listed — so a remote-only path and a tracked path are
// both invisible in it while being exactly what a status must report.
func TestFetchRecordsTheWholeListingNotOnlyWhatItVerified(t *testing.T) {
	dir := t.TempDir()
	untracked := "untracked.css"
	tracked := "tracked.css"
	remoteOnly := "remote-only.css"

	untrackedBody := "a{}\n"
	trackedBody := "b{}\n"
	mkfile(t, dir, untracked, untrackedBody)
	mkfile(t, dir, tracked, trackedBody)

	st := State{ProjectID: "proj-A", Files: map[string]FileState{
		tracked: {Etag: "eTracked", Size: int64(len(trackedBody)), SHA: SHA256Hex([]byte(trackedBody))},
	}}
	if err := st.save(dir); err != nil {
		t.Fatal(err)
	}

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(
				fileEntry(untracked, "eUntracked", int64(len(untrackedBody))),
				fileEntry(tracked, "eTracked", int64(len(trackedBody))),
				fileEntry(remoteOnly, "eRemote", 7))}
		}
		p, _ := args["path"].(string)
		switch p {
		case untracked:
			return fakeReply{Text: envelopeFor(p, "eUntracked", untrackedBody)}
		case tracked:
			return fakeReply{Text: envelopeFor(p, "eTracked", trackedBody)}
		case remoteOnly:
			return fakeReply{Text: envelopeFor(p, "eRemote", "1234567")}
		}
		return fakeReply{Text: "unexpected path " + p, IsError: true}
	})

	if _, err := Fetch(context.Background(), fakeClient(f), FetchOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 2,
	}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	bl, err := loadBaseline(dir)
	if err != nil {
		t.Fatalf("loadBaseline: %v", err)
	}

	for _, path := range []string{untracked, tracked, remoteOnly} {
		e, ok := bl.Listing[path]
		if !ok {
			t.Errorf("Listing is missing %q; an offline status cannot report a path the snapshot never saw", path)
			continue
		}
		if e.Etag == "" {
			t.Errorf("Listing[%q].Etag is empty; without it nothing can tell a stale snapshot from a fresh one", path)
		}
	}
	if e := bl.Listing[remoteOnly]; e.Size != 7 {
		t.Errorf("Listing[%q].Size = %d, want 7", remoteOnly, e.Size)
	}

	// The narrow set is unchanged: only the untracked, present path is proof.
	verified := make([]string, 0, len(bl.Verified))
	for p := range bl.Verified {
		verified = append(verified, p)
	}
	slices.Sort(verified)
	if !slices.Equal(verified, []string{untracked}) {
		t.Errorf("Verified = %v, want only [%s] — the snapshot must widen the listing, never the proof",
			verified, untracked)
	}
}

// TestAMissingListingIsNotAnEmptyOne is the trap. A baseline.json written
// before the field exists decodes with no listing at all, and that must not
// read as "the server holds nothing" — the first is "nobody ever asked", the
// second is a claim. loadBaseline's nil-map fixup exists for Verified and
// must not be extended here, or every pre-existing baseline on disk starts
// asserting an empty server.
func TestAMissingListingIsNotAnEmptyOne(t *testing.T) {
	write := func(t *testing.T, body string) Baseline {
		t.Helper()
		dir := t.TempDir()
		if err := os.MkdirAll(StateDir(dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(BaselinePath(dir), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		bl, err := loadBaseline(dir)
		if err != nil {
			t.Fatalf("loadBaseline: %v", err)
		}
		return bl
	}

	legacy := write(t, `{"project_id":"p","verified":{}}`)
	if legacy.Listing != nil {
		t.Errorf("a baseline with no listing key decoded to %#v, want nil — "+
			"an absent snapshot must stay distinguishable from an empty server", legacy.Listing)
	}

	empty := write(t, `{"project_id":"p","verified":{},"listing":{}}`)
	if empty.Listing == nil {
		t.Error("a baseline holding an empty listing decoded to nil; " +
			"a server that genuinely holds nothing is a fetched fact, not an absent one")
	}
}

// TestAnEmptyListingSurvivesTheRoundTrip pins the same distinction through
// save, where encoding/json is the thing that erases it: `omitempty` drops an
// empty map exactly as it drops a nil one, so the tag must not carry it.
func TestAnEmptyListingSurvivesTheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	bl := Baseline{ProjectID: "p", Verified: map[string]BaselineEntry{}, Listing: map[string]SnapshotEntry{}}
	if err := bl.save(dir); err != nil {
		t.Fatalf("save: %v", err)
	}

	raw, err := os.ReadFile(BaselinePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	v, ok := generic["listing"]
	if !ok {
		t.Fatal(`"listing" is missing from a baseline that holds an empty one; the tag must not carry omitempty`)
	}
	if m, isMap := v.(map[string]any); !isMap || len(m) != 0 {
		t.Errorf(`"listing" = %#v, want an empty object, not null`, v)
	}
}

// TestSnapshotEntryIsADistinctType is invariant 17's guard one layer out, and
// the stakes are higher than BaselineEntry's. A snapshot is shaped like the
// live listing planPull/planPush consume, so if Listing were a
// map[string]RemoteEntry then `planPull(bl.Listing, ..., false)` would compile — and
// a stale snapshot driving --prune reads every path the server has since
// gained as a user deletion. The field's element type is what the mistake
// would land on, so that is what this reads.
func TestSnapshotEntryIsADistinctType(t *testing.T) {
	if reflect.TypeOf(SnapshotEntry{}) == reflect.TypeOf(RemoteEntry{}) {
		t.Fatal("SnapshotEntry has collapsed into RemoteEntry; a stale snapshot now compiles " +
			"where a live listing is expected, and --prune cannot tell them apart")
	}

	field, ok := reflect.TypeOf(Baseline{}).FieldByName("Listing")
	if !ok {
		t.Fatal("Baseline has no Listing field")
	}
	if field.Type.Elem() == reflect.TypeOf(RemoteEntry{}) {
		t.Fatal("Baseline.Listing holds RemoteEntry, so it can be passed straight to planPull/planPush " +
			"as if it were this run's listing")
	}
}

// TestTheSnapshotIsRecordedAlreadyFiltered is what everything downstream
// rests on. A reader cannot filter the snapshot: filterRemote takes
// RemoteEntry, SnapshotEntry is deliberately not that type, and
// TestSyncCallersCannotFilterOneSide forbids anyone but survey from calling
// the ignore machinery — so if fetch records an ignored path, nothing later
// can take it back out, and an offline report names a path .dsxignore
// excludes from both sides of every real sync (invariant 9).
func TestTheSnapshotIsRecordedAlreadyFiltered(t *testing.T) {
	dir := t.TempDir()
	kept := "keep.css"
	ignored := "vendor/skip.css"

	mkfile(t, dir, ".dsxignore", "vendor/\n")
	mkfile(t, dir, kept, "a{}\n")

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(
				fileEntry(kept, "eKeep", 4),
				fileEntry(ignored, "eSkip", 9))}
		}
		p, _ := args["path"].(string)
		if p == kept {
			return fakeReply{Text: envelopeFor(p, "eKeep", "a{}\n")}
		}
		return fakeReply{Text: "unexpected path " + p, IsError: true}
	})

	if _, err := Fetch(context.Background(), fakeClient(f), FetchOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 2,
	}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	bl, err := loadBaseline(dir)
	if err != nil {
		t.Fatalf("loadBaseline: %v", err)
	}
	if _, ok := bl.Listing[ignored]; ok {
		t.Errorf("Listing holds %q, which .dsxignore excludes; no reader can filter it back out", ignored)
	}
	if _, ok := bl.Listing[kept]; !ok {
		t.Errorf("Listing is missing %q — the filter took the wrong side", kept)
	}
}
