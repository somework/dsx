package syncer

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestFetchWritesTheExpectedBaselineAndTouchesNoWorkingTreeFile is the
// positive control: it must go red against a `return nil` stub, not just
// against a broken one. It asserts BOTH that .dsx/baseline.json decodes to
// exactly the expected entries per path AND that every path outside .dsx/ is
// byte-identical (content and mtime) before and after, with no temp left at
// any depth.
func TestFetchWritesTheExpectedBaselineAndTouchesNoWorkingTreeFile(t *testing.T) {
	dir := t.TempDir()
	body := []byte("shared bytes\n")
	mkfile(t, dir, "readme.md", string(body))
	mkfile(t, dir, "sub/other.css", "css body\n")

	// before snapshot, taken before Fetch runs at all.
	type snap struct {
		body  []byte
		mtime time.Time
	}
	before := map[string]snap{}
	for _, rel := range []string{"readme.md", "sub/other.css"} {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		b, err := os.ReadFile(full)
		if err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(full)
		if err != nil {
			t.Fatal(err)
		}
		before[rel] = snap{body: b, mtime: fi.ModTime()}
	}

	otherBody := []byte("css body\n")
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(
				fileEntry("readme.md", "e1", int64(len(body))),
				fileEntry("sub/other.css", "e2", int64(len(otherBody))))}
		}
		p, _ := args["path"].(string)
		switch p {
		case "readme.md":
			return fakeReply{Text: envelopeFor(p, "e1", string(body))}
		case "sub/other.css":
			return fakeReply{Text: envelopeFor(p, "e2", string(otherBody))}
		}
		return fakeReply{Text: "unexpected path " + p, IsError: true}
	})
	c := fakeClient(f)

	rep, err := Fetch(context.Background(), c, FetchOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 2})
	if err != nil {
		t.Fatalf("Fetch errored: %v", err)
	}
	if len(rep.Fetched) != 2 {
		t.Fatalf("Fetched = %v, want 2 paths", rep.Fetched)
	}
	if wantBytes := int64(len(body) + len(otherBody)); rep.Bytes != wantBytes {
		t.Errorf("rep.Bytes = %d, want %d", rep.Bytes, wantBytes)
	}

	// Positive half: the baseline decodes to exactly the expected entries.
	bl, err := loadBaseline(dir)
	if err != nil {
		t.Fatalf("loadBaseline: %v", err)
	}
	want := map[string]BaselineEntry{
		"readme.md":     {Etag: "e1", Size: int64(len(body)), SHA: SHA256Hex(body)},
		"sub/other.css": {Etag: "e2", Size: int64(len(otherBody)), SHA: SHA256Hex(otherBody)},
	}
	if len(bl.Verified) != len(want) {
		t.Fatalf("baseline has %d entries, want %d: %+v", len(bl.Verified), len(want), bl.Verified)
	}
	for path, wantEntry := range want {
		got, ok := bl.Verified[path]
		if !ok {
			t.Errorf("baseline missing entry for %s", path)
			continue
		}
		if got != wantEntry {
			t.Errorf("baseline[%s] = %+v, want %+v", path, got, wantEntry)
		}
	}
	if bl.ProjectID != "proj-A" {
		t.Errorf("baseline ProjectID = %q, want proj-A", bl.ProjectID)
	}
	if bl.Endpoint != c.Endpoint() {
		t.Errorf("baseline Endpoint = %q, want %q — Push/Pull's bound() check reads this directly", bl.Endpoint, c.Endpoint())
	}

	// Negative half: nothing outside .dsx/ moved.
	for rel, snapBefore := range before {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		b, err := os.ReadFile(full)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != string(snapBefore.body) {
			t.Errorf("%s content changed", rel)
		}
		fi, err := os.Stat(full)
		if err != nil {
			t.Fatal(err)
		}
		if !fi.ModTime().Equal(snapBefore.mtime) {
			t.Errorf("%s mtime changed: %v -> %v", rel, snapBefore.mtime, fi.ModTime())
		}
	}

	// No temp left anywhere in the tree, including inside .dsx/.
	err = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}
		name := fi.Name()
		if name == "state.json" || name == "baseline.json" || name == "readme.md" || name == "other.css" {
			return nil
		}
		t.Errorf("unexpected file left behind: %s", p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestFetchWritesNoEntryWhenTheDecodedLengthDisagrees: invariant 1's
// extension to the baseline. A path whose downloaded length disagrees with
// the listing's size must get no entry — not a wrong one — and the run must
// still succeed for the paths that did check out.
func TestFetchWritesNoEntryWhenTheDecodedLengthDisagrees(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "bad.css", "whatever local bytes\n")
	goodBody := []byte("good\n")
	mkfile(t, dir, "good.css", string(goodBody))

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(
				// listing claims 999 bytes; the server actually serves far fewer.
				fileEntry("bad.css", "eBad", 999),
				fileEntry("good.css", "eGood", int64(len(goodBody))))}
		}
		p, _ := args["path"].(string)
		switch p {
		case "bad.css":
			return fakeReply{Text: envelopeFor(p, "eBad", "short")}
		case "good.css":
			return fakeReply{Text: envelopeFor(p, "eGood", string(goodBody))}
		}
		return fakeReply{Text: "unexpected path " + p, IsError: true}
	})
	c := fakeClient(f)

	rep, err := Fetch(context.Background(), c, FetchOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 2})
	if err != nil {
		t.Fatalf("Fetch errored on a length mismatch: %v", err)
	}
	if !slices.Contains(rep.Skipped, "bad.css") {
		t.Errorf("Skipped = %v, want bad.css present", rep.Skipped)
	}
	if slices.Contains(rep.Fetched, "bad.css") {
		t.Errorf("Fetched = %v, bad.css must not be recorded", rep.Fetched)
	}
	if !slices.Contains(rep.Fetched, "good.css") {
		t.Errorf("Fetched = %v, want good.css present", rep.Fetched)
	}
	// bad.css's rejected bytes ("short", 5 bytes) must not be counted — this
	// also proves the skipped path's bytes are not folded into rep.Bytes.
	if want := int64(len(goodBody)); rep.Bytes != want {
		t.Errorf("rep.Bytes = %d, want %d (good.css only)", rep.Bytes, want)
	}

	bl, err := loadBaseline(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := bl.Verified["bad.css"]; ok {
		t.Errorf("baseline holds an entry for bad.css despite the length mismatch: %+v", bl.Verified["bad.css"])
	}
	if _, ok := bl.Verified["good.css"]; !ok {
		t.Errorf("baseline missing good.css")
	}
}

// TestFetchSkipsPathsIgnoredByDsxignore: survey is the only filter
// (invariant 9). An ignored path must never be downloaded or baselined, even
// though the raw server listing carries it.
func TestFetchSkipsPathsIgnoredByDsxignore(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, ".dsxignore", "*.secret\n")
	mkfile(t, dir, "keep.css", "keep\n")
	mkfile(t, dir, "drop.secret", "drop\n")

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(
				fileEntry("keep.css", "e1", 5),
				fileEntry("drop.secret", "e2", 5))}
		}
		p, _ := args["path"].(string)
		if p == "drop.secret" {
			t.Fatalf("read_file called for an ignored path: %s", p)
		}
		return fakeReply{Text: envelopeFor(p, "e1", "keep\n")}
	})
	c := fakeClient(f)

	rep, err := Fetch(context.Background(), c, FetchOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 2})
	if err != nil {
		t.Fatalf("Fetch errored: %v", err)
	}
	if slices.Contains(rep.Fetched, "drop.secret") {
		t.Errorf("Fetched = %v, drop.secret must be ignored", rep.Fetched)
	}
	if !slices.Contains(rep.Fetched, "keep.css") {
		t.Errorf("Fetched = %v, want keep.css present", rep.Fetched)
	}
	if got := f.CountTool("read_file"); got != 1 {
		t.Errorf("read_file called %d times, want 1 (only keep.css)", got)
	}

	bl, err := loadBaseline(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := bl.Verified["drop.secret"]; ok {
		t.Errorf("baseline holds an ignored path: %+v", bl.Verified)
	}
}

// TestFetchDoesNotPersistAPartialListing: an interrupted run must not
// silently discard a baseline it never re-verified (invariant 3). Two
// shapes: the listing itself never completes, and the listing completes but
// the caller's context is cancelled during the download loop. Either way, a
// pre-existing baseline.json must survive untouched.
func TestFetchDoesNotPersistAPartialListing(t *testing.T) {
	t.Run("cancelled before the listing", func(t *testing.T) {
		dir := t.TempDir()
		mkfile(t, dir, "a.css", "a\n")

		seed := Baseline{ProjectID: "proj-A", Verified: map[string]BaselineEntry{
			"keep.css": {Etag: "eKeep", Size: 4, SHA: "shaKeep"},
		}}
		if err := seed.save(dir); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(BaselinePath(dir))
		if err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			return fakeReply{Text: listingFor(fileEntry("a.css", "e1", 2))}
		})
		c := fakeClient(f)

		rep, err := Fetch(ctx, c, FetchOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 2})
		if err == nil {
			t.Fatal("Fetch succeeded against an already-cancelled context")
		}

		// The report must name only what the durable save actually recorded
		// (invariant 12) — here, nothing.
		if len(rep.Fetched) != 0 {
			t.Errorf("rep.Fetched = %v, want none: the baseline was never saved", rep.Fetched)
		}
		if rep.Bytes != 0 {
			t.Errorf("rep.Bytes = %d, want 0", rep.Bytes)
		}

		after, err := os.ReadFile(BaselinePath(dir))
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Errorf("baseline.json changed on an interrupted fetch:\nbefore: %s\nafter:  %s", before, after)
		}
	})

	t.Run("cancelled during the download loop", func(t *testing.T) {
		dir := t.TempDir()
		mkfile(t, dir, "a.css", "a\n")
		mkfile(t, dir, "b.css", "b\n")

		seed := Baseline{ProjectID: "proj-A", Verified: map[string]BaselineEntry{
			"keep.css": {Etag: "eKeep", Size: 4, SHA: "shaKeep"},
		}}
		if err := seed.save(dir); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(BaselinePath(dir))
		if err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithCancel(context.Background())

		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			if name == "list_files" {
				return fakeReply{Text: listingFor(
					fileEntry("a.css", "e1", 1), fileEntry("b.css", "e2", 1))}
			}
			// Cancel from inside the first read this test observes: the caller
			// (not dsx's own derived context) hangs up mid-transfer.
			cancel()
			p, _ := args["path"].(string)
			return fakeReply{Text: envelopeFor(p, "e1", "a")}
		})
		c := fakeClient(f)

		rep, err := Fetch(ctx, c, FetchOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 1})
		if err == nil {
			t.Fatal("Fetch succeeded despite the caller's context being cancelled mid-run")
		}

		if len(rep.Fetched) != 0 {
			t.Errorf("rep.Fetched = %v, want none: the baseline was never saved", rep.Fetched)
		}
		if rep.Bytes != 0 {
			t.Errorf("rep.Bytes = %d, want 0", rep.Bytes)
		}

		after, err := os.ReadFile(BaselinePath(dir))
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Errorf("baseline.json changed on an interrupted fetch:\nbefore: %s\nafter:  %s", before, after)
		}
	})

	// "cancelled after every download succeeded" reaches the parent.Err()
	// check by a route neither subtest above takes: both downloads complete
	// cleanly (nothing lands in errs), and the parent context is cancelled in
	// the gap between wg.Wait() returning and the wholesale save — exactly
	// the race invariant 3's check exists to catch. Hooked through Progress:
	// prog.step fires once per successful download, outside the lock and
	// before that goroutine returns, so cancelling on the last step lands the
	// cancellation before wg.Wait() unblocks without ever failing a read.
	t.Run("cancelled after every download succeeded", func(t *testing.T) {
		dir := t.TempDir()
		mkfile(t, dir, "a.css", "a\n")
		mkfile(t, dir, "b.css", "b\n")

		seed := Baseline{ProjectID: "proj-A", Verified: map[string]BaselineEntry{
			"keep.css": {Etag: "eKeep", Size: 4, SHA: "shaKeep"},
		}}
		if err := seed.save(dir); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(BaselinePath(dir))
		if err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithCancel(context.Background())

		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			if name == "list_files" {
				return fakeReply{Text: listingFor(
					fileEntry("a.css", "e1", 1), fileEntry("b.css", "e2", 1))}
			}
			p, _ := args["path"].(string)
			switch p {
			case "a.css":
				return fakeReply{Text: envelopeFor(p, "e1", "a")}
			case "b.css":
				return fakeReply{Text: envelopeFor(p, "e2", "b")}
			}
			return fakeReply{Text: "unexpected path " + p, IsError: true}
		})
		c := fakeClient(f)

		w := &cancelOnNthWrite{want: 2, cancel: cancel}

		rep, err := Fetch(ctx, c, FetchOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 2, Progress: w})
		if err == nil {
			t.Fatal("Fetch succeeded despite the caller's context being cancelled after every download completed")
		}
		if !strings.Contains(err.Error(), "fetch interrupted") {
			t.Errorf("err = %v, want it to mention \"fetch interrupted\"", err)
		}

		if len(rep.Fetched) != 0 {
			t.Errorf("rep.Fetched = %v, want none: the baseline was never saved despite both downloads succeeding", rep.Fetched)
		}
		if rep.Bytes != 0 {
			t.Errorf("rep.Bytes = %d, want 0", rep.Bytes)
		}

		after, err := os.ReadFile(BaselinePath(dir))
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Errorf("baseline.json changed on an interrupted fetch:\nbefore: %s\nafter:  %s", before, after)
		}
	})

	// "errored after one path already succeeded" is the shape the review
	// found reachable but untested: one download completes and is recorded
	// into the in-memory verified map before a sibling download hard-fails,
	// so `errs` is non-empty and Fetch returns before the wholesale save
	// ever runs. The report must not claim the succeeded path was recorded.
	t.Run("errored after one path already succeeded", func(t *testing.T) {
		dir := t.TempDir()
		mkfile(t, dir, "good.css", "good\n")
		mkfile(t, dir, "bad.css", "bad\n")

		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			if name == "list_files" {
				return fakeReply{Text: listingFor(
					fileEntry("good.css", "e1", 5), fileEntry("bad.css", "e2", 4))}
			}
			p, _ := args["path"].(string)
			switch p {
			case "good.css":
				return fakeReply{Text: envelopeFor(p, "e1", "good\n")}
			case "bad.css":
				return fakeReply{Text: "server exploded", IsError: true}
			}
			return fakeReply{Text: "unexpected path " + p, IsError: true}
		})
		c := fakeClient(f)

		rep, err := Fetch(context.Background(), c, FetchOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 1})
		if err == nil {
			t.Fatal("Fetch succeeded despite one path hard-erroring")
		}

		if len(rep.Fetched) != 0 {
			t.Errorf("rep.Fetched = %v, want none: good.css was verified in memory but never durably saved", rep.Fetched)
		}
		if rep.Bytes != 0 {
			t.Errorf("rep.Bytes = %d, want 0", rep.Bytes)
		}

		if _, err := os.Stat(BaselinePath(dir)); !os.IsNotExist(err) {
			t.Errorf("baseline.json was written despite the run erroring: stat err = %v", err)
		}
	})
}

// cancelOnNthWrite is a Progress io.Writer that calls cancel once its Write
// method has been invoked `want` times. progress.step calls Write exactly
// once per completed download, outside the download's own mutex and before
// that goroutine returns — so triggering cancel on the final expected write
// lands the cancellation before wg.Wait() can unblock, without ever failing
// a read.
type cancelOnNthWrite struct {
	n      atomic.Int32
	want   int32
	cancel func()
}

func (w *cancelOnNthWrite) Write(p []byte) (int, error) {
	if w.n.Add(1) == w.want {
		w.cancel()
	}
	return len(p), nil
}
