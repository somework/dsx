package files

import (
	"context"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/clitest"
	"github.com/somework/dsx/internal/dsxerr"
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
