package syncer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// symlinkedDsxDir builds a target directory whose .dsx is a symlink to a
// separate, empty directory: the hazard checkLedgerHome refuses. target is
// returned so a case can assert nothing was ever written through the link.
func symlinkedDsxDir(t *testing.T) (dir, target string) {
	t.Helper()
	dir = t.TempDir()
	target = t.TempDir()
	if err := os.Symlink(target, StateDir(dir)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	return dir, target
}

// symlinkedDsxDirWithLedger is symlinkedDsxDir plus a real ledger written
// directly into the symlink's target, so LoadState reads a legitimate,
// populated State through the link before checkLedgerHome ever inspects the
// link itself.
func symlinkedDsxDirWithLedger(t *testing.T, st State) (dir, target string) {
	t.Helper()
	dir, target = symlinkedDsxDir(t)
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, stateBaseName), append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, target
}

// cancelOnWrite is an io.Writer standing in for Progress; Push only writes
// progress after a batch's writeBatch call has already returned, so firing
// cancel here lands strictly between "the write landed" and the ctx.Err()
// check that follows it — never racing the HTTP round trip itself.
type cancelOnWrite struct{ cancel context.CancelFunc }

func (c cancelOnWrite) Write(p []byte) (int, error) {
	c.cancel()
	return len(p), nil
}

func assertSymlinkUntouched(t *testing.T, dir, target string) {
	t.Helper()
	fi, err := os.Lstat(StateDir(dir))
	if err != nil {
		t.Fatalf(".dsx vanished: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error(".dsx is no longer a symlink — something replaced it")
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		// Exactly the seeded ledger, if any — never a second file appearing.
		t.Errorf("the symlink's target holds %d entries, want at most the seeded ledger: %v", len(entries), entries)
	}
}

// TestPushRefusalCreatesNoDsxDirectory enumerates the shapes a trace agent
// found: two refusals that precede checkLedgerHome entirely (project and
// endpoint mismatch), a symlinked .dsx alone, the same symlink layered under
// plans of increasing shape (all-conflicts, binary-conflicts), under a
// pre-cancelled context, and under DryRun. The final case is the contrast
// that makes the rest meaningful: a successful write later interrupted DOES
// create .dsx, because invariant 5 requires save() to run on every error
// path — without this case the rest would pass identically with the whole
// feature deleted.
func TestPushRefusalCreatesNoDsxDirectory(t *testing.T) {
	noopFake := func(t *testing.T, remote ...RemoteEntry) (*fakeMCP, func() int) {
		t.Helper()
		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			if name == "list_files" {
				return fakeReply{Text: listingFor(remote...)}
			}
			return fakeReply{Text: "[]"}
		})
		return f, func() int { return len(f.Recorded()) }
	}

	t.Run("project mismatch", func(t *testing.T) {
		dir := t.TempDir()
		syncSeedState(t, dir, State{ProjectID: "proj-A"})
		before := readBack(t, StatePath(dir))

		f, calls := noopFake(t)
		_, err := Push(context.Background(), fakeClient(f), PushOpts{
			ProjectID: "proj-B", Dir: dir, Concurrency: 1,
		})
		if err == nil {
			t.Fatal("want a refusal")
		}
		if calls() != 0 {
			t.Errorf("tools called=%d, want 0", calls())
		}
		if got := readBack(t, StatePath(dir)); got != before {
			t.Error("the ledger was rewritten by a refused push")
		}
	})

	t.Run("endpoint mismatch", func(t *testing.T) {
		dir := t.TempDir()
		syncSeedState(t, dir, State{ProjectID: "proj-A", Endpoint: "https://foreign.example/mcp"})
		before := readBack(t, StatePath(dir))

		f, calls := noopFake(t)
		_, err := Push(context.Background(), fakeClient(f), PushOpts{
			ProjectID: "proj-A", Dir: dir, Concurrency: 1,
		})
		if err == nil {
			t.Fatal("want a refusal")
		}
		if calls() != 0 {
			t.Errorf("tools called=%d, want 0", calls())
		}
		if got := readBack(t, StatePath(dir)); got != before {
			t.Error("the ledger was rewritten by a refused push")
		}
	})

	t.Run("symlinked .dsx alone", func(t *testing.T) {
		dir, target := symlinkedDsxDir(t)
		f, calls := noopFake(t)
		_, err := Push(context.Background(), fakeClient(f), PushOpts{
			ProjectID: "proj-A", Dir: dir, Concurrency: 1,
		})
		if err == nil {
			t.Fatal("want a refusal")
		}
		if got := dsxerr.Classify(err).Kind; got != dsxerr.KindLocal {
			t.Errorf("kind=%v, want %v", got, dsxerr.KindLocal)
		}
		if calls() != 0 {
			t.Errorf("tools called=%d, want 0", calls())
		}
		entries, rdErr := os.ReadDir(target)
		if rdErr != nil {
			t.Fatal(rdErr)
		}
		if len(entries) != 0 {
			t.Errorf("something was written through the symlink: %v", entries)
		}
	})

	t.Run("symlinked .dsx with an all-conflicts plan", func(t *testing.T) {
		dir, target := symlinkedDsxDirWithLedger(t, State{ProjectID: "proj-A", Files: map[string]FileState{}})
		mkfile(t, dir, "a.css", "mine{}")
		f, calls := noopFake(t, fileEntry("a.css", "e1", 6))
		_, err := Push(context.Background(), fakeClient(f), PushOpts{
			ProjectID: "proj-A", Dir: dir, Concurrency: 1,
		})
		if err == nil {
			t.Fatal("want a refusal")
		}
		if calls() != 0 {
			t.Errorf("tools called=%d, want 0 — a plan this rich must never be reached", calls())
		}
		assertSymlinkUntouched(t, dir, target)
	})

	t.Run("symlinked .dsx with a binary-conflicts plan", func(t *testing.T) {
		dir, target := symlinkedDsxDirWithLedger(t, State{ProjectID: "proj-A", Files: map[string]FileState{
			"img.png": {Etag: "e1", Binary: true},
		}})
		mkfile(t, dir, "img.png", "not really png bytes")
		f, calls := noopFake(t, fileEntry("img.png", "e1", 20))
		_, err := Push(context.Background(), fakeClient(f), PushOpts{
			ProjectID: "proj-A", Dir: dir, Concurrency: 1,
		})
		if err == nil {
			t.Fatal("want a refusal")
		}
		if calls() != 0 {
			t.Errorf("tools called=%d, want 0", calls())
		}
		assertSymlinkUntouched(t, dir, target)
	})

	t.Run("symlinked .dsx with a pre-cancelled context", func(t *testing.T) {
		dir, target := symlinkedDsxDir(t)
		f, calls := noopFake(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := Push(ctx, fakeClient(f), PushOpts{
			ProjectID: "proj-A", Dir: dir, Concurrency: 1,
		})
		if err == nil {
			t.Fatal("want a refusal")
		}
		// The symlink refusal, not a generic "interrupted" error: checkLedgerHome
		// takes no context and must fire regardless of its state.
		if got := dsxerr.Classify(err).Kind; got != dsxerr.KindLocal {
			t.Errorf("kind=%v, want %v (a pre-cancelled context must not mask the .dsx refusal)", got, dsxerr.KindLocal)
		}
		if calls() != 0 {
			t.Errorf("tools called=%d, want 0", calls())
		}
		entries, rdErr := os.ReadDir(target)
		if rdErr != nil {
			t.Fatal(rdErr)
		}
		if len(entries) != 0 {
			t.Errorf("something was written through the symlink: %v", entries)
		}
	})

	t.Run("symlinked .dsx with DryRun", func(t *testing.T) {
		dir, target := symlinkedDsxDir(t)
		f, calls := noopFake(t)
		_, err := Push(context.Background(), fakeClient(f), PushOpts{
			ProjectID: "proj-A", Dir: dir, Concurrency: 1, DryRun: true,
		})
		if err == nil {
			t.Fatal("want a refusal — checkLedgerHome runs unconditionally, dry run included")
		}
		if calls() != 0 {
			t.Errorf("tools called=%d, want 0", calls())
		}
		entries, rdErr := os.ReadDir(target)
		if rdErr != nil {
			t.Fatal(rdErr)
		}
		if len(entries) != 0 {
			t.Errorf("something was written through the symlink: %v", entries)
		}
	})

	// The contrast: an ordinary directory, never migrated, whose write
	// succeeds and is only then interrupted. save() runs on every error path
	// (invariant 5), and that is the one legitimate moment .dsx is born.
	t.Run("contrast: a successful write later interrupted creates .dsx", func(t *testing.T) {
		dir := t.TempDir()
		mkfile(t, dir, "a.css", "a{}")

		if _, err := os.Stat(StateDir(dir)); err == nil {
			t.Fatal(".dsx exists before the push even ran")
		}

		ctx, cancel := context.WithCancel(context.Background())
		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			switch name {
			case "list_files":
				return fakeReply{Text: listingFor()}
			case "write_files":
				return fakeReply{Text: `{"etags":{"a.css":"e1"},"written":1}`}
			}
			return fakeReply{Text: "unexpected " + name, IsError: true}
		})

		// cancelOnWrite fires from the progress callback, which push.go only
		// reaches after writeBatch has already returned success — the ctx.Err()
		// check that follows is what turns this into "interrupted after the
		// write landed" rather than racing the HTTP round trip itself.
		_, err := Push(ctx, fakeClient(f), PushOpts{
			ProjectID: "proj-A", Dir: dir, Concurrency: 1,
			Progress: cancelOnWrite{cancel},
		})
		if err == nil {
			t.Fatal("want the interrupted error")
		}

		if _, statErr := os.Stat(StateDir(dir)); statErr != nil {
			t.Errorf(".dsx was not created despite a save on the interrupted path: %v", statErr)
		}
		st, loadErr := LoadState(dir)
		if loadErr != nil {
			t.Fatalf("LoadState: %v", loadErr)
		}
		if _, ok := st.Files["a.css"]; !ok {
			t.Error("the write that landed before the interrupt is missing from the saved ledger")
		}
	})
}

// TestPullRefusesASymlinkedDsxBeforeFetchingAnything mirrors the push guard
// for the pull half: checkLedgerHome is wired into both, and only a test
// against each proves it — a shared helper proves nothing about wiring.
func TestPullRefusesASymlinkedDsxBeforeFetchingAnything(t *testing.T) {
	dir, target := symlinkedDsxDir(t)
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor(fileEntry("a.css", "e1", 3))}
	})

	_, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 1,
	})
	if err == nil {
		t.Fatal("want a refusal")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindLocal {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindLocal)
	}
	if got := len(f.Recorded()); got != 0 {
		t.Errorf("tools called=%d, want 0", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.css")); err == nil {
		t.Error("a.css was fetched despite the .dsx refusal")
	}
	entries, rdErr := os.ReadDir(target)
	if rdErr != nil {
		t.Fatal(rdErr)
	}
	if len(entries) != 0 {
		t.Errorf("something was written through the symlink: %v", entries)
	}
}
