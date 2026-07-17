package main

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// A non-positive concurrency must not be able to reach a semaphore.
//
// walkTree and runPull each build `make(chan struct{}, concurrency)`. At zero
// that channel is unbuffered, and walk's `sem <- struct{}{}` blocks forever:
// its only receiver is `<-sem`, downstream in the same goroutine. wg.Wait never
// returns, nothing errors, nothing prints -- the process just stops. Below zero
// make panics outright: "makechan: size out of range".
//
// The clamp used to live in the two CLI callers, so `dsx tree -j 0` was safe by
// convention: two call sites, two clamps, and nothing tying either to the
// semaphore it protects. cmdTree calls walkTree directly rather than through
// runPull, so a clamp moved to runPull alone would have deleted cmdTree's.
// These tests fix the property at the supplier instead of auditing it at each
// caller, which is why they call walkTree and runPull rather than the CLI.
//
// They are timeout-guarded because the failure mode is a hang, not a panic: an
// unguarded assertion here would wedge the whole suite instead of reporting.

// withTimeout runs fn and fails if it has not returned in time.
//
// fn returns its error rather than calling t, because on timeout the test
// completes while fn's goroutine is still live -- and a t.Errorf from a
// goroutine outliving its test panics the binary. The channel is buffered so a
// late send cannot block that goroutine forever on top of whatever wedged it.
func withTimeout(t *testing.T, d time.Duration, what string, fn func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()

	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(d):
		t.Fatalf("%s did not return within %s: deadlocked on an unbuffered semaphore", what, d)
	}
}

func TestWalkTreeClampsNonPositiveConcurrency(t *testing.T) {
	for _, jobs := range []int{0, -1, -8} {
		t.Run(fmt.Sprint(jobs), func(t *testing.T) {
			// The listing must depend on the path: a fake answering every walk
			// with the same directory entry describes an infinite tree, and
			// walkTree would recurse forever for reasons having nothing to do
			// with the semaphore under test.
			f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
				switch args["path"] {
				case nil: // the project root
					return fakeReply{Text: listingFor(
						fileEntry("a.md", "e1", 1),
						dirEntry("sub"),
					)}
				case "sub":
					return fakeReply{Text: listingFor(fileEntry("sub/b.md", "e2", 2))}
				}
				return fakeReply{Text: "[]"}
			})
			c := fakeClient(f)

			withTimeout(t, 5*time.Second, "walkTree", func() error {
				files, err := walkTree(context.Background(), c, "p1", jobs)
				if err != nil {
					return fmt.Errorf("walkTree(-j %d): %w", jobs, err)
				}
				if len(files) != 2 {
					return fmt.Errorf("walkTree(-j %d) enumerated %d files, want 2: %v", jobs, len(files), files)
				}
				return nil
			})
		})
	}
}

func TestRunPullClampsNonPositiveConcurrency(t *testing.T) {
	for _, jobs := range []int{0, -1} {
		t.Run(fmt.Sprint(jobs), func(t *testing.T) {
			f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
				switch name {
				case "list_files":
					return fakeReply{Text: listingFor(fileEntry("a.md", "e1", 5))}
				case "read_file":
					return fakeReply{Text: envelopeFor("a.md", "e1", "hello")}
				}
				return fakeReply{Text: "[]"}
			})
			dir := t.TempDir()

			withTimeout(t, 5*time.Second, "runPull", func() error {
				rep, err := runPull(context.Background(), fakeClient(f), pullOpts{
					projectID:   "p1",
					dir:         dir,
					concurrency: jobs,
				})
				if err != nil {
					return fmt.Errorf("runPull(-j %d): %w", jobs, err)
				}
				if len(rep.Fetched) != 1 {
					return fmt.Errorf("runPull(-j %d) fetched %d files, want 1", jobs, len(rep.Fetched))
				}
				return nil
			})
		})
	}
}
