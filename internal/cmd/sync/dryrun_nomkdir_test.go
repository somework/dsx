package synccmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// -n is a dry run, and it created the directory it was pointed at, so a
// typo'd path silently became a real directory and the report described a
// sync into it.
//
// status was the other half of this pair and is gone from it, not by
// oversight: it takes no directory and makes no network call, so there is no
// path it could be pointed at to create. Left in, the case would have passed
// on the "takes no arguments" refusal instead of the one this test is about —
// green for a reason the name does not mention.
func TestADryRunCreatesNoDirectory(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode string
		args func(dir string) []string
	}{
		{"pull -n", "pull", func(d string) []string { return []string{d, "-n"} }},
		{"push -n", "push", func(d string) []string { return []string{d, "-n"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			missing := filepath.Join(parent, "typo")

			f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
				return fakeReply{Text: listingFor()}
			})
			_, err := captureStdout(t, func() error {
				return cmdSync(context.Background(), fakeClient(f), tc.mode, tc.args(missing))
			})
			if err == nil {
				t.Fatalf("%s accepted a directory that does not exist", tc.name)
			}
			if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
				t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
			}
			if _, statErr := os.Stat(missing); statErr == nil {
				t.Errorf("%s created %s — a command that transfers nothing must leave no trace",
					tc.name, missing)
			}
		})
	}
}

// A real pull still creates its target: that is the one path whose whole job is
// to put files there.
func TestARealPullStillCreatesItsDirectory(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "fresh")

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})
	if _, err := captureStdout(t, func() error {
		return cmdSync(context.Background(), fakeClient(f), "pull", syncIn(t, target, "proj-A"))
	}); err != nil {
		t.Fatalf("a real pull into a new directory failed: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("a real pull did not create its target: %v", err)
	}
}

// An existing directory is the normal case for both dry modes.
func TestADryRunOnAnExistingDirectoryStillWorks(t *testing.T) {
	dir := t.TempDir()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})
	if _, err := captureStdout(t, func() error {
		return cmdSync(context.Background(), fakeClient(f), "status", syncIn(t, dir, "proj-A"))
	}); err != nil {
		t.Fatalf("status on an existing directory failed: %v", err)
	}
}
