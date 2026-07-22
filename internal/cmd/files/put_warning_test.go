package files

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/clitest"
	"github.com/somework/dsx/internal/syncer"
)

func putFake(t *testing.T) *clitest.Server {
	t.Helper()
	return newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: `{"etags":{"a.css":"e2"},"written":1}`}
	})
}

// TestPutLeavesTheLedgerBehind... pins the trap; nothing warned about it. put
// has no <dir>, so it cannot address a ledger — but it can say that the
// directory it was run from has one, and what that means.
func TestPutWarnsWhenTheWorkingDirectoryIsBoundToTheSameProject(t *testing.T) {
	dir := t.TempDir()
	clitest.SeedState(t, dir, syncer.State{ProjectID: "p1", Files: map[string]syncer.FileState{}})
	mkfile(t, dir, "a.css", "a{}")
	t.Chdir(dir)

	var warn bytes.Buffer
	putWarn = &warn
	t.Cleanup(func() { putWarn = nil })

	if _, err := captureStdout(t, func() error {
		return cmdPut(t.Context(), fakeClient(putFake(t)), []string{"p1", "a.css", "a.css"})
	}); err != nil {
		t.Fatal(err)
	}
	got := warn.String()
	if !strings.Contains(got, "dsx push") {
		t.Errorf("warning does not name the command that stays in step:\n%s", got)
	}
	if !strings.Contains(got, syncer.DirName) {
		t.Errorf("warning does not name the ledger's home:\n%s", got)
	}
}

// The warning follows the same ledger tree and cat resolve their project from.
// Left at the cwd it goes quiet exactly where the trap is easiest to fall into:
// deep in a synced tree, where `dsx files tree` answers from the binding above
// and put's silence reads as "no ledger is affected".
func TestPutWarnsFromAnyDepthInsideTheBoundTree(t *testing.T) {
	root := t.TempDir()
	clitest.SeedState(t, root, syncer.State{ProjectID: "p1", Files: map[string]syncer.FileState{}})
	deep := filepath.Join(root, "components", "buttons")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	mkfile(t, deep, "a.css", "a{}")
	t.Chdir(deep)

	var warn bytes.Buffer
	putWarn = &warn
	t.Cleanup(func() { putWarn = nil })

	if _, err := captureStdout(t, func() error {
		return cmdPut(t.Context(), fakeClient(putFake(t)), []string{"p1", "a.css", "a.css"})
	}); err != nil {
		t.Fatal(err)
	}
	got := warn.String()
	if !strings.Contains(got, "dsx push") {
		t.Errorf("put wrote to a bound tree from a subdirectory and said nothing:\n%s", got)
	}
	// The path has to be the tree it found, not "./": from here "./" names a
	// directory that holds no ledger at all, so the note would send the reader
	// looking in the wrong place.
	if !strings.Contains(got, root) {
		t.Errorf("warning does not name the tree it actually found (%s):\n%s", root, got)
	}
}

// A different project's ledger says nothing about this write.
func TestPutIsSilentWhenTheDirectoryIsBoundElsewhere(t *testing.T) {
	dir := t.TempDir()
	clitest.SeedState(t, dir, syncer.State{ProjectID: "other", Files: map[string]syncer.FileState{}})
	mkfile(t, dir, "a.css", "a{}")
	t.Chdir(dir)

	var warn bytes.Buffer
	putWarn = &warn
	t.Cleanup(func() { putWarn = nil })

	if _, err := captureStdout(t, func() error {
		return cmdPut(t.Context(), fakeClient(putFake(t)), []string{"p1", "a.css", "a.css"})
	}); err != nil {
		t.Fatal(err)
	}
	if got := warn.String(); got != "" {
		t.Errorf("warned about a ledger bound to another project:\n%s", got)
	}
}

func TestPutIsSilentOutsideASyncedDirectory(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "a.css", "a{}")
	t.Chdir(dir)

	var warn bytes.Buffer
	putWarn = &warn
	t.Cleanup(func() { putWarn = nil })

	if _, err := captureStdout(t, func() error {
		return cmdPut(t.Context(), fakeClient(putFake(t)), []string{"p1", "a.css", "a.css"})
	}); err != nil {
		t.Fatal(err)
	}
	if got := warn.String(); got != "" {
		t.Errorf("warned with no ledger present:\n%s", got)
	}
}

// The warning is advice, not a refusal: put still writes.
func TestPutStillWritesAfterWarning(t *testing.T) {
	dir := t.TempDir()
	clitest.SeedState(t, dir, syncer.State{ProjectID: "p1", Files: map[string]syncer.FileState{}})
	mkfile(t, dir, "a.css", "a{}")
	t.Chdir(dir)

	var warn bytes.Buffer
	putWarn = &warn
	t.Cleanup(func() { putWarn = nil })

	var called []string
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		called = append(called, name)
		return fakeReply{Text: `{"etags":{"a.css":"e2"},"written":1}`}
	})
	if _, err := captureStdout(t, func() error {
		return cmdPut(t.Context(), fakeClient(f), []string{"p1", "a.css", "a.css"})
	}); err != nil {
		t.Fatal(err)
	}
	if len(called) == 0 {
		t.Error("put refused instead of warning")
	}
}

// The warning does not touch the ledger: put has no <dir>, so the shell's
// directory is not known to be the sync root, and writing there on a guess
// would be inventing a binding.
func TestPutDoesNotWriteTheLedger(t *testing.T) {
	dir := t.TempDir()
	seeded := syncer.State{ProjectID: "p1", Files: map[string]syncer.FileState{}}
	clitest.SeedState(t, dir, seeded)
	mkfile(t, dir, "a.css", "a{}")
	t.Chdir(dir)

	var warn bytes.Buffer
	putWarn = &warn
	t.Cleanup(func() { putWarn = nil })

	if _, err := captureStdout(t, func() error {
		return cmdPut(t.Context(), fakeClient(putFake(t)), []string{"p1", "a.css", "a.css"})
	}); err != nil {
		t.Fatal(err)
	}
	st, err := syncer.LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Files) != 0 {
		t.Errorf("put wrote %v into the ledger of a directory it cannot know is the sync root",
			st.Files)
	}
}
