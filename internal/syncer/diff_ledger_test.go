package syncer

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
)

// diffCountingFake serves one path and counts what read_file was asked for.
func diffCountingFake(t *testing.T, path, body, serverEtag string, reads *atomic.Int64) *fakeMCP {
	t.Helper()
	return newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry(path, serverEtag, int64(len(body))))}
		case "read_file":
			reads.Add(1)
			p, _ := args["path"].(string)
			return fakeReply{Text: envelopeFor(p, serverEtag, body)}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})
}

// TestDiffProvesATrackedPathFromTheLedger: a path dsx itself pulled is one
// whose sha AT a known etag the ledger already records. If the server still
// shows that etag and the bytes on disk still hash to that sha, the two sides
// are identical and downloading them again proves nothing. Measured before
// the fix: `dsx diff` immediately after a `dsx clone` re-downloaded all four
// files of the sandbox project to report all four `same`.
func TestDiffProvesATrackedPathFromTheLedger(t *testing.T) {
	dir := t.TempDir()
	body := "tracked{}\n"
	mkfile(t, dir, "a.css", body)

	st := State{ProjectID: "proj-A", Files: map[string]FileState{
		"a.css": {Etag: "e1", Size: int64(len(body)), SHA: SHA256Hex([]byte(body))},
	}}
	if err := st.save(dir); err != nil {
		t.Fatal(err)
	}

	var reads atomic.Int64
	rep, err := Diff(context.Background(), fakeClient(diffCountingFake(t, "a.css", body, "e1", &reads)),
		DiffOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 2})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !slices.Equal(rep.Same, []string{"a.css"}) {
		t.Errorf("Same = %v, want [a.css] (report %+v)", rep.Same, rep)
	}
	if got := reads.Load(); got != 0 {
		t.Errorf("read_file called %d times for a path the ledger already proves", got)
	}
}

// TestDiffStillDownloadsWhenTheServerMovedPastTheLedger is the control that
// makes the one above mean something: the ledger proves a path only while the
// listing still shows the etag it was recorded at.
func TestDiffStillDownloadsWhenTheServerMovedPastTheLedger(t *testing.T) {
	dir := t.TempDir()
	body := "tracked{}\n"
	mkfile(t, dir, "a.css", body)

	st := State{ProjectID: "proj-A", Files: map[string]FileState{
		"a.css": {Etag: "e1", Size: int64(len(body)), SHA: SHA256Hex([]byte(body))},
	}}
	if err := st.save(dir); err != nil {
		t.Fatal(err)
	}

	var reads atomic.Int64
	// Same bytes, new etag: the server was rewritten with identical content,
	// which every write does (etags are write timestamps, not content hashes).
	rep, err := Diff(context.Background(), fakeClient(diffCountingFake(t, "a.css", body, "e2-NEW", &reads)),
		DiffOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 2})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if got := reads.Load(); got != 1 {
		t.Errorf("read_file called %d times; a moved etag must be re-read, not assumed", got)
	}
	if !slices.Equal(rep.Same, []string{"a.css"}) {
		t.Errorf("Same = %v, want [a.css] — the download found the bytes equal", rep.Same)
	}
}

// TestDiffStillDownloadsWhenTheLocalCopyMovedPastTheLedger is the other
// control. The ledger's sha describes the bytes dsx wrote; an edited file no
// longer hashes to it, so the entry proves nothing about what is there now.
func TestDiffStillDownloadsWhenTheLocalCopyMovedPastTheLedger(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "a.css", "EDITED-LOCALLY\n")

	st := State{ProjectID: "proj-A", Files: map[string]FileState{
		"a.css": {Etag: "e1", Size: 10, SHA: SHA256Hex([]byte("tracked{}\n"))},
	}}
	if err := st.save(dir); err != nil {
		t.Fatal(err)
	}

	var reads atomic.Int64
	rep, err := Diff(context.Background(), fakeClient(diffCountingFake(t, "a.css", "tracked{}\n", "e1", &reads)),
		DiffOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 2})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if got := reads.Load(); got != 1 {
		t.Errorf("read_file called %d times; an edited local file must be re-read", got)
	}
	if len(rep.Differs) != 1 || rep.Differs[0].Path != "a.css" {
		t.Errorf("Differs = %+v, want the one edited path", rep.Differs)
	}
}

// TestDiffNeverProvesABinaryLedgerEntry: `Binary: true` means dsx did not put
// those bytes there (invariant 23). A plain pull records the marker with no
// sha at all, and a forced push records the bytes it SENT beside it — neither
// is a claim about what is on disk now, so neither may stand in for a read.
func TestDiffNeverProvesABinaryLedgerEntry(t *testing.T) {
	dir := t.TempDir()
	body := "not really binary\n"
	mkfile(t, dir, "a.png", body)

	st := State{ProjectID: "proj-A", Files: map[string]FileState{
		"a.png": {Etag: "e1", Size: int64(len(body)), SHA: SHA256Hex([]byte(body)), Binary: true},
	}}
	if err := st.save(dir); err != nil {
		t.Fatal(err)
	}

	var reads atomic.Int64
	if _, err := Diff(context.Background(), fakeClient(diffCountingFake(t, "a.png", body, "e1", &reads)),
		DiffOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 2}); err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if got := reads.Load(); got != 1 {
		t.Errorf("read_file called %d times; a binary ledger entry proves nothing about local bytes", got)
	}
}
