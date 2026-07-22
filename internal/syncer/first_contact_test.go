package syncer

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const firstContactBody = "server"

// firstContactFake serves two remote files, one of which the caller already
// holds locally under different bytes.
func firstContactFake(t *testing.T) *fakeMCP {
	t.Helper()
	return newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(
				fileEntry("README.md", "e1", int64(len(firstContactBody))),
				fileEntry("other.css", "e2", int64(len(firstContactBody))))}
		}
		p, _ := args["path"].(string)
		return fakeReply{Text: envelopeFor(p, "e1", firstContactBody)}
	})
}

// A first pull into a populated directory used to write every non-conflicting
// path and only then report the conflict, leaving a half-foreign tree the
// caller never agreed to. Nothing is tracked yet, so nothing about that tree
// was asked for: refuse before the first byte.
func TestAFirstPullWritesNothingWhenItAlreadyHasAConflict(t *testing.T) {
	dir := t.TempDir()
	mine := []byte("MY OWN WORK\n")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), mine, 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := Pull(context.Background(), fakeClient(firstContactFake(t)), PullOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 2,
	})
	if err != nil {
		t.Fatalf("Pull errored instead of reporting: %v", err)
	}
	if rep.Outcome() == nil {
		t.Fatal("a first pull into a conflicting directory reported no conflict")
	}
	if len(rep.Fetched) != 0 {
		t.Errorf("fetched=%v, want none — the refusal must precede the first write", rep.Fetched)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "other.css")); statErr == nil {
		t.Error("other.css was written before the conflict was reported")
	}
	got, readErr := os.ReadFile(filepath.Join(dir, "README.md"))
	if readErr != nil || string(got) != string(mine) {
		t.Errorf("README.md = %q, %v — the caller's file must be untouched", got, readErr)
	}
	if LedgerExistsForTest(dir) {
		t.Error("a refusal wrote a ledger; nothing happened, so nothing should be recorded")
	}
}

// A first pull with no conflict is the ordinary clone and must still work.
func TestAFirstPullWithNoConflictStillWrites(t *testing.T) {
	dir := t.TempDir()

	rep, err := Pull(context.Background(), fakeClient(firstContactFake(t)), PullOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 2,
	})
	if err != nil {
		t.Fatalf("a clean first pull failed: %v", err)
	}
	if len(rep.Fetched) != 2 {
		t.Errorf("fetched=%v, want both files", rep.Fetched)
	}
}

// An established sync is unaffected: a conflict on one path must not stop the
// others, which is what makes a conflict recoverable one file at a time.
func TestAnEstablishedPullStillWritesAroundAConflict(t *testing.T) {
	dir := t.TempDir()
	mine := []byte("edited\n")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), mine, 0o644); err != nil {
		t.Fatal(err)
	}
	seedFirstContactLedger(t, dir)

	rep, err := Pull(context.Background(), fakeClient(firstContactFake(t)), PullOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 2,
	})
	if err != nil {
		t.Fatalf("Pull errored instead of reporting: %v", err)
	}
	if rep.Outcome() == nil {
		t.Fatal("want the conflict reported")
	}
	if len(rep.Fetched) == 0 {
		t.Error("an established pull stopped fetching the paths that were not in conflict")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "other.css")); statErr != nil {
		t.Errorf("other.css was not written: %v", statErr)
	}
}

// --force is the caller saying they want the server's copy; it must still work
// on a first contact.
func TestAForcedFirstPullStillWrites(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := Pull(context.Background(), fakeClient(firstContactFake(t)), PullOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 2, Force: true,
	})
	if err != nil {
		t.Fatalf("--force on a first pull failed: %v", err)
	}
	if len(rep.Fetched) != 2 {
		t.Errorf("fetched=%v, want both under --force", rep.Fetched)
	}
}

// TestFirstContactStillRefusesWhenABaselineExists: one path is baselined and
// matches (Verified, not a conflict), a second is a real conflict. First
// contact's "write nothing while a conflict exists" rule must still hold —
// a baseline redirecting one path out of Conflicts must not disable the
// refusal that protects every other path.
func TestFirstContactStillRefusesWhenABaselineExists(t *testing.T) {
	dir := t.TempDir()
	verifiedBody := []byte("shared bytes\n")
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), verifiedBody, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "other.css"), []byte("MINE\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(
				fileEntry("readme.md", "e1", int64(len(verifiedBody))),
				fileEntry("other.css", "e2", 5))}
		}
		p, _ := args["path"].(string)
		return fakeReply{Text: envelopeFor(p, "e2", "server")}
	})
	c := fakeClient(f)

	bl := Baseline{
		ProjectID: "proj-A",
		Endpoint:  c.Endpoint(),
		Verified: map[string]BaselineEntry{
			"readme.md": {Etag: "e1", Size: int64(len(verifiedBody)), SHA: SHA256Hex(verifiedBody)},
		},
	}
	if err := bl.save(dir); err != nil {
		t.Fatal(err)
	}

	rep, err := Pull(context.Background(), c, PullOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 2,
	})
	if err != nil {
		t.Fatalf("Pull errored instead of reporting: %v", err)
	}
	if rep.Verified != 1 {
		t.Errorf("verified=%d, want 1 for the baselined path", rep.Verified)
	}
	if !slices.Contains(rep.Conflicts, "other.css") {
		t.Errorf("conflicts=%v, want other.css present", rep.Conflicts)
	}
	if slices.Contains(rep.Conflicts, "readme.md") {
		t.Errorf("conflicts=%v, readme.md must not be reported as a conflict — it is proven", rep.Conflicts)
	}
	if len(rep.Fetched) != 0 {
		t.Errorf("fetched=%v, want none — first contact must still refuse while a real conflict exists", rep.Fetched)
	}
	if LedgerExistsForTest(dir) {
		t.Error("a refusal wrote a ledger; nothing happened, so nothing should be recorded")
	}
}
