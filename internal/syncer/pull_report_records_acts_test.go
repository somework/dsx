package syncer

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

// trackedTree writes each file and returns a State recording it as ours and
// unmodified.
func trackedTree(t *testing.T, dir string, files map[string]string) State {
	t.Helper()
	st := State{ProjectID: "p1", Files: map[string]FileState{}}
	for path, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		st.Files[path] = FileState{
			Etag: "old",
			Size: int64(len(body)),
			SHA:  SHA256Hex([]byte(body)),
		}
	}
	if err := st.save(dir); err != nil {
		t.Fatal(err)
	}
	return st
}

func existsIn(t *testing.T, dir, path string) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(path)))
	return err == nil
}

// A fetch error returns before the prune loop ever runs, so nothing was deleted.
func TestPullDeletedIsEmptyWhenTheFetchFailedBeforeThePrune(t *testing.T) {
	dir := t.TempDir()
	trackedTree(t, dir, map[string]string{"a.css": "aaa", "b.css": "bbb"})

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("new.css", "e1", 1))}
		case "read_file":
			return fakeReply{Text: "boom", IsError: true}
		}
		return fakeReply{Text: "[]"}
	})

	rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 2, Prune: true,
	})
	if err == nil {
		t.Fatal("want the fetch error, got nil — the fixture stopped failing")
	}
	if len(rep.Deleted) != 0 {
		t.Errorf("Deleted = %v, want empty: pull returned before the prune loop", rep.Deleted)
	}
	for _, p := range []string{"a.css", "b.css"} {
		if !existsIn(t, dir, p) {
			t.Errorf("%s is gone from disk, but the run aborted before pruning", p)
		}
	}
}

// The prune loop breaks on the first os.Remove failure. Deleted must name the
// paths actually removed.
func TestPullDeletedNamesOnlyThePathsActuallyRemoved(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions do not block os.Remove")
	}
	dir := t.TempDir()
	trackedTree(t, dir, map[string]string{
		"a.css":     "aaa",
		"sub/b.css": "bbb",
		"z.css":     "zzz",
	})

	// Make sub/ unwritable so os.Remove(sub/b.css) fails mid-loop, leaving
	// sub/b.css and z.css on disk.
	sub := filepath.Join(dir, "sub")
	if err := os.Chmod(sub, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o700) })

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor()}
		}
		return fakeReply{Text: "[]"}
	})

	rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 2, Prune: true,
	})
	if err == nil {
		t.Fatal("want the os.Remove error, got nil — the fixture stopped failing")
	}
	if want := []string{"a.css"}; !slices.Equal(rep.Deleted, want) {
		t.Errorf("Deleted = %v, want %v", rep.Deleted, want)
	}
	for _, p := range []string{"sub/b.css", "z.css"} {
		if !existsIn(t, dir, p) {
			t.Errorf("%s is gone from disk but was reported as surviving", p)
		}
	}
	if existsIn(t, dir, "a.css") {
		t.Error("a.css is still on disk but was reported deleted")
	}
}

// Positive control: the whole plan really ran, so the whole plan is reported.
func TestPullDeletedIsReportedWhenThePruneSucceeded(t *testing.T) {
	dir := t.TempDir()
	trackedTree(t, dir, map[string]string{"a.css": "aaa", "b.css": "bbb", "z.css": "zzz"})

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor()}
		}
		return fakeReply{Text: "[]"}
	})

	rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 2, Prune: true,
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	want := []string{"a.css", "b.css", "z.css"}
	if !slices.Equal(rep.Deleted, want) {
		t.Fatalf("Deleted = %v, want %v", rep.Deleted, want)
	}
	for _, p := range want {
		if existsIn(t, dir, p) {
			t.Errorf("%s reported deleted but still on disk", p)
		}
	}
}

// Positive control: --dry-run reports the plan as the outcome (invariant 12).
func TestPullDryRunStillPreviewsTheDeletes(t *testing.T) {
	dir := t.TempDir()
	trackedTree(t, dir, map[string]string{"a.css": "aaa", "b.css": "bbb"})

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor()}
		}
		return fakeReply{Text: "[]"}
	})

	rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 2, Prune: true, DryRun: true,
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if want := []string{"a.css", "b.css"}; !slices.Equal(rep.Deleted, want) {
		t.Errorf("Deleted = %v, want the previewed plan %v", rep.Deleted, want)
	}
	for _, p := range []string{"a.css", "b.css"} {
		if !existsIn(t, dir, p) {
			t.Errorf("%s was deleted during a dry run", p)
		}
	}
}

// The error paths must sort too: --json is a machine surface there as well.
func TestPullFetchedIsSortedOnTheErrorPath(t *testing.T) {
	dir := t.TempDir()

	// Serve the readable files in strictly reverse-alphabetical order, then
	// fail, so an unsorted Fetched is deterministic rather than lucky.
	order := []string{"g.css", "f.css", "e.css", "d.css", "c.css", "b.css", "a.css"}
	var (
		mu   sync.Mutex
		cond = sync.NewCond(&mu)
		idx  int
	)

	entries := []RemoteEntry{fileEntry("zz-fail.css", "e0", 1)}
	for _, p := range order {
		entries = append(entries, fileEntry(p, "e1", 1))
	}

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(entries...)}
		case "read_file":
			p, _ := args["path"].(string)
			mu.Lock()
			defer mu.Unlock()
			if p == "zz-fail.css" {
				for idx < len(order) {
					cond.Wait()
				}
				return fakeReply{Text: "boom", IsError: true}
			}
			for idx >= len(order) || order[idx] != p {
				cond.Wait()
			}
			idx++
			cond.Broadcast()
			return fakeReply{Text: envelopeFor(p, "e-new", "x")}
		}
		return fakeReply{Text: "[]"}
	})

	rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 16,
	})
	if err == nil {
		t.Fatal("want the fetch error, got nil — the fixture stopped failing")
	}
	// Not an exact count: fail() cancels fetchCtx, so a peer whose response is
	// still in flight legitimately loses its body.
	if len(rep.Fetched) < 4 {
		t.Fatalf("fetched %d, want at least 4 — the fixture stopped exercising the sort: %v",
			len(rep.Fetched), rep.Fetched)
	}
	assertSorted(t, "Fetched", rep.Fetched)
	// Breadth only, and empty here -- see the note in report_order_test.go.
	assertSorted(t, "Binary", rep.Binary)
}
