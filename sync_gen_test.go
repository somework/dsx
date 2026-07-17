package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// End-to-end cover for pull.go, push.go and tree.go against the fake endpoint.
//
// The fake proves nothing about the protocol -- see fake_test.go. What these
// tests pin is dsx's own conduct around the decisions in plan.go: what reaches
// the disk, what reaches the ledger, and what a caller is told when a run does
// not finish. Every assertion below is on that conduct.

// ---------------------------------------------------------------------------
// helpers (all prefixed `sync` so they cannot collide with another area's)
// ---------------------------------------------------------------------------

func syncLoadState(t *testing.T, dir string) syncState {
	t.Helper()
	st, err := loadState(dir)
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	return st
}

func syncSeedState(t *testing.T, dir string, st syncState) {
	t.Helper()
	if st.Files == nil {
		st.Files = map[string]fileState{}
	}
	if err := st.save(dir); err != nil {
		t.Fatalf("seeding ledger: %v", err)
	}
}

func syncLedgerExists(t *testing.T, dir string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, stateFileName))
	return err == nil
}

func syncExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

// syncWindow renders one windowed read_file reply, framed the way the live
// server frames one.
//
// A window that stops short of total_lines carries, INSIDE its body, a notice
// saying so. That is measured, not assumed (a 316 KB live read on 2026-07-17);
// dsx spliced that notice into reassembled files until it was caught. A fixture
// without it would be testing a protocol that does not exist, which is exactly
// how the bug survived being "covered" in the first place.
func syncWindow(path, etag string, lo, hi, total int, body string) string {
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(body)
	if hi < total {
		esc += fmt.Sprintf(
			"\n…[+%d bytes truncated at read_file's 256 KiB cap — the body ends at a complete line; continue with offset=%d]",
			4096, hi+1)
	}
	return fmt.Sprintf(
		"<untrusted-project-content path=%q etag=%q lines=\"%d-%d\" total_lines=\"%d\">\n%s\n</untrusted-project-content>",
		path, etag, lo, hi, total, esc)
}

// syncArgFiles pulls the `files` array out of a recorded tool call without
// panicking on a shape we did not send; a panic in a server goroutine takes the
// whole test binary down and says nothing useful.
func syncArgFiles(args map[string]any) []map[string]any {
	raw, _ := args["files"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func syncFirstCall(t *testing.T, f *fakeMCP, tool string) recordedCall {
	t.Helper()
	for _, c := range f.recorded() {
		if c.Tool == tool {
			return c
		}
	}
	t.Fatalf("%s was never called; calls: %v", tool, f.recorded())
	return recordedCall{}
}

func syncToolOrder(f *fakeMCP) []string {
	var out []string
	for _, c := range f.recorded() {
		if c.Tool != "" {
			out = append(out, c.Tool)
		}
	}
	return out
}

// syncWaitForPath blocks until path appears. It is how a fake reply orders
// itself after another fetch has genuinely landed on disk: without it, the
// failing fetch's cancel() could abort the succeeding one and a test meant to
// prove "the ledger records what moved" would prove nothing.
func syncWaitForPath(path string) bool {
	for range 4000 {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

// syncCancelAfterRT cancels a context once n round trips have completed, each
// reply having been buffered first.
//
// Cancelling from inside the fake's handler instead would abort the in-flight
// request, which exercises the error path -- a different line from the
// interruption path. Only a cancel that lands between two successful calls
// reaches the parent.Err() check that invariant 3 is about.
type syncCancelAfterRT struct {
	base   http.RoundTripper
	after  int64
	seen   atomic.Int64
	cancel context.CancelFunc
}

func (rt *syncCancelAfterRT) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := rt.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if rt.seen.Add(1) == rt.after {
		rt.cancel()
	}
	return resp, nil
}

// syncCancelAfter wires c to cancel a fresh context once n replies have been
// fully received, and returns that context.
func syncCancelAfter(t *testing.T, c *client, n int64) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c.http = &http.Client{Transport: &syncCancelAfterRT{
		base: http.DefaultTransport, after: n, cancel: cancel,
	}}
	return ctx
}

// ---------------------------------------------------------------------------
// tree.go
// ---------------------------------------------------------------------------

func TestWalkTreeReturnsProjectRelativePathsFromEveryDepth(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name != "list_files" {
			return fakeReply{Text: "unexpected " + name, IsError: true}
		}
		switch args["path"] {
		case nil:
			return fakeReply{Text: listingFor(
				fileEntry("index.css", "e1", 3),
				dirEntry("tokens"),
			)}
		case "tokens":
			return fakeReply{Text: listingFor(
				dirEntry("tokens/deep"),
				fileEntry("tokens/colors.css", "e2", 5),
			)}
		case "tokens/deep":
			return fakeReply{Text: listingFor(fileEntry("tokens/deep/x.css", "e3", 7))}
		}
		return fakeReply{Text: "no such dir", IsError: true}
	})

	got, err := f.client().walkTree(context.Background(), "p1", 4)
	if err != nil {
		t.Fatalf("walkTree: %v", err)
	}
	want := []string{"index.css", "tokens/colors.css", "tokens/deep/x.css"}
	if !slices.Equal(sortedPaths(got), want) {
		t.Errorf("paths = %v, want %v — keys must be project-relative, not basenames", sortedPaths(got), want)
	}
	// Directories must not survive into the file map: planPull would try to
	// fetch one.
	for p, e := range got {
		if e.isDir() {
			t.Errorf("%s came back as a directory entry", p)
		}
	}
	if got["tokens/colors.css"].Etag != "e2" {
		t.Errorf("etag = %q, want e2 — the listing's etags are the whole point of a cheap sync", got["tokens/colors.css"].Etag)
	}
}

func TestWalkTreeDescendsSiblingDirectoriesConcurrently(t *testing.T) {
	// Each sibling refuses to answer until the other has arrived. A sequential
	// walk deadlocks and the timeout turns it into a visible failure; a
	// concurrent one sails through. This is the difference between one listing
	// per directory costing a round trip each and costing all of them in series.
	arrivedX := make(chan struct{})
	arrivedY := make(chan struct{})

	await := func(other chan struct{}) bool {
		select {
		case <-other:
			return true
		case <-time.After(5 * time.Second):
			return false
		}
	}

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch args["path"] {
		case nil:
			return fakeReply{Text: listingFor(dirEntry("x"), dirEntry("y"))}
		case "x":
			close(arrivedX)
			if !await(arrivedY) {
				return fakeReply{Text: "y was never listed alongside x: the walk is sequential", IsError: true}
			}
			return fakeReply{Text: listingFor(fileEntry("x/1.css", "e", 1))}
		case "y":
			close(arrivedY)
			if !await(arrivedX) {
				return fakeReply{Text: "x was never listed alongside y: the walk is sequential", IsError: true}
			}
			return fakeReply{Text: listingFor(fileEntry("y/1.css", "e", 1))}
		}
		return fakeReply{Text: "unexpected", IsError: true}
	})

	got, err := f.client().walkTree(context.Background(), "p1", 4)
	if err != nil {
		t.Fatalf("walkTree: %v", err)
	}
	if !slices.Equal(sortedPaths(got), []string{"x/1.css", "y/1.css"}) {
		t.Errorf("paths = %v, want both siblings", sortedPaths(got))
	}
}

// Invariant 3. A cancel that lands between two successful listings leaves no
// error behind, so only parent.Err() can tell that the tree was never finished.
// Returning the map here would let `--prune` read "not enumerated" as "deleted
// on the server" and delete live files.
func TestWalkTreeInterruptedBetweenListingsIsAFailureNotAShortListing(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		// No directories: the walk cannot produce an error of its own, so a
		// green result here would come strictly from the missing parent check.
		return fakeReply{Text: listingFor(fileEntry("a.css", "e1", 3))}
	})
	c := f.client()
	ctx := syncCancelAfter(t, c, 1)

	got, err := c.walkTree(ctx, "p1", 4)
	if err == nil {
		t.Fatalf("walkTree returned %v and no error — an interrupted walk must not report success", sortedPaths(got))
	}
	if got != nil {
		t.Errorf("listing = %v, want nil — a caller must not be handed a tree it can diff against", sortedPaths(got))
	}
	if f.countTool("list_files") != 1 {
		t.Fatalf("list_files calls = %d, want 1 — the scenario relies on there being no failing call",
			f.countTool("list_files"))
	}
}

func TestWalkTreeReturnsNoListingWhenADirectoryFails(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if args["path"] == nil {
			return fakeReply{Text: listingFor(fileEntry("a.css", "e1", 3), dirEntry("bad"))}
		}
		return fakeReply{Text: "permission denied", IsError: true}
	})

	got, err := f.client().walkTree(context.Background(), "p1", 4)
	if err == nil {
		t.Fatal("walkTree succeeded despite a failed directory")
	}
	// a.css was enumerated. Handing it back with an error would still be a
	// short listing, and callers that check the map first would act on it.
	if got != nil {
		t.Errorf("listing = %v, want nil alongside the error", sortedPaths(got))
	}
}

func TestListDirOmitsThePathForTheRootAndSendsItForASubdirectory(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})
	c := f.client()

	if _, err := c.listDir(context.Background(), "p1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.listDir(context.Background(), "p1", "tokens"); err != nil {
		t.Fatal(err)
	}

	calls := f.recorded()
	if _, ok := calls[0].Args["path"]; ok {
		// "" is not the root; omitting the argument is.
		t.Errorf("root listing sent path=%v, want the argument omitted", calls[0].Args["path"])
	}
	if calls[1].Args["path"] != "tokens" {
		t.Errorf("subdirectory listing sent path=%v, want tokens", calls[1].Args["path"])
	}
}

func TestListDirReportsAMalformedListingRatherThanAnEmptyOne(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: "not json at all"}
	})
	entries, err := f.client().listDir(context.Background(), "p1", "")
	if err == nil {
		t.Fatalf("listDir accepted garbage and returned %v — an empty tree drives --prune", entries)
	}
	if !strings.Contains(err.Error(), "malformed listing") {
		t.Errorf("error = %v, want it to name the malformed listing", err)
	}
}

// ---------------------------------------------------------------------------
// readFull
// ---------------------------------------------------------------------------

func TestReadFullConcatenatesEveryWindowAndAsksForTheNextByLine(t *testing.T) {
	// The question this test used to leave open -- whether the newline between
	// two windows rides on the end of the first or the start of the second --
	// has since been answered live: the server ends every window at a complete
	// line, so the newline is the first window's last byte and the pieces need
	// no separator between them. The fixtures below reflect that measurement.
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name != "read_file" {
			return fakeReply{Text: "unexpected " + name, IsError: true}
		}
		if _, windowed := args["offset"]; !windowed {
			return fakeReply{Text: syncWindow("a.css", "e1", 1, 2, 4, "alpha\nbeta\n")}
		}
		return fakeReply{Text: syncWindow("a.css", "e1", 3, 4, 4, "gamma\ndelta\n")}
	})

	body, etag, err := f.client().readFull(context.Background(), "p1", "a.css")
	if err != nil {
		t.Fatalf("readFull: %v", err)
	}
	if want := "alpha\nbeta\ngamma\ndelta\n"; body != want {
		t.Errorf("body = %q, want %q — the windows must be joined in order, with no server prose between them", body, want)
	}
	if etag != "e1" {
		t.Errorf("etag = %q, want e1", etag)
	}
	calls := f.recorded()
	if len(calls) != 2 {
		t.Fatalf("read_file calls = %d, want 2 — the read must walk to completion", len(calls))
	}
	if _, ok := calls[0].Args["offset"]; ok {
		t.Errorf("first read sent offset=%v, want it omitted", calls[0].Args["offset"])
	}
	if got := calls[1].Args["offset"]; got != float64(3) {
		t.Errorf("second read sent offset=%v, want 3 (one past the last line returned)", got)
	}
}

func TestReadFullRefusesATruncatedLineRatherThanReturningIt(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: `<untrusted-project-content path="a.css" etag="e1" truncated_line="true">` +
			"\nhalf of a very long line\n</untrusted-project-content>"}
	})
	body, _, err := f.client().readFull(context.Background(), "p1", "a.css")
	if err == nil {
		t.Fatalf("readFull returned %q for a truncated line", body)
	}
	if body != "" {
		t.Errorf("body = %q, want empty — half a line must never reach a caller", body)
	}
	if !strings.Contains(err.Error(), "256 KiB") {
		t.Errorf("error = %v, want it to explain the read cap", err)
	}
}

// A file that moves between two windows cannot be stitched: the halves come
// from different versions. Nothing downstream could detect the seam, so the
// refusal has to happen here.
func TestReadFullRefusesAnEtagThatChangesMidRead(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if _, windowed := args["offset"]; !windowed {
			return fakeReply{Text: syncWindow("a.css", "e1", 1, 2, 4, "alpha\nbeta\n")}
		}
		return fakeReply{Text: syncWindow("a.css", "e2", 3, 4, 4, "gamma\ndelta\n")}
	})
	body, etag, err := f.client().readFull(context.Background(), "p1", "a.css")
	if err == nil {
		t.Fatalf("readFull stitched two versions together and returned %q", body)
	}
	if body != "" || etag != "" {
		t.Errorf("readFull returned body=%q etag=%q, want both empty", body, etag)
	}
	for _, want := range []string{"mid-read", "e1", "e2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to name %q", err, want)
		}
	}
}

func TestReadFullRefusesAReadThatMakesNoProgress(t *testing.T) {
	// A server that keeps answering with the same window would otherwise spin
	// forever, appending the same bytes each time.
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: syncWindow("a.css", "e1", 1, 2, 4, "alpha\nbeta\n")}
	})
	_, _, err := f.client().readFull(context.Background(), "p1", "a.css")
	if err == nil {
		t.Fatal("readFull accepted a window that repeated itself")
	}
	if !strings.Contains(err.Error(), "no progress") {
		t.Errorf("error = %v, want it to name the stall", err)
	}
	if n := f.countTool("read_file"); n != 2 {
		t.Errorf("read_file calls = %d, want 2 — the loop must break on the repeat", n)
	}
}

// ---------------------------------------------------------------------------
// pull.go
// ---------------------------------------------------------------------------

// Invariant 1. This assertion is what caught an agent-driven pull silently
// corrupting 2 of 100 files: a size the listing already told us disagreeing
// with the decode means the decode is wrong, and a wrong file on disk is worse
// than no file.
func TestPullRefusesToWriteAFileWhoseDecodedLengthDisagreesWithTheListing(t *testing.T) {
	dir := t.TempDir()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("a.css", "e1", 100))}
		case "read_file":
			return fakeReply{Text: envelopeFor("a.css", "e1", "hi")} // 2 bytes, not 100
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	rep, err := runPull(context.Background(), f.client(), pullOpts{
		projectID: "p1", dir: dir, concurrency: 4,
	})
	if err == nil {
		t.Fatal("pull accepted a body that disagrees with the listing")
	}
	if !strings.Contains(err.Error(), "refusing to write") {
		t.Errorf("error = %v, want it to say the write was refused", err)
	}
	if syncExists(t, filepath.Join(dir, "a.css")) {
		t.Error("a.css landed on disk — a corrupt file is worse than no file")
	}
	if len(rep.Fetched) != 0 {
		t.Errorf("fetched = %v, want none", rep.Fetched)
	}
	// The ledger is saved on the error path (invariant 5), but it must not
	// claim we hold bytes we refused to write.
	if _, tracked := syncLoadState(t, dir).Files["a.css"]; tracked {
		t.Error("the ledger records a.css — the next run would call it unchanged")
	}
}

// Invariant 5. A file whose bytes are already on disk must be in the ledger
// even though the run failed. Without the entry, the next sync sees bytes it
// has no record of, calls its own work a conflict, and pushes the user to
// --force -- the opposite of safe.
func TestPullSavesTheLedgerForAFileAlreadyWrittenWhenALaterFetchFails(t *testing.T) {
	dir := t.TempDir()
	const bodyA = "body{color:red}"

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(
				fileEntry("a.css", "e1", int64(len(bodyA))),
				fileEntry("b.css", "e2", 999), // the listing disagrees with the body
			)}
		case "read_file":
			if args["path"] == "a.css" {
				return fakeReply{Text: envelopeFor("a.css", "e1", bodyA)}
			}
			// Hold b.css back until a.css has landed, or b's cancel() could
			// abort a's read and the test would prove nothing.
			if !syncWaitForPath(filepath.Join(dir, "a.css")) {
				return fakeReply{Text: "a.css never landed", IsError: true}
			}
			return fakeReply{Text: envelopeFor("b.css", "e2", "short")}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	_, err := runPull(context.Background(), f.client(), pullOpts{
		projectID: "p1", dir: dir, concurrency: 4,
	})
	if err == nil {
		t.Fatal("pull reported success despite b.css failing")
	}
	if !syncLedgerExists(t, dir) {
		t.Fatal("no ledger was written — a.css is on disk with no record of it")
	}

	st := syncLoadState(t, dir)
	if st.ProjectID != "p1" {
		t.Errorf("project_id = %q, want p1 — an empty pin is no pin", st.ProjectID)
	}
	got, tracked := st.Files["a.css"]
	if !tracked {
		t.Fatalf("ledger files = %v, want a.css recorded", sortedPaths(st.Files))
	}
	want := fileState{Etag: "e1", Size: int64(len(bodyA)), SHA: sha256hex([]byte(bodyA))}
	if got != want {
		t.Errorf("a.css state = %+v, want %+v", got, want)
	}
	if _, tracked := st.Files["b.css"]; tracked {
		t.Error("b.css is in the ledger although it was never written")
	}
	if syncExists(t, filepath.Join(dir, "b.css")) {
		t.Error("b.css landed on disk despite the size mismatch")
	}
}

// Invariants 3 and 5 together: the fetch succeeded and the bytes are down, but
// the user interrupted us before the run finished. That is a failure whose
// ledger still has to record what moved.
func TestPullInterruptedAfterAFetchIsAFailureThatStillRecordsTheFetch(t *testing.T) {
	dir := t.TempDir()
	const body = "a{}"

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("a.css", "e1", int64(len(body))))}
		case "read_file":
			return fakeReply{Text: envelopeFor("a.css", "e1", body)}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})
	c := f.client()
	// Round trip 1 is the listing, 2 is the read: cancel lands after both.
	ctx := syncCancelAfter(t, c, 2)

	rep, err := runPull(ctx, c, pullOpts{projectID: "p1", dir: dir, concurrency: 4})
	if err == nil {
		t.Fatalf("pull reported success (%+v) after being interrupted", rep)
	}
	if !strings.Contains(err.Error(), "interrupted") {
		t.Errorf("error = %v, want it to name the interruption", err)
	}
	if !syncExists(t, filepath.Join(dir, "a.css")) {
		t.Fatal("a.css is not on disk — the scenario relies on the fetch having finished")
	}
	if _, tracked := syncLoadState(t, dir).Files["a.css"]; !tracked {
		t.Error("a.css is on disk with no ledger entry — the next run calls it a conflict")
	}
}

// The project pin. Etags belong to one project; replaying them against another
// would compare versions that have nothing to do with each other.
func TestPullRefusesAProjectTheDirectoryIsNotBoundTo(t *testing.T) {
	dir := t.TempDir()
	syncSeedState(t, dir, syncState{ProjectID: "project-a"})

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})
	_, err := runPull(context.Background(), f.client(), pullOpts{
		projectID: "project-b", dir: dir, concurrency: 4,
	})
	if err == nil {
		t.Fatal("pull crossed the pin")
	}
	for _, want := range []string{"project-a", "project-b"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to name %q", err, want)
		}
	}
	if n := len(f.recorded()); n != 0 {
		t.Errorf("%d requests were made before the pin was checked, want 0", n)
	}
}

func TestPushRefusesAProjectTheDirectoryIsNotBoundTo(t *testing.T) {
	dir := t.TempDir()
	syncSeedState(t, dir, syncState{ProjectID: "project-a"})

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})
	_, err := runPush(context.Background(), f.client(), pushOpts{
		projectID: "project-b", dir: dir, concurrency: 4,
	})
	if err == nil {
		t.Fatal("push crossed the pin")
	}
	for _, want := range []string{"project-a", "project-b"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to name %q", err, want)
		}
	}
	if n := len(f.recorded()); n != 0 {
		t.Errorf("%d requests were made before the pin was checked, want 0", n)
	}
}

func TestPullBinaryRefusalIsRecordedAgainstItsEtagAndRetriedWhenItChanges(t *testing.T) {
	t.Run("an unreadable file is reported, not an error", func(t *testing.T) {
		dir := t.TempDir()
		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			switch name {
			case "list_files":
				return fakeReply{Text: listingFor(fileEntry("logo.png", "e1", 67))}
			case "read_file":
				return fakeReply{Text: "logo.png is a binary file and cannot be read", IsError: true}
			}
			return fakeReply{Text: "unexpected " + name, IsError: true}
		})

		rep, err := runPull(context.Background(), f.client(), pullOpts{
			projectID: "p1", dir: dir, concurrency: 4,
		})
		if err != nil {
			t.Fatalf("pull failed on a binary file: %v — the asymmetry is the service's, not ours", err)
		}
		if !slices.Equal(rep.Binary, []string{"logo.png"}) {
			t.Errorf("binary = %v, want [logo.png]", rep.Binary)
		}
		if len(rep.Fetched) != 0 {
			t.Errorf("fetched = %v, want none", rep.Fetched)
		}
		if syncExists(t, filepath.Join(dir, "logo.png")) {
			t.Error("logo.png landed on disk although read_file never served it")
		}
		got := syncLoadState(t, dir).Files["logo.png"]
		want := fileState{Etag: "e1", Binary: true}
		if got != want {
			t.Errorf("ledger entry = %+v, want %+v — the refusal is remembered against the etag", got, want)
		}
	})

	t.Run("the same etag is not asked for again", func(t *testing.T) {
		dir := t.TempDir()
		syncSeedState(t, dir, syncState{ProjectID: "p1", Files: map[string]fileState{
			"logo.png": {Etag: "e1", Binary: true},
		}})
		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			if name == "list_files" {
				return fakeReply{Text: listingFor(fileEntry("logo.png", "e1", 67))}
			}
			return fakeReply{Text: "unexpected " + name, IsError: true}
		})

		rep, err := runPull(context.Background(), f.client(), pullOpts{
			projectID: "p1", dir: dir, concurrency: 4,
		})
		if err != nil {
			t.Fatal(err)
		}
		if n := f.countTool("read_file"); n != 0 {
			t.Errorf("read_file calls = %d, want 0 — a known refusal at a known etag costs nothing", n)
		}
		if !slices.Equal(rep.Binary, []string{"logo.png"}) {
			t.Errorf("binary = %v, want [logo.png] — the file must still be reported", rep.Binary)
		}
	})

	t.Run("a new etag re-tries it", func(t *testing.T) {
		dir := t.TempDir()
		syncSeedState(t, dir, syncState{ProjectID: "p1", Files: map[string]fileState{
			"logo.png": {Etag: "e1", Binary: true},
		}})
		const body = "now text"
		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			switch name {
			case "list_files":
				return fakeReply{Text: listingFor(fileEntry("logo.png", "e2", int64(len(body))))}
			case "read_file":
				return fakeReply{Text: envelopeFor("logo.png", "e2", body)}
			}
			return fakeReply{Text: "unexpected " + name, IsError: true}
		})

		rep, err := runPull(context.Background(), f.client(), pullOpts{
			projectID: "p1", dir: dir, concurrency: 4,
		})
		if err != nil {
			t.Fatal(err)
		}
		if n := f.countTool("read_file"); n != 1 {
			t.Fatalf("read_file calls = %d, want 1 — a changed etag must be re-asked", n)
		}
		if !slices.Equal(rep.Fetched, []string{"logo.png"}) {
			t.Errorf("fetched = %v, want [logo.png]", rep.Fetched)
		}
		if st := syncLoadState(t, dir); st.Files["logo.png"].Binary {
			t.Error("the ledger still marks logo.png binary after it was read as text")
		}
	})
}

// "Binary" is a report; everything else is a failure. Widening the refusal test
// would turn any read_file failure into a file dsx quietly claims to know
// about, at an etag it never verified -- and the next sync would skip it.
func TestPullTreatsANonBinaryFetchFailureAsAnErrorNotAsABinaryFile(t *testing.T) {
	cases := []struct {
		name  string
		reply fakeReply
	}{
		// A tool error, but not the one refusal we tolerate.
		{"a tool error that is not the binary refusal",
			fakeReply{Text: "a.css: no such path in this project", IsError: true}},
		// Not a tool error at all: nothing to match on, so a fallthrough here
		// would be an unclassified failure treated as success.
		{"a reply that is not an envelope",
			fakeReply{Text: "<html>gateway timeout</html>"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
				if name == "list_files" {
					return fakeReply{Text: listingFor(fileEntry("a.css", "e1", 4))}
				}
				return tc.reply
			})

			rep, err := runPull(context.Background(), f.client(), pullOpts{
				projectID: "p1", dir: dir, concurrency: 4,
			})
			if err != nil && !strings.Contains(err.Error(), "a.css") {
				t.Errorf("error = %v, want it to name the file that failed", err)
			}
			if err == nil {
				t.Fatalf("pull reported success (%+v) although the only fetch failed", rep)
			}
			if len(rep.Binary) != 0 {
				t.Errorf("binary = %v, want none — this was a failure, not a refusal", rep.Binary)
			}
			if _, tracked := syncLoadState(t, dir).Files["a.css"]; tracked {
				t.Error("a.css reached the ledger — the next sync would skip a file we never read")
			}
			if syncExists(t, filepath.Join(dir, "a.css")) {
				t.Error("a.css landed on disk")
			}
		})
	}
}

// Invariant 7 in the direction that matters: the path came from the server, so
// it is untrusted input, and the fetch happens before safeJoin sees it.
func TestPullNeverWritesOutsideTheTargetDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "design")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const body = "pwned"

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("../escape.css", "e1", int64(len(body))))}
		case "read_file":
			return fakeReply{Text: envelopeFor("../escape.css", "e1", body)}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	_, err := runPull(context.Background(), f.client(), pullOpts{
		projectID: "p1", dir: dir, concurrency: 4,
	})
	if err == nil {
		t.Fatal("pull accepted a path that leaves the sync directory")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Errorf("error = %v, want it to name the escape", err)
	}
	if syncExists(t, filepath.Join(root, "escape.css")) {
		t.Fatal("the server wrote a file outside the sync directory")
	}
}

func TestPullDryRunTransfersNothingAndLeavesNoLedger(t *testing.T) {
	dir := t.TempDir()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(
				fileEntry("a.css", "e1", 10),
				fileEntry("b.css", "e2", 7),
			)}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	rep, err := runPull(context.Background(), f.client(), pullOpts{
		projectID: "p1", dir: dir, concurrency: 4, dryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.Fetched, []string{"a.css", "b.css"}) {
		t.Errorf("fetched = %v, want both listed", rep.Fetched)
	}
	if rep.Bytes != 17 {
		t.Errorf("bytes = %d, want 17 — the size comes from the listing, not from a read", rep.Bytes)
	}
	if n := f.countTool("read_file"); n != 0 {
		t.Errorf("read_file calls = %d, want 0 — `status` must transfer nothing", n)
	}
	if syncLedgerExists(t, dir) {
		t.Error("a dry run wrote a ledger")
	}
	if syncExists(t, filepath.Join(dir, "a.css")) {
		t.Error("a dry run wrote a file")
	}
}

func TestPullPruneRemovesATrackedUnmodifiedFileFromDiskAndLedger(t *testing.T) {
	dir := t.TempDir()
	const body = "gone{}"
	mkfile(t, dir, "gone.css", body)
	syncSeedState(t, dir, syncState{ProjectID: "p1", Files: map[string]fileState{
		"gone.css": {Etag: "e1", Size: int64(len(body)), SHA: sha256hex([]byte(body))},
	}})

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor()} // the server no longer has it
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	rep, err := runPull(context.Background(), f.client(), pullOpts{
		projectID: "p1", dir: dir, concurrency: 4, prune: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.Deleted, []string{"gone.css"}) {
		t.Errorf("deleted = %v, want [gone.css]", rep.Deleted)
	}
	if syncExists(t, filepath.Join(dir, "gone.css")) {
		t.Error("gone.css is still on disk")
	}
	if _, tracked := syncLoadState(t, dir).Files["gone.css"]; tracked {
		t.Error("gone.css is still in the ledger — a deleted file we still claim to hold")
	}
}

// ---------------------------------------------------------------------------
// push.go
// ---------------------------------------------------------------------------

func TestPushSendsBase64WithIfMatchAndRecordsTheReturnedEtags(t *testing.T) {
	dir := t.TempDir()
	const body = "body{color:red}"
	mkfile(t, dir, "a.css", body)

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor()}
		case "write_files":
			return fakeReply{Text: `{"etags":{"a.css":"e9"},"written":1,"url":"https://example/x"}`}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})
	c := f.client()

	rep, err := runPush(context.Background(), c, pushOpts{projectID: "p1", dir: dir, concurrency: 4})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if !slices.Equal(rep.Written, []string{"a.css"}) {
		t.Errorf("written = %v, want [a.css]", rep.Written)
	}
	if rep.Bytes != int64(len(body)) {
		t.Errorf("bytes = %d, want %d", rep.Bytes, len(body))
	}

	sent := syncArgFiles(syncFirstCall(t, f, "write_files").Args)
	if len(sent) != 1 {
		t.Fatalf("sent %d files, want 1", len(sent))
	}
	if sent[0]["encoding"] != "base64" {
		t.Errorf("encoding = %v, want base64", sent[0]["encoding"])
	}
	data, _ := sent[0]["data"].(string)
	raw, decErr := base64.StdEncoding.DecodeString(data)
	if decErr != nil || string(raw) != body {
		t.Errorf("data decoded to %q (%v), want %q", raw, decErr, body)
	}
	// The path is new to us and absent from the listing, so the write must
	// assert that: a blind write would clobber a file created since the listing.
	if sent[0]["if_match"] != "0" {
		t.Errorf("if_match = %v, want \"0\" — a new path must assert it does not exist", sent[0]["if_match"])
	}

	st := syncLoadState(t, dir)
	if st.ProjectID != "p1" || st.Endpoint != c.endpoint {
		t.Errorf("ledger pin = %q/%q, want p1/%s", st.ProjectID, st.Endpoint, c.endpoint)
	}
	want := fileState{Etag: "e9", Size: int64(len(body)), SHA: sha256hex([]byte(body))}
	if got := st.Files["a.css"]; got != want {
		t.Errorf("a.css state = %+v, want %+v", got, want)
	}
}

// The pin has to be written before the first byte leaves, not after the last
// one lands. An error path that saved the ledger without it would leave
// project_id empty, and an empty pin short-circuits the guard: the next sync
// could aim these etags at a different project.
func TestPushPinsTheProjectIntoTheLedgerBeforeTheFirstWrite(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "a.css", "a{}")

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor()}
		case "write_files":
			return fakeReply{Text: "quota exceeded", IsError: true}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})
	c := f.client()

	if _, err := runPush(context.Background(), c, pushOpts{projectID: "p1", dir: dir, concurrency: 4}); err == nil {
		t.Fatal("push reported success although write_files failed")
	}
	if !syncLedgerExists(t, dir) {
		t.Fatal("no ledger was written on the error path")
	}
	st := syncLoadState(t, dir)
	if st.ProjectID != "p1" {
		t.Errorf("project_id = %q, want p1 — an empty pin is no pin", st.ProjectID)
	}
	if len(st.Files) != 0 {
		t.Errorf("ledger files = %v, want none — nothing was written", sortedPaths(st.Files))
	}
}

func TestPushSelfAuthorisesWithAPlanTokenWhenTheServerDemandsAGrant(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "a.css", "a{}")

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor()}
		case "finalize_plan":
			return fakeReply{Text: `{"plan_token":"tok-1"}`}
		case "write_files":
			if _, ok := args["plan_token"]; !ok {
				return fakeReply{
					HTTPStatus: http.StatusForbidden,
					HTTPBody:   `{"error":"needs_project_grant","project_id":"p1"}`,
				}
			}
			return fakeReply{Text: `{"etags":{"a.css":"e9"},"written":1}`}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	rep, err := runPush(context.Background(), f.client(), pushOpts{projectID: "p1", dir: dir, concurrency: 4})
	if err != nil {
		t.Fatalf("push: %v — a grant refusal is recoverable without a browser", err)
	}
	if !slices.Equal(rep.Written, []string{"a.css"}) {
		t.Errorf("written = %v, want [a.css]", rep.Written)
	}
	if got := syncToolOrder(f); !slices.Equal(got, []string{"list_files", "write_files", "finalize_plan", "write_files"}) {
		t.Errorf("calls = %v, want the grant refusal answered by a plan_token and one retry", got)
	}

	// The token must authorise exactly the paths in this batch: a
	// project-scoped one would hand the retry more reach than the write needs.
	plan := syncFirstCall(t, f, "finalize_plan")
	writes, _ := plan.Args["writes"].([]any)
	if len(writes) != 1 || writes[0] != "a.css" {
		t.Errorf("finalize_plan writes = %v, want [a.css]", plan.Args["writes"])
	}
	if plan.Args["project_id"] != "p1" {
		t.Errorf("finalize_plan project_id = %v, want p1", plan.Args["project_id"])
	}
}

func TestPushReportsBothFailuresWhenItCannotSelfAuthorise(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "a.css", "a{}")

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor()}
		case "finalize_plan":
			return fakeReply{Text: "plans are disabled for this project", IsError: true}
		case "write_files":
			return fakeReply{
				HTTPStatus: http.StatusForbidden,
				HTTPBody:   `{"error":"needs_project_grant","project_id":"p1"}`,
			}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	_, err := runPush(context.Background(), f.client(), pushOpts{projectID: "p1", dir: dir, concurrency: 4})
	if err == nil {
		t.Fatal("push reported success although it was never authorised")
	}
	// Only the first failure would send the user to a browser they do not need;
	// only the second would hide why authorisation was wanted at all.
	for _, want := range []string{"needs_project_grant", "could not self-authorise"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}
}

func TestPushRefusesToRecordAnEtagItNeverSaw(t *testing.T) {
	cases := []struct {
		name  string
		reply string
	}{
		{"prose instead of json", "Wrote 1 file to the project."},
		{"json without etags", `{"written":1,"url":"https://example/x"}`},
		{"a list where a map belongs", `[{"path":"a.css","etag":"e9"}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			mkfile(t, dir, "a.css", "a{}")

			f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
				switch name {
				case "list_files":
					return fakeReply{Text: listingFor()}
				case "write_files":
					return fakeReply{Text: tc.reply}
				}
				return fakeReply{Text: "unexpected " + name, IsError: true}
			})

			rep, err := runPush(context.Background(), f.client(), pushOpts{
				projectID: "p1", dir: dir, concurrency: 4,
			})
			if err == nil {
				t.Fatal("push accepted a reply it could not read")
			}
			// The bytes may well be up. Saying so, and naming the way out, is
			// the difference between a recoverable state and a mystery.
			if !strings.Contains(err.Error(), "etags were not recorded") {
				t.Errorf("error = %v, want it to say the ledger is behind the bytes", err)
			}
			if !strings.Contains(err.Error(), "dsx pull") {
				t.Errorf("error = %v, want it to name the way back to a synchronised state", err)
			}
			if len(rep.Written) != 0 {
				t.Errorf("written = %v, want none reported", rep.Written)
			}
			if _, tracked := syncLoadState(t, dir).Files["a.css"]; tracked {
				t.Error("an etag we never saw reached the ledger")
			}
		})
	}
}

func TestPushDryRunSendsNothingAndLeavesNoLedger(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "a.css", "body{}")

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor()}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	rep, err := runPush(context.Background(), f.client(), pushOpts{
		projectID: "p1", dir: dir, concurrency: 4, dryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.Written, []string{"a.css"}) {
		t.Errorf("written = %v, want [a.css] reported", rep.Written)
	}
	if n := f.countTool("write_files"); n != 0 {
		t.Errorf("write_files calls = %d, want 0", n)
	}
	if syncLedgerExists(t, dir) {
		t.Error("a dry run pinned the directory")
	}
}

func TestPushPruneDeletesWithTheLedgersEtagAndThenForgetsThePath(t *testing.T) {
	dir := t.TempDir()
	syncSeedState(t, dir, syncState{ProjectID: "p1", Files: map[string]fileState{
		"gone.css": {Etag: "e1", Size: 3, SHA: sha256hex([]byte("a{}"))},
	}})

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("gone.css", "e1", 3))}
		case "finalize_plan":
			return fakeReply{Text: `{"plan_token":"tok-del"}`}
		case "delete_files":
			return fakeReply{Text: `{"deleted":1}`}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	rep, err := runPush(context.Background(), f.client(), pushOpts{
		projectID: "p1", dir: dir, concurrency: 4, prune: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.Deleted, []string{"gone.css"}) {
		t.Errorf("deleted = %v, want [gone.css]", rep.Deleted)
	}

	plan := syncFirstCall(t, f, "finalize_plan")
	deletes, _ := plan.Args["deletes"].([]any)
	if len(deletes) != 1 || deletes[0] != "gone.css" {
		t.Errorf("finalize_plan deletes = %v, want [gone.css]", plan.Args["deletes"])
	}

	del := syncFirstCall(t, f, "delete_files")
	if del.Args["plan_token"] != "tok-del" {
		t.Errorf("delete_files plan_token = %v, want tok-del", del.Args["plan_token"])
	}
	sent := syncArgFiles(del.Args)
	if len(sent) != 1 || sent[0]["path"] != "gone.css" {
		t.Fatalf("delete_files files = %v, want gone.css", sent)
	}
	// Without if_match the delete is unconditional, so a file changed since our
	// listing would be removed on the strength of a stale view.
	if sent[0]["if_match"] != "e1" {
		t.Errorf("if_match = %v, want e1 from the ledger", sent[0]["if_match"])
	}
	if _, tracked := syncLoadState(t, dir).Files["gone.css"]; tracked {
		t.Error("gone.css is still in the ledger after being deleted from the server")
	}
}

// The reply is server-controlled, so the paths in it are untrusted input just
// as a listing's are. Only the paths we sent may enter the ledger: an entry for
// anything else is a claim about a file dsx never read, and `pull --prune`
// reads the ledger to decide what it may delete from disk.
func TestPushIgnoresAnEtagForAPathItDidNotSend(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "a.css", "a{}")

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor()}
		case "write_files":
			return fakeReply{Text: `{"etags":{"a.css":"e9","../../evil.css":"e1","never-sent.css":"e2"},"written":3}`}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	rep, err := runPush(context.Background(), f.client(), pushOpts{projectID: "p1", dir: dir, concurrency: 4})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.Written, []string{"a.css"}) {
		t.Errorf("written = %v, want only the file we sent", rep.Written)
	}
	if got := sortedPaths(syncLoadState(t, dir).Files); !slices.Equal(got, []string{"a.css"}) {
		t.Errorf("ledger files = %v, want only [a.css]", got)
	}
}

// Invariant 5 on push's other error path. The writes really did land; a delete
// that fails afterwards must not discard the record of them, and must not
// pretend the file it failed to delete is gone.
func TestPushKeepsTheLedgerWhenTheDeleteFails(t *testing.T) {
	dir := t.TempDir()
	const body = "a{}"
	mkfile(t, dir, "a.css", body)
	syncSeedState(t, dir, syncState{ProjectID: "p1", Files: map[string]fileState{
		"gone.css": {Etag: "e1", Size: 5, SHA: sha256hex([]byte("gone!"))},
	}})

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("gone.css", "e1", 5))}
		case "write_files":
			return fakeReply{Text: `{"etags":{"a.css":"e9"},"written":1}`}
		case "finalize_plan":
			return fakeReply{Text: "plan quota exhausted", IsError: true}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	if _, err := runPush(context.Background(), f.client(), pushOpts{
		projectID: "p1", dir: dir, concurrency: 4, prune: true,
	}); err == nil {
		t.Fatal("push reported success although the delete was never authorised")
	}
	if n := f.countTool("delete_files"); n != 0 {
		t.Errorf("delete_files calls = %d, want 0 — no token, no delete", n)
	}

	st := syncLoadState(t, dir)
	want := fileState{Etag: "e9", Size: int64(len(body)), SHA: sha256hex([]byte(body))}
	if got := st.Files["a.css"]; got != want {
		t.Errorf("a.css state = %+v, want %+v — the write landed and must stay recorded", got, want)
	}
	if _, tracked := st.Files["gone.css"]; !tracked {
		t.Error("gone.css was dropped from the ledger although it is still on the server")
	}
}

// A batch that failed must not take the ledger for the batches that succeeded
// down with it: those files really are on the server, at the etags reported.
func TestPushKeepsTheLedgerForBatchesThatLandedBeforeOneFailed(t *testing.T) {
	dir := t.TempDir()
	const total = maxBatchFiles + 1
	for i := range total {
		mkfile(t, dir, fmt.Sprintf("f%03d.css", i), "a{}")
	}

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor()}
		case "write_files":
			sent := syncArgFiles(args)
			if len(sent) != maxBatchFiles {
				return fakeReply{Text: "the second batch is rejected", IsError: true}
			}
			etags := map[string]string{}
			for _, m := range sent {
				p, _ := m["path"].(string)
				etags[p] = "e-" + p
			}
			b, err := json.Marshal(writeResult{Etags: etags, Written: len(etags)})
			if err != nil {
				return fakeReply{Text: err.Error(), IsError: true}
			}
			return fakeReply{Text: string(b)}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	rep, err := runPush(context.Background(), f.client(), pushOpts{projectID: "p1", dir: dir, concurrency: 4})
	if err == nil {
		t.Fatal("push reported success although the second batch failed")
	}
	if len(rep.Written) != maxBatchFiles {
		t.Errorf("written = %d, want %d", len(rep.Written), maxBatchFiles)
	}
	st := syncLoadState(t, dir)
	if len(st.Files) != maxBatchFiles {
		t.Fatalf("ledger files = %d, want %d — the first batch is on the server either way",
			len(st.Files), maxBatchFiles)
	}
	last := fmt.Sprintf("f%03d.css", total-1)
	if _, tracked := st.Files[last]; tracked {
		t.Errorf("%s is in the ledger although its batch was rejected", last)
	}
}

// ---------------------------------------------------------------------------
// reports
// ---------------------------------------------------------------------------

func TestPullReportRender(t *testing.T) {
	full := pullReport{
		Fetched: []string{"a.css", "b.css"}, Unchanged: 3,
		Deleted: []string{"d.css"}, Conflicts: []string{"c.css"},
		Binary: []string{"logo.png"}, Bytes: 2048,
	}

	t.Run("json round-trips every field", func(t *testing.T) {
		var got pullReport
		if err := json.Unmarshal([]byte(full.render(true)), &got); err != nil {
			t.Fatalf("--json output is not JSON: %v", err)
		}
		if !reflect.DeepEqual(got, full) {
			t.Errorf("round-trip = %+v, want %+v", got, full)
		}
	})

	t.Run("prose names every path a human has to act on", func(t *testing.T) {
		want := "pulled 2, unchanged 3, deleted 1, conflicts 1, binary 1 (2.0 KB)" +
			"\n  ! c.css — local differs; --force to overwrite" +
			"\n  ~ 1 binary file(s) skipped — read_file serves text only: logo.png"
		if got := full.render(false); got != want {
			t.Errorf("render:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("a quiet run says only what happened", func(t *testing.T) {
		// Counts that are zero must not appear: the summary line is a token
		// budget, and "deleted 0" invites a reader to wonder what was deleted.
		got := pullReport{Unchanged: 4}.render(false)
		if got != "pulled 0, unchanged 4 (0 B)" {
			t.Errorf("render = %q", got)
		}
	})
}

func TestPushReportRender(t *testing.T) {
	full := pushReport{
		Written: []string{"a.css"}, Unchanged: 2,
		Deleted: []string{"d.css"}, Conflicts: []string{"c.css"}, Bytes: 1536,
	}

	t.Run("json round-trips every field", func(t *testing.T) {
		var got pushReport
		if err := json.Unmarshal([]byte(full.render(true)), &got); err != nil {
			t.Fatalf("--json output is not JSON: %v", err)
		}
		if !reflect.DeepEqual(got, full) {
			t.Errorf("round-trip = %+v, want %+v", got, full)
		}
	})

	t.Run("prose names the conflict and the way out", func(t *testing.T) {
		want := "pushed 1, unchanged 2, deleted 1, conflicts 1 (1.5 KB)" +
			"\n  ! c.css — server moved ahead; `dsx pull` first, or --force"
		if got := full.render(false); got != want {
			t.Errorf("render:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("a quiet run says only what happened", func(t *testing.T) {
		got := pushReport{Unchanged: 9}.render(false)
		if got != "pushed 0, unchanged 9 (0 B)" {
			t.Errorf("render = %q", got)
		}
	})
}
