package syncer

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

	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/mcp"
	"github.com/somework/dsx/internal/mcptest"
)

func syncLoadState(t *testing.T, dir string) State {
	t.Helper()
	st, err := LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	return st
}

func syncSeedState(t *testing.T, dir string, st State) {
	t.Helper()
	if st.Files == nil {
		st.Files = map[string]FileState{}
	}
	if err := st.save(dir); err != nil {
		t.Fatalf("seeding ledger: %v", err)
	}
}

func syncLedgerExists(t *testing.T, dir string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, StateFileName))
	return err == nil
}

func syncExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

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

func syncFirstCall(t *testing.T, f *fakeMCP, tool string) mcptest.Call {
	t.Helper()
	for _, c := range f.Recorded() {
		if c.Tool == tool {
			return c
		}
	}
	t.Fatalf("%s was never called; calls: %v", tool, f.Recorded())
	return mcptest.Call{}
}

func syncToolOrder(f *fakeMCP) []string {
	var out []string
	for _, c := range f.Recorded() {
		if c.Tool != "" {
			out = append(out, c.Tool)
		}
	}
	return out
}

func syncWaitForPath(path string) bool {
	for range 4000 {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

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

func syncCancelAfter(t *testing.T, f *fakeMCP, n int64) (*mcp.Client, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c := mcp.New("test-token",
		mcp.WithEndpoint(f.URL()),
		mcp.WithHTTPClient(&http.Client{Transport: &syncCancelAfterRT{
			base: http.DefaultTransport, after: n, cancel: cancel,
		}}))
	return c, ctx
}

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

	got, err := WalkTree(context.Background(), fakeClient(f), "p1", 4)
	if err != nil {
		t.Fatalf("WalkTree: %v", err)
	}
	want := []string{"index.css", "tokens/colors.css", "tokens/deep/x.css"}
	if !slices.Equal(SortedPaths(got), want) {
		t.Errorf("paths = %v, want %v — keys must be project-relative, not basenames", SortedPaths(got), want)
	}

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

	got, err := WalkTree(context.Background(), fakeClient(f), "p1", 4)
	if err != nil {
		t.Fatalf("WalkTree: %v", err)
	}
	if !slices.Equal(SortedPaths(got), []string{"x/1.css", "y/1.css"}) {
		t.Errorf("paths = %v, want both siblings", SortedPaths(got))
	}
}

func TestWalkTreeInterruptedBetweenListingsIsAFailureNotAShortListing(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor(fileEntry("a.css", "e1", 3))}
	})
	c, ctx := syncCancelAfter(t, f, 1)

	got, err := WalkTree(ctx, c, "p1", 4)
	if err == nil {
		t.Fatalf("WalkTree returned %v and no error — an interrupted walk must not report success", SortedPaths(got))
	}
	if got != nil {
		t.Errorf("listing = %v, want nil — a caller must not be handed a tree it can diff against", SortedPaths(got))
	}
	if f.CountTool("list_files") != 1 {
		t.Fatalf("list_files calls = %d, want 1 — the scenario relies on there being no failing call",
			f.CountTool("list_files"))
	}
}

func TestWalkTreeReturnsNoListingWhenADirectoryFails(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if args["path"] == nil {
			return fakeReply{Text: listingFor(fileEntry("a.css", "e1", 3), dirEntry("bad"))}
		}
		return fakeReply{Text: "permission denied", IsError: true}
	})

	got, err := WalkTree(context.Background(), fakeClient(f), "p1", 4)
	if err == nil {
		t.Fatal("WalkTree succeeded despite a failed directory")
	}

	if got != nil {
		t.Errorf("listing = %v, want nil alongside the error", SortedPaths(got))
	}
}

func TestListDirOmitsThePathForTheRootAndSendsItForASubdirectory(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})
	c := fakeClient(f)

	if _, err := listDir(context.Background(), c, "p1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := listDir(context.Background(), c, "p1", "tokens"); err != nil {
		t.Fatal(err)
	}

	calls := f.Recorded()
	if _, ok := calls[0].Args["path"]; ok {
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
	entries, err := listDir(context.Background(), fakeClient(f), "p1", "")
	if err == nil {
		t.Fatalf("listDir accepted garbage and returned %v — an empty tree drives --prune", entries)
	}
	if !strings.Contains(err.Error(), "malformed listing") {
		t.Errorf("error = %v, want it to name the malformed listing", err)
	}
	// A listing dsx cannot parse is the server breaking its half of the
	// protocol, which the taxonomy already names. Falling back to the generic
	// "error" token tells an agent nothing it can branch on.
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindProtocol {
		t.Errorf("kind = %q, want %q", got, dsxerr.KindProtocol)
	}
}

func TestReadFullConcatenatesEveryWindowAndAsksForTheNextByLine(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name != "read_file" {
			return fakeReply{Text: "unexpected " + name, IsError: true}
		}
		if _, windowed := args["offset"]; !windowed {
			return fakeReply{Text: syncWindow("a.css", "e1", 1, 2, 4, "alpha\nbeta\n")}
		}
		return fakeReply{Text: syncWindow("a.css", "e1", 3, 4, 4, "gamma\ndelta\n")}
	})

	body, etag, err := fakeClient(f).ReadFull(context.Background(), "p1", "a.css")
	if err != nil {
		t.Fatalf("readFull: %v", err)
	}
	if want := "alpha\nbeta\ngamma\ndelta\n"; body != want {
		t.Errorf("body = %q, want %q — the windows must be joined in order, with no server prose between them", body, want)
	}
	if etag != "e1" {
		t.Errorf("etag = %q, want e1", etag)
	}
	calls := f.Recorded()
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
	body, _, err := fakeClient(f).ReadFull(context.Background(), "p1", "a.css")
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

func TestReadFullRefusesAnEtagThatChangesMidRead(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if _, windowed := args["offset"]; !windowed {
			return fakeReply{Text: syncWindow("a.css", "e1", 1, 2, 4, "alpha\nbeta\n")}
		}
		return fakeReply{Text: syncWindow("a.css", "e2", 3, 4, 4, "gamma\ndelta\n")}
	})
	body, etag, err := fakeClient(f).ReadFull(context.Background(), "p1", "a.css")
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
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: syncWindow("a.css", "e1", 1, 2, 4, "alpha\nbeta\n")}
	})
	_, _, err := fakeClient(f).ReadFull(context.Background(), "p1", "a.css")
	if err == nil {
		t.Fatal("readFull accepted a window that repeated itself")
	}
	if !strings.Contains(err.Error(), "no progress") {
		t.Errorf("error = %v, want it to name the stall", err)
	}
	if n := f.CountTool("read_file"); n != 2 {
		t.Errorf("read_file calls = %d, want 2 — the loop must break on the repeat", n)
	}
}

func TestPullRefusesToWriteAFileWhoseDecodedLengthDisagreesWithTheListing(t *testing.T) {
	dir := t.TempDir()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("a.css", "e1", 100))}
		case "read_file":
			return fakeReply{Text: envelopeFor("a.css", "e1", "hi")}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4,
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

	if _, tracked := syncLoadState(t, dir).Files["a.css"]; tracked {
		t.Error("the ledger records a.css — the next run would call it unchanged")
	}
}

func TestPullSavesTheLedgerForAFileAlreadyWrittenWhenALaterFetchFails(t *testing.T) {
	dir := t.TempDir()
	const bodyA = "body{color:red}"

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(
				fileEntry("a.css", "e1", int64(len(bodyA))),
				fileEntry("b.css", "e2", 999),
			)}
		case "read_file":
			if args["path"] == "a.css" {
				return fakeReply{Text: envelopeFor("a.css", "e1", bodyA)}
			}

			if !syncWaitForPath(filepath.Join(dir, "a.css")) {
				return fakeReply{Text: "a.css never landed", IsError: true}
			}
			return fakeReply{Text: envelopeFor("b.css", "e2", "short")}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	_, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4,
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
		t.Fatalf("ledger files = %v, want a.css recorded", SortedPaths(st.Files))
	}
	want := FileState{Etag: "e1", Size: int64(len(bodyA)), SHA: SHA256Hex([]byte(bodyA))}
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

	c, ctx := syncCancelAfter(t, f, 2)

	rep, err := Pull(ctx, c, PullOpts{ProjectID: "p1", Dir: dir, Concurrency: 4})
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

func TestPushInterruptedAfterTheDeletesIsAFailureNotAShortSuccess(t *testing.T) {
	dir := t.TempDir()
	syncSeedState(t, dir, State{ProjectID: "p1", Files: map[string]FileState{
		"gone.css": {Etag: "e1", Size: 3, SHA: SHA256Hex([]byte("a{}"))},
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

	// list_files, finalize_plan, delete_files — the cancel lands after the last
	// server call of the run has already returned.
	c, ctx := syncCancelAfter(t, f, 3)

	rep, err := Push(ctx, c, PushOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4, Prune: true,
	})
	if err == nil {
		t.Fatalf("push reported success (%+v) after being interrupted", rep)
	}
	if !strings.Contains(err.Error(), "interrupted") {
		t.Errorf("error = %v, want it to name the interruption", err)
	}
	if _, still := syncLoadState(t, dir).Files["gone.css"]; still {
		t.Error("gone.css is still in the ledger though the server acknowledged its delete")
	}
}

func TestPushInterruptedBeforeThePruneDeletesSendsNoDelete(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "a.css", "a{}")
	syncSeedState(t, dir, State{ProjectID: "p1", Files: map[string]FileState{
		"gone.css": {Etag: "e1", Size: 3, SHA: SHA256Hex([]byte("a{}"))},
	}})

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("gone.css", "e1", 3))}
		case "write_files":
			return fakeReply{Text: `{"etags":{"a.css":"e2"},"written":1}`}
		case "finalize_plan":
			return fakeReply{Text: `{"plan_token":"tok-del"}`}
		case "delete_files":
			return fakeReply{Text: `{"deleted":1}`}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	c, ctx := syncCancelAfter(t, f, 2)

	rep, err := Push(ctx, c, PushOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4, Prune: true,
	})
	if err == nil {
		t.Fatalf("push reported success (%+v) after being interrupted", rep)
	}
	if !strings.Contains(err.Error(), "interrupted") {
		t.Errorf("error = %v, want it to name the interruption", err)
	}
	// This loop does not prove invariant 3: the cancelled call dies inside the
	// transport and never reaches the fake, so delete_files/finalize_plan would
	// be absent from f.Recorded() even if push.go ignored the cancellation. The
	// "interrupted" check above is what reds on a real violation.
	for _, call := range f.Recorded() {
		if call.Tool == "delete_files" || call.Tool == "finalize_plan" {
			t.Errorf("%s ran after the push was interrupted; --prune deleted against a tree the user cancelled", call.Tool)
		}
	}
	if got := syncLoadState(t, dir).Files["a.css"].Etag; got != "e2" {
		t.Errorf("a.css etag = %q, want e2 — the acknowledged write must survive the error path", got)
	}
	if _, still := syncLoadState(t, dir).Files["gone.css"]; !still {
		t.Error("gone.css was dropped from the ledger though no delete was sent")
	}
}

func TestPullRefusesAProjectTheDirectoryIsNotBoundTo(t *testing.T) {
	dir := t.TempDir()
	syncSeedState(t, dir, State{ProjectID: "project-a"})

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})
	_, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "project-b", Dir: dir, Concurrency: 4,
	})
	if err == nil {
		t.Fatal("pull crossed the pin")
	}
	if kind := dsxerr.Classify(err).Kind; kind != dsxerr.KindUsage {
		t.Errorf("Kind = %q, want %q — a directory bound to another project is a usage error", kind, dsxerr.KindUsage)
	}
	for _, want := range []string{"project-a", "project-b"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to name %q", err, want)
		}
	}
	if n := len(f.Recorded()); n != 0 {
		t.Errorf("%d requests were made before the pin was checked, want 0", n)
	}
}

func TestPushRefusesAProjectTheDirectoryIsNotBoundTo(t *testing.T) {
	dir := t.TempDir()
	syncSeedState(t, dir, State{ProjectID: "project-a"})

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})
	_, err := Push(context.Background(), fakeClient(f), PushOpts{
		ProjectID: "project-b", Dir: dir, Concurrency: 4,
	})
	if err == nil {
		t.Fatal("push crossed the pin")
	}
	if kind := dsxerr.Classify(err).Kind; kind != dsxerr.KindUsage {
		t.Errorf("Kind = %q, want %q — a directory bound to another project is a usage error", kind, dsxerr.KindUsage)
	}
	for _, want := range []string{"project-a", "project-b"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to name %q", err, want)
		}
	}
	if n := len(f.Recorded()); n != 0 {
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

		rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
			ProjectID: "p1", Dir: dir, Concurrency: 4,
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
		want := FileState{Etag: "e1", Binary: true}
		if got != want {
			t.Errorf("ledger entry = %+v, want %+v — the refusal is remembered against the etag", got, want)
		}
	})

	t.Run("the same etag is not asked for again", func(t *testing.T) {
		dir := t.TempDir()
		syncSeedState(t, dir, State{ProjectID: "p1", Files: map[string]FileState{
			"logo.png": {Etag: "e1", Binary: true},
		}})
		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			if name == "list_files" {
				return fakeReply{Text: listingFor(fileEntry("logo.png", "e1", 67))}
			}
			return fakeReply{Text: "unexpected " + name, IsError: true}
		})

		rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
			ProjectID: "p1", Dir: dir, Concurrency: 4,
		})
		if err != nil {
			t.Fatal(err)
		}
		if n := f.CountTool("read_file"); n != 0 {
			t.Errorf("read_file calls = %d, want 0 — a known refusal at a known etag costs nothing", n)
		}
		if !slices.Equal(rep.Binary, []string{"logo.png"}) {
			t.Errorf("binary = %v, want [logo.png] — the file must still be reported", rep.Binary)
		}
	})

	t.Run("a new etag re-tries it", func(t *testing.T) {
		dir := t.TempDir()
		syncSeedState(t, dir, State{ProjectID: "p1", Files: map[string]FileState{
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

		rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
			ProjectID: "p1", Dir: dir, Concurrency: 4,
		})
		if err != nil {
			t.Fatal(err)
		}
		if n := f.CountTool("read_file"); n != 1 {
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

func TestPullTreatsANonBinaryFetchFailureAsAnErrorNotAsABinaryFile(t *testing.T) {
	cases := []struct {
		name  string
		reply fakeReply
	}{
		{"a tool error that is not the binary refusal",
			fakeReply{Text: "a.css: no such path in this project", IsError: true}},

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

			rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
				ProjectID: "p1", Dir: dir, Concurrency: 4,
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

	_, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4,
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

	rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4, DryRun: true,
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
	if n := f.CountTool("read_file"); n != 0 {
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
	syncSeedState(t, dir, State{ProjectID: "p1", Files: map[string]FileState{
		"gone.css": {Etag: "e1", Size: int64(len(body)), SHA: SHA256Hex([]byte(body))},
	}})

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor()}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4, Prune: true,
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
	c := fakeClient(f)

	rep, err := Push(context.Background(), c, PushOpts{ProjectID: "p1", Dir: dir, Concurrency: 4})
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

	if sent[0]["if_match"] != "0" {
		t.Errorf("if_match = %v, want \"0\" — a new path must assert it does not exist", sent[0]["if_match"])
	}

	st := syncLoadState(t, dir)
	if st.ProjectID != "p1" || st.Endpoint != c.Endpoint() {
		t.Errorf("ledger pin = %q/%q, want p1/%s", st.ProjectID, st.Endpoint, c.Endpoint())
	}
	want := FileState{Etag: "e9", Size: int64(len(body)), SHA: SHA256Hex([]byte(body))}
	if got := st.Files["a.css"]; got != want {
		t.Errorf("a.css state = %+v, want %+v", got, want)
	}
}

func TestPushInterruptedAfterAWriteIsAFailureThatStillRecordsTheWrite(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "a.css", "a{}")

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor()}
		case "write_files":
			return fakeReply{Text: `{"etags":{"a.css":"e1"},"written":1}`}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	c, ctx := syncCancelAfter(t, f, 2)

	rep, err := Push(ctx, c, PushOpts{ProjectID: "p1", Dir: dir, Concurrency: 4})
	if err == nil {
		t.Fatalf("push reported success (%+v) after being interrupted", rep)
	}
	if !strings.Contains(err.Error(), "interrupted") {
		t.Errorf("error = %v, want it to name the interruption", err)
	}
	if got := syncLoadState(t, dir).Files["a.css"].Etag; got != "e1" {
		t.Errorf("a.css etag = %q, want e1 — the acknowledged write must survive the error path", got)
	}
}

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
	c := fakeClient(f)

	if _, err := Push(context.Background(), c, PushOpts{ProjectID: "p1", Dir: dir, Concurrency: 4}); err == nil {
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
		t.Errorf("ledger files = %v, want none — nothing was written", SortedPaths(st.Files))
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

	rep, err := Push(context.Background(), fakeClient(f), PushOpts{ProjectID: "p1", Dir: dir, Concurrency: 4})
	if err != nil {
		t.Fatalf("push: %v — a grant refusal is recoverable without a browser", err)
	}
	if !slices.Equal(rep.Written, []string{"a.css"}) {
		t.Errorf("written = %v, want [a.css]", rep.Written)
	}
	if got := syncToolOrder(f); !slices.Equal(got, []string{"list_files", "write_files", "finalize_plan", "write_files"}) {
		t.Errorf("calls = %v, want the grant refusal answered by a plan_token and one retry", got)
	}

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

	_, err := Push(context.Background(), fakeClient(f), PushOpts{ProjectID: "p1", Dir: dir, Concurrency: 4})
	if err == nil {
		t.Fatal("push reported success although it was never authorised")
	}

	for _, want := range []string{"needs_project_grant", "could not self-authorise"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to mention %q", err, want)
		}
	}
}

// A self-authorisation that fails for a reason the taxonomy already names must
// keep that name. The grant refusal itself is terminal, but the finalize_plan
// failure underneath it is the actionable half: a 5xx says "retry may succeed",
// a 401 says "refresh the token". Flattening both into exit 1 tells an agent
// "it failed" and nothing else.
func TestSelfAuthorisationFailureKeepsItsKind(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{"finalize_plan hits a 5xx", http.StatusServiceUnavailable, "upstream is down", dsxerr.ExitTransport},
		{"finalize_plan is rejected", http.StatusUnauthorized, "token rejected", dsxerr.ExitAuth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			mkfile(t, dir, "a.css", "a{}")

			f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
				switch name {
				case "list_files":
					return fakeReply{Text: listingFor()}
				case "finalize_plan":
					return fakeReply{HTTPStatus: tc.status, HTTPBody: tc.body}
				case "write_files":
					return fakeReply{
						HTTPStatus: http.StatusForbidden,
						HTTPBody:   `{"error":"needs_project_grant","project_id":"p1"}`,
					}
				}
				return fakeReply{Text: "unexpected " + name, IsError: true}
			})

			_, err := Push(context.Background(), fakeClient(f), PushOpts{ProjectID: "p1", Dir: dir, Concurrency: 4})
			if err == nil {
				t.Fatal("push reported success although it was never authorised")
			}
			if got := dsxerr.ExitCodeFor(err); got != tc.want {
				t.Errorf("exit = %d, want %d — the kind inside the self-authorisation failure was flattened",
					got, tc.want)
			}
		})
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

			rep, err := Push(context.Background(), fakeClient(f), PushOpts{
				ProjectID: "p1", Dir: dir, Concurrency: 4,
			})
			if err == nil {
				t.Fatal("push accepted a reply it could not read")
			}

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
			// The same failure class as the unacknowledged-etag branch below
			// it, and it must carry the same name and the same paths: the
			// reply was unreadable, so every path in the batch is absent from
			// the ledger.
			de := dsxerr.Classify(err)
			if de.Kind != dsxerr.KindProtocol {
				t.Errorf("kind = %q, want %q", de.Kind, dsxerr.KindProtocol)
			}
			if !slices.Contains(de.Paths, "a.css") {
				t.Errorf("paths = %v, want it to name a.css", de.Paths)
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

	rep, err := Push(context.Background(), fakeClient(f), PushOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4, DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.Written, []string{"a.css"}) {
		t.Errorf("written = %v, want [a.css] reported", rep.Written)
	}
	if n := f.CountTool("write_files"); n != 0 {
		t.Errorf("write_files calls = %d, want 0", n)
	}
	if syncLedgerExists(t, dir) {
		t.Error("a dry run pinned the directory")
	}
}

func TestPushPruneDeletesWithTheLedgersEtagAndThenForgetsThePath(t *testing.T) {
	dir := t.TempDir()
	syncSeedState(t, dir, State{ProjectID: "p1", Files: map[string]FileState{
		"gone.css": {Etag: "e1", Size: 3, SHA: SHA256Hex([]byte("a{}"))},
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

	rep, err := Push(context.Background(), fakeClient(f), PushOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4, Prune: true,
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

	if sent[0]["if_match"] != "e1" {
		t.Errorf("if_match = %v, want e1 from the ledger", sent[0]["if_match"])
	}
	if _, tracked := syncLoadState(t, dir).Files["gone.css"]; tracked {
		t.Error("gone.css is still in the ledger after being deleted from the server")
	}
}

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

	rep, err := Push(context.Background(), fakeClient(f), PushOpts{ProjectID: "p1", Dir: dir, Concurrency: 4})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.Written, []string{"a.css"}) {
		t.Errorf("written = %v, want only the file we sent", rep.Written)
	}
	if got := SortedPaths(syncLoadState(t, dir).Files); !slices.Equal(got, []string{"a.css"}) {
		t.Errorf("ledger files = %v, want only [a.css]", got)
	}
}

func TestPushKeepsTheLedgerWhenTheDeleteFails(t *testing.T) {
	dir := t.TempDir()
	const body = "a{}"
	mkfile(t, dir, "a.css", body)
	syncSeedState(t, dir, State{ProjectID: "p1", Files: map[string]FileState{
		"gone.css": {Etag: "e1", Size: 5, SHA: SHA256Hex([]byte("gone!"))},
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

	if _, err := Push(context.Background(), fakeClient(f), PushOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4, Prune: true,
	}); err == nil {
		t.Fatal("push reported success although the delete was never authorised")
	}
	if n := f.CountTool("delete_files"); n != 0 {
		t.Errorf("delete_files calls = %d, want 0 — no token, no delete", n)
	}

	st := syncLoadState(t, dir)
	want := FileState{Etag: "e9", Size: int64(len(body)), SHA: SHA256Hex([]byte(body))}
	if got := st.Files["a.css"]; got != want {
		t.Errorf("a.css state = %+v, want %+v — the write landed and must stay recorded", got, want)
	}
	if _, tracked := st.Files["gone.css"]; !tracked {
		t.Error("gone.css was dropped from the ledger although it is still on the server")
	}
}

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

	rep, err := Push(context.Background(), fakeClient(f), PushOpts{ProjectID: "p1", Dir: dir, Concurrency: 4})
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

func TestPullReportRender(t *testing.T) {
	full := PullReport{
		Fetched: []string{"a.css", "b.css"}, Unchanged: 3,
		Deleted: []string{"d.css"}, Conflicts: []string{"c.css"},
		Binary: []string{"logo.png"}, Bytes: 2048,
	}

	t.Run("json round-trips every field", func(t *testing.T) {
		var got PullReport
		if err := json.Unmarshal([]byte(full.Render(true)), &got); err != nil {
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
		if got := full.Render(false); got != want {
			t.Errorf("render:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("a quiet run says only what happened", func(t *testing.T) {
		got := PullReport{Unchanged: 4}.Render(false)
		if got != "pulled 0, unchanged 4 (0 B)" {
			t.Errorf("render = %q", got)
		}
	})
}

func TestPushReportRender(t *testing.T) {
	full := PushReport{
		Written: []string{"a.css"}, Unchanged: 2,
		Deleted: []string{"d.css"}, Conflicts: []string{"c.css"}, Bytes: 1536,
	}

	t.Run("json round-trips every field", func(t *testing.T) {
		var got PushReport
		if err := json.Unmarshal([]byte(full.Render(true)), &got); err != nil {
			t.Fatalf("--json output is not JSON: %v", err)
		}
		if !reflect.DeepEqual(got, full) {
			t.Errorf("round-trip = %+v, want %+v", got, full)
		}
	})

	t.Run("prose names the conflict and the way out", func(t *testing.T) {
		want := "pushed 1, unchanged 2, deleted 1, conflicts 1 (1.5 KB)" +
			"\n  ! c.css — server moved ahead; `dsx pull` first, or --force"
		if got := full.Render(false); got != want {
			t.Errorf("render:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("a quiet run says only what happened", func(t *testing.T) {
		got := PushReport{Unchanged: 9}.Render(false)
		if got != "pushed 0, unchanged 9 (0 B)" {
			t.Errorf("render = %q", got)
		}
	})
}
