package synccmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/clitest"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/syncer"
)

func cloneFake(t *testing.T) *fakeMCP {
	t.Helper()
	return newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(fileEntry("a.css", "e1", 3))}
		}
		return fakeReply{Text: envelopeFor("a.css", "e1", "a{}")}
	})
}

func runClone(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return captureStdout(t, func() error {
		return cmdClone(context.Background(), fakeClient(cloneFake(t)), args)
	})
}

func TestCloneFetchesIntoANewDirectory(t *testing.T) {
	target := filepath.Join(t.TempDir(), "fresh")

	if _, err := runClone(t, "proj-A", target); err != nil {
		t.Fatalf("clone into a new directory failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "a.css")); err != nil {
		t.Errorf("a.css was not written: %v", err)
	}
	st, err := syncer.LoadState(target)
	if err != nil {
		t.Fatal(err)
	}
	if st.ProjectID != "proj-A" {
		t.Errorf("project_id=%q, want the cloned project — clone must pin", st.ProjectID)
	}
}

// Both positionals are required. A lone <project> would have to guess a
// directory, and every guess is one dsx would rather the user made.
func TestCloneRequiresBothPositionals(t *testing.T) {
	for _, args := range [][]string{{}, {"proj-A"}} {
		_, err := runClone(t, args...)
		if err == nil {
			t.Fatalf("clone(%v) was accepted", args)
		}
		if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
			t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
		}
	}
}

// The refusals must all precede the network: a clone that has already asked
// the server has already spent the round trip it was refusing to make.
func TestEveryCloneRefusalPrecedesTheNetwork(t *testing.T) {
	parent := t.TempDir()

	populated := filepath.Join(parent, "populated")
	if err := os.MkdirAll(populated, 0o755); err != nil {
		t.Fatal(err)
	}
	clitest.Mkfile(t, populated, "mine.txt", "my own work")

	synced := filepath.Join(parent, "synced")
	if err := os.MkdirAll(synced, 0o755); err != nil {
		t.Fatal(err)
	}
	syncSeedState(t, synced, syncer.State{ProjectID: "proj-A", Files: map[string]syncer.FileState{}})

	notADir := filepath.Join(parent, "file")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, dir, needle string }{
		{"populated", populated, "not empty"},
		{"already a working copy", synced, "ledger"},
		{"not a directory", notADir, "not a directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var called []string
			f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
				called = append(called, name)
				return fakeReply{Text: listingFor()}
			})
			_, err := captureStdout(t, func() error {
				return cmdClone(context.Background(), fakeClient(f), []string{"proj-A", tc.dir})
			})
			if err == nil {
				t.Fatalf("clone into %s was accepted", tc.name)
			}
			if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
				t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
			}
			if !strings.Contains(err.Error(), tc.needle) {
				t.Errorf("refusal does not say %q:\n%s", tc.needle, err)
			}
			if len(called) != 0 {
				t.Errorf("tools called=%v, want none", called)
			}
		})
	}
}

// dsx's own leavings and the caller's configuration are not content: a clone
// refused because of a .DS_Store or a .dsxignore the user wrote first would be
// refusing over nothing. .dsxignore especially — it shapes the very first pull,
// so demanding it be written afterwards inverts its purpose.
func TestCloneIgnoresWhatSyncIgnores(t *testing.T) {
	for _, name := range []string{
		".DS_Store",
		".dsxignore",
		".dsx-state.json.8412",
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "target")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			clitest.Mkfile(t, dir, name, "x")

			if _, err := runClone(t, "proj-A", dir); err != nil {
				t.Errorf("clone refused over %s, which sync does not see: %v", name, err)
			}
		})
	}
}

// .dsx is a builtin ignore (C1), so LocalIsEmpty asks through survey and
// cannot see a foreign .dsx directory — a bare Lstat is the only thing left
// standing between clone and overwriting someone else's .dsx/ layout.
func TestCloneRefusesADirectoryHoldingADsxDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "target")
	if err := os.MkdirAll(filepath.Join(dir, ".dsx"), 0o755); err != nil {
		t.Fatal(err)
	}
	clitest.Mkfile(t, filepath.Join(dir, ".dsx"), "marker.txt", "not ours")

	err := checkCloneTarget(dir)
	if err == nil {
		t.Fatal("clone into a directory holding a foreign .dsx was accepted")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
	if !strings.Contains(err.Error(), ".dsx") {
		t.Errorf("refusal does not name .dsx:\n%s", err)
	}
}

// Mutation testing found that narrowing the .dsx probe to "err == nil &&
// fi.IsDir()" makes no test fail — nothing pinned the regular-file or
// symlink shape. IsDir() on a bare Lstat result is false for a symlink
// regardless of what it resolves to, so a symlinked .dsx would slip the
// narrowed guard exactly like a plain file would.
func TestCloneRefusesADsxRegularFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "target")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".dsx"), []byte("not ours"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := checkCloneTarget(dir)
	if err == nil {
		t.Fatal("clone into a directory holding a .dsx regular file was accepted")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
	if !strings.Contains(err.Error(), ".dsx") {
		t.Errorf("refusal does not name .dsx:\n%s", err)
	}
}

func TestCloneRefusesADsxSymlinkToADirectoryInsideTheTree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "target")
	real := filepath.Join(dir, "real-dsx")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".dsx")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	err := checkCloneTarget(dir)
	if err == nil {
		t.Fatal("clone into a directory holding a .dsx symlink was accepted")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
	if !strings.Contains(err.Error(), ".dsx") {
		t.Errorf("refusal does not name .dsx:\n%s", err)
	}
}

func TestCloneRefusesADsxSymlinkPointingOutsideTheTree(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "target")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "elsewhere")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".dsx")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	err := checkCloneTarget(dir)
	if err == nil {
		t.Fatal("clone into a directory holding a .dsx symlink pointing outside the tree was accepted")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
	if !strings.Contains(err.Error(), ".dsx") {
		t.Errorf("refusal does not name .dsx:\n%s", err)
	}
}

// A .git directory is skipped whole, so cloning beside a fresh repo works.
func TestCloneIgnoresAGitDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "target")
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	clitest.Mkfile(t, filepath.Join(dir, ".git"), "config", "[core]")

	if _, err := runClone(t, "proj-A", dir); err != nil {
		t.Errorf("clone refused over a .git directory: %v", err)
	}
}

// A symlink resolves elsewhere and scanLocal walks the real tree, so a link to
// a fresh git checkout reads as empty and the whole project lands somewhere the
// caller never named.
func TestCloneRefusesASymlinkTarget(t *testing.T) {
	parent := t.TempDir()
	real := filepath.Join(parent, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := runClone(t, "proj-A", link)
	if err == nil {
		t.Fatal("clone into a symlink was accepted")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("refusal does not name the symlink:\n%s", err)
	}
}

// A refusal leaves nothing behind — checked before the directory is made, or
// clone would be testing a directory it had just created itself (invariant 16).
//
// The fixture is a target under a path component that is a regular file, so
// MkdirAll cannot make it. It used to pass a deep path as the PROJECT and
// "proj-A" as the directory, expecting the reversed-argument guard to fire —
// but that guard is on the directory slot and only for a UUID-shaped token
// (TestCloneCatchesReversedArguments covers it properly), so nothing refused,
// clone ran for real into a relative ./proj-A inside the package directory,
// and the test t.Skip'd having asserted nothing. That directory was committed
// once already. Hence t.Chdir: a relative path this test creates by mistake
// lands in a temp directory and not in the repository.
func TestARefusedCloneCreatesNothing(t *testing.T) {
	parent := t.TempDir()
	t.Chdir(parent)

	// The reversed pair: the project id is in the DIRECTORY slot, which is the
	// one refusal whose target clone would otherwise have created — and would
	// have created under a project id's name, the defect uuidasdir_test.go has
	// already caught twice in advice text. checkCloneTarget runs above
	// MkdirAll; swap those two lines in clone.go and this goes red.
	if _, err := runClone(t, filepath.Join(parent, "fresh"), sampleProjectID); err == nil {
		t.Fatal("clone accepted the reversed pair; the rest of this test " +
			"would then be asserting about a refusal that never happened")
	}
	if _, err := os.Stat(sampleProjectID); err == nil {
		t.Errorf("a refused clone created a directory named after the project id (%s)", sampleProjectID)
	}
	if _, err := os.Stat(filepath.Join(parent, "fresh")); err == nil {
		t.Errorf("a refused clone created the other slot's path too")
	}
}

// Arguments the other way round: the second slot is a directory, so a project
// id there means the caller swapped them. The guard is on <dir> and never on
// <project> — a hand-written shape check must not decide what dsx accepts as a
// project when pull accepts anything.
func TestCloneCatchesReversedArguments(t *testing.T) {
	dir := t.TempDir()
	_, err := runClone(t, dir, sampleProjectID)
	if err == nil {
		t.Fatal("reversed arguments were accepted")
	}
	if !strings.Contains(err.Error(), "looks like a project id") {
		t.Errorf("refusal does not name the swap:\n%s", err)
	}
}

// A project id that is not UUID-shaped must still clone: the shape check gates
// the directory slot, and PROTOCOL.md documents the id's shape as measured,
// not promised.
func TestCloneAcceptsAnyProjectShape(t *testing.T) {
	target := filepath.Join(t.TempDir(), "fresh")
	if _, err := runClone(t, "not-a-uuid-at-all", target); err != nil {
		t.Errorf("clone refused a project id it cannot vouch for: %v", err)
	}
}

// clone must not put --force in the vocabulary of a first run, and --prune has
// nothing to prune in an empty directory.
func TestCloneDeclaresNoForceOrPrune(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh")
	for _, flag := range []string{"--force", "--prune", "-n"} {
		if _, err := runClone(t, "proj-A", dir, flag); err == nil {
			t.Errorf("clone accepted %s", flag)
		}
	}
}

// clone fetches binaries and pull does not, and the asymmetry is deliberate:
// a clone that omits files is not a clone, and an empty directory has neither
// an established tree to surprise nor a ledger to rewrite.
func TestCloneFetchesBinariesWithoutBeingAsked(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0xff, 0xfe}
	target := filepath.Join(t.TempDir(), "fresh")

	var f *fakeMCP
	f = newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(
				fileEntry("a.css", "e1", 3),
				fileEntry("og.png", "e2", int64(len(png))),
			)}
		case "read_file":
			if args["path"] == "og.png" {
				return fakeReply{Text: `read_file: "og.png" is a binary file (stored base64)`, IsError: true}
			}
			return fakeReply{Text: envelopeFor("a.css", "e1", "a{}")}
		case "render_preview":
			return fakeReply{Text: f.PreviewReply(args["path"].(string))}
		}
		return fakeReply{Text: "[]"}
	})
	f.PutServe("og.png", png)

	if _, err := captureStdout(t, func() error {
		return cmdClone(context.Background(), fakeClient(f), []string{"proj-A", target})
	}); err != nil {
		t.Fatalf("clone: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(target, "og.png"))
	if err != nil {
		t.Fatalf("clone left the binary behind: %v", err)
	}
	if string(got) != string(png) {
		t.Fatalf("wrote % x, want % x", got, png)
	}
	// dsx wrote these bytes, so the marker goes — unlike an adopted path,
	// which keeps it (invariant 23).
	st, err := syncer.LoadState(target)
	if err != nil {
		t.Fatal(err)
	}
	if e := st.Files["og.png"]; e.SHA == "" || e.Binary {
		t.Fatalf("a cloned binary is not an ordinary ledger entry: %+v", e)
	}
}

// The escape hatch clone deliberately has no flag for: pin the directory, then
// a plain pull, which leaves binaries alone. If this stops working, clone's
// unconditional fetch has no way out and needs one.
func TestPinThenPlainPullIsTheTextOnlyPath(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0xff, 0xfe}
	dir := t.TempDir()

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(
				fileEntry("a.css", "e1", 3),
				fileEntry("og.png", "e2", int64(len(png))),
			)}
		case "read_file":
			if args["path"] == "og.png" {
				return fakeReply{Text: `read_file: "og.png" is a binary file (stored base64)`, IsError: true}
			}
			return fakeReply{Text: envelopeFor("a.css", "e1", "a{}")}
		}
		return fakeReply{Text: "[]"}
	})

	if _, err := captureStdout(t, func() error {
		return cmdPin(context.Background(), fakeClient(f), []string{"proj-A", dir})
	}); err != nil {
		t.Fatalf("pin: %v", err)
	}
	t.Chdir(dir)
	if _, err := captureStdout(t, func() error {
		return cmdPull(context.Background(), fakeClient(f), nil)
	}); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.css")); err != nil {
		t.Fatalf("the text file was not pulled: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "og.png")); err == nil {
		t.Fatal("a plain pull after pin fetched the binary; the text-only path is gone")
	}
}
