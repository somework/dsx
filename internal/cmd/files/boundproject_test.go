package files

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/clitest"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/mcp"
	"github.com/somework/dsx/internal/syncer"
)

// bindCwd makes the test's working directory a synced directory bound to
// project.
func bindCwd(t *testing.T, project string) {
	t.Helper()
	dir := t.TempDir()
	clitest.SeedState(t, dir, syncer.State{ProjectID: project, Files: map[string]syncer.FileState{}})
	t.Chdir(dir)
}

// unboundCwd makes the test's working directory carry no ledger.
func unboundCwd(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
}

// bindAbove binds a tree to project and leaves the test standing two levels
// down inside it, with no ledger of its own anywhere below the root.
func bindAbove(t *testing.T, project string) {
	t.Helper()
	root := t.TempDir()
	clitest.SeedState(t, root, syncer.State{ProjectID: project, Files: map[string]syncer.FileState{}})
	deep := filepath.Join(root, "components", "buttons")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(deep)
}

// The sync verbs find their ledger by walking up, the way git finds .git.
// tree and cat read the same binding, so standing in a subdirectory must not
// change the answer — otherwise `cd components && dsx files tree` refuses in a
// tree where `dsx pull` works, and the difference is invisible from the prompt.
func TestTreeAndCatWalkUpToTheLedgerLikeTheSyncVerbs(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(ctx context.Context, c *mcp.Client, args []string) error
		argv []string
	}{
		{"tree", cmdTree, []string{"--json"}},
		{"cat", cmdCat, []string{"a.css"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bindAbove(t, "proj-A")

			var seen []map[string]any
			f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
				seen = append(seen, args)
				if name == "read_file" {
					return fakeReply{Text: clitest.EnvelopeFor("a.css", "e1", "body")}
				}
				return fakeReply{Text: listingFor(fileEntry("a.css", "e1", 4))}
			})

			if _, err := captureStdout(t, func() error {
				return tc.run(context.Background(), fakeClient(f), tc.argv)
			}); err != nil {
				t.Fatalf("%s failed two levels inside a synced tree: %v", tc.name, err)
			}
			if len(seen) == 0 {
				t.Fatal("no call was made")
			}
			if got := seen[0]["project_id"]; got != "proj-A" {
				t.Errorf("project_id=%v, want proj-A from the ledger above", got)
			}
		})
	}
}

// The walk-up is for reads only. A write resolving its project from a ledger
// the caller never named would let the working directory choose the target of
// a destructive act — one directory further from the caller's attention than
// the case invariant 14 already forbids.
func TestNoWriteWalksUpForItsProject(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(ctx context.Context, c *mcp.Client, args []string) error
		argv []string
	}{
		{"put", cmdPut, []string{"tokens.css"}},
		{"rm", cmdRm, []string{"tokens.css"}},
		{"cp", cmdCp, []string{"a.css", "b.css"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bindAbove(t, "proj-A")

			var seen []map[string]any
			f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
				seen = append(seen, args)
				return fakeReply{Text: "{}"}
			})

			_, err := captureStdout(t, func() error {
				return tc.run(context.Background(), fakeClient(f), tc.argv)
			})
			if err == nil {
				t.Fatalf("%s ran with no project named, inside a synced tree", tc.name)
			}
			if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
				t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
			}
			for _, a := range seen {
				if a["project_id"] == "proj-A" {
					t.Errorf("%s took its project from a ledger above the cwd; the working "+
						"directory must not choose the target of a destructive act", tc.name)
				}
			}
		})
	}
}

// tree and cat are read-only against the server and take no <dir>, so reading
// the directory's own binding adds no way to name a project — it stops hiding
// the one dsx already obeys for pull/push/status.
func TestTreeWithNoProjectUsesTheDirectoryBinding(t *testing.T) {
	bindCwd(t, "proj-A")

	var seen []map[string]any
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		seen = append(seen, args)
		return fakeReply{Text: listingFor(fileEntry("a.css", "e1", 3))}
	})

	if _, err := captureStdout(t, func() error {
		return cmdTree(context.Background(), fakeClient(f), []string{"--json"})
	}); err != nil {
		t.Fatalf("tree with no project failed inside a synced directory: %v", err)
	}
	if len(seen) == 0 {
		t.Fatal("no call was made")
	}
	if got := seen[0]["project_id"]; got != "proj-A" {
		t.Errorf("project_id=%v, want proj-A from the ledger", got)
	}
}

func TestTreeWithAProjectIgnoresTheDirectoryBinding(t *testing.T) {
	bindCwd(t, "proj-A")

	var seen []map[string]any
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		seen = append(seen, args)
		return fakeReply{Text: listingFor()}
	})

	if _, err := captureStdout(t, func() error {
		return cmdTree(context.Background(), fakeClient(f), []string{"proj-B", "--json"})
	}); err != nil {
		t.Fatal(err)
	}
	if got := seen[0]["project_id"]; got != "proj-B" {
		t.Errorf("project_id=%v, want the named proj-B", got)
	}
}

func TestTreeOutsideASyncedDirectoryStillNeedsAProject(t *testing.T) {
	unboundCwd(t)

	f := newFakeMCP(t, cmdsReplyJSON(listingFor()))
	_, err := captureStdout(t, func() error {
		return cmdTree(context.Background(), fakeClient(f), []string{"--json"})
	})
	if err == nil {
		t.Fatal("tree ran with no project outside a synced directory")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
	if !strings.Contains(err.Error(), "tree <project>") {
		t.Errorf("refusal does not show the form:\n%s", err)
	}
	// The walk-up made the bare form misleading: it says a project is required
	// without saying that the search for one covered every directory above too.
	// Left unsaid, `cd .. && dsx files tree` reads as worth trying.
	if !strings.Contains(err.Error(), "or above") {
		t.Errorf("refusal does not say the search reached past the cwd:\n%s", err)
	}
}

func TestCatWithOnePositionalTakesItAsThePath(t *testing.T) {
	bindCwd(t, "proj-A")

	var seen []map[string]any
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		seen = append(seen, args)
		return fakeReply{Text: clitest.EnvelopeFor("tokens.css", "e1", "a{}")}
	})

	if _, err := captureStdout(t, func() error {
		return cmdCat(context.Background(), fakeClient(f), []string{"tokens.css"})
	}); err != nil {
		t.Fatalf("cat <path> failed inside a synced directory: %v", err)
	}
	if got := seen[0]["project_id"]; got != "proj-A" {
		t.Errorf("project_id=%v, want proj-A from the ledger", got)
	}
	if got := seen[0]["path"]; got != "tokens.css" {
		t.Errorf("path=%v, want the sole positional read as the path", got)
	}
}

func TestCatWithTwoPositionalsIsUnchanged(t *testing.T) {
	bindCwd(t, "proj-A")

	var seen []map[string]any
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		seen = append(seen, args)
		return fakeReply{Text: clitest.EnvelopeFor("tokens.css", "e1", "a{}")}
	})

	if _, err := captureStdout(t, func() error {
		return cmdCat(context.Background(), fakeClient(f), []string{"proj-B", "tokens.css"})
	}); err != nil {
		t.Fatal(err)
	}
	if got := seen[0]["project_id"]; got != "proj-B" {
		t.Errorf("project_id=%v, want the named proj-B", got)
	}
}

func TestCatOutsideASyncedDirectoryStillNeedsAProject(t *testing.T) {
	unboundCwd(t)

	f := newFakeMCP(t, cmdsReplyJSON(""))
	_, err := captureStdout(t, func() error {
		return cmdCat(context.Background(), fakeClient(f), []string{"tokens.css"})
	})
	if err == nil {
		t.Fatal("cat ran with no project outside a synced directory")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
}

// ls is deliberately left alone: `ls <project> [path]` with one positional is
// already ambiguous, and resolving it from the ledger would turn `dsx ls foo`
// into either "list project foo" or "list path foo" depending on cwd — the
// same trap the sync refusal now warns about.
func TestLsStillRequiresAProjectInsideASyncedDirectory(t *testing.T) {
	bindCwd(t, "proj-A")

	var seen []map[string]any
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		seen = append(seen, args)
		return fakeReply{Text: "[]"}
	})

	if _, err := captureStdout(t, func() error {
		return cmdLs(context.Background(), fakeClient(f), []string{"type"})
	}); err != nil {
		t.Fatalf("ls changed its refusal behaviour: %v", err)
	}
	if got := seen[0]["project_id"]; got != "type" {
		t.Errorf("project_id=%v — ls resolved a positional from the ledger, "+
			"making `dsx ls X` mean different things depending on cwd", got)
	}
}

// The one-positional arm above is only half of ls's refusal. `dsx ls` with none
// is the other half, and it is the arm a ledger fallback would be added to:
// tree and cat both take theirs there. Left unheld, a fallback here goes in
// silently — the whole suite stays green.
// TestTreeWithNoProjectUsesTheDirectoryBinding is the positive control: it
// proves bindCwd makes that fallback reachable at all.
func TestLsWithNoProjectRefusesInsideASyncedDirectory(t *testing.T) {
	bindCwd(t, "proj-A")

	var seen []map[string]any
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		seen = append(seen, args)
		return fakeReply{Text: "[]"}
	})

	_, err := captureStdout(t, func() error {
		return cmdLs(context.Background(), fakeClient(f), nil)
	})
	if err == nil {
		t.Fatal("bare `dsx ls` ran inside a synced directory")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
	if len(seen) > 0 {
		t.Errorf("ls called the server with %v; it took its project from the ledger", seen[0])
	}
}

// The mutating commands keep naming their project: a ledger read on put/rm/cp
// would let cwd decide the target of a destructive act.
func TestMutatingCommandsDoNotReadTheLedger(t *testing.T) {
	bindCwd(t, "proj-A")

	var seen []map[string]any
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		seen = append(seen, args)
		return fakeReply{Text: "{}"}
	})

	_, err := captureStdout(t, func() error {
		return cmdRm(context.Background(), fakeClient(f), []string{"tokens.css"})
	})
	if err == nil && len(seen) > 0 && seen[0]["project_id"] == "proj-A" {
		t.Error("rm took its project from the ledger; cwd must not choose what gets deleted")
	}
}

// cat --out writes a path the user named, and it is the command conflictHint
// prescribes as the way out of a conflict. Leaving it destructive would
// half-fix the asymmetry and leave the documented cure able to destroy.
func TestCatOutRefusesAReadOnlyDestination(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "locked.css")
	if err := os.WriteFile(out, []byte("LOCKED"), 0o444); err != nil {
		t.Fatal(err)
	}

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: clitest.EnvelopeFor("a.css", "e1", "server")}
	})
	_, err := captureStdout(t, func() error {
		return cmdCat(context.Background(), fakeClient(f), []string{"proj-A", "a.css", "--out", out})
	})
	if err == nil {
		t.Fatal("cat --out replaced a read-only file")
	}
	if !strings.Contains(err.Error(), "chmod +w") {
		t.Errorf("refusal does not name the fix, so cat --out is not going through WriteAtomic:\n%s", err)
	}
	b, readErr := os.ReadFile(out)
	if readErr != nil || string(b) != "LOCKED" {
		t.Errorf("destination = %q, %v — want it untouched", b, readErr)
	}
}
