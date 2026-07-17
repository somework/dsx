package syncer

import (
	"context"
	"fmt"
	"testing"
	"time"
)

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
			f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
				switch args["path"] {
				case nil:
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

			withTimeout(t, 5*time.Second, "WalkTree", func() error {
				files, err := WalkTree(context.Background(), c, "p1", jobs)
				if err != nil {
					return fmt.Errorf("WalkTree(-j %d): %w", jobs, err)
				}
				if len(files) != 2 {
					return fmt.Errorf("WalkTree(-j %d) enumerated %d files, want 2: %v", jobs, len(files), files)
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

			withTimeout(t, 5*time.Second, "Pull", func() error {
				rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
					ProjectID:   "p1",
					Dir:         dir,
					Concurrency: jobs,
				})
				if err != nil {
					return fmt.Errorf("Pull(-j %d): %w", jobs, err)
				}
				if len(rep.Fetched) != 1 {
					return fmt.Errorf("Pull(-j %d) fetched %d files, want 1", jobs, len(rep.Fetched))
				}
				return nil
			})
		})
	}
}
