package syncer

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func bindAt(t *testing.T, dir, project string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (State{ProjectID: project, Files: map[string]FileState{}}).save(dir); err != nil {
		t.Fatal(err)
	}
}

func TestFindRootReturnsTheDirectoryItself(t *testing.T) {
	dir := t.TempDir()
	bindAt(t, dir, "proj-A")

	got, err := FindRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Errorf("FindRoot(%q) = %q, want the directory itself", dir, got)
	}
}

// TestFindRootWalksUpFromASubdirectory is the whole point: `cd design/components
// && dsx pull` has to work, the way `git status` works from anywhere inside a
// repository.
func TestFindRootWalksUpFromASubdirectory(t *testing.T) {
	root := t.TempDir()
	bindAt(t, root, "proj-A")
	deep := filepath.Join(root, "components", "buttons", "css")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := FindRoot(deep)
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Errorf("FindRoot(%q) = %q, want %q", deep, got, root)
	}
}

// TestFindRootStopsAtTheNearestLedger: a nested binding is the one dsx must
// never silently reach past. Whoever bound the inner directory chose it, and
// answering with the outer one would sync a tree they did not name.
func TestFindRootStopsAtTheNearestLedger(t *testing.T) {
	outer := t.TempDir()
	bindAt(t, outer, "proj-OUTER")
	inner := filepath.Join(outer, "vendor", "kit")
	bindAt(t, inner, "proj-INNER")

	got, err := FindRoot(filepath.Join(inner, "sub"))
	if err == nil && got != inner {
		t.Errorf("FindRoot reached past the nearest ledger: got %q, want %q", got, inner)
	}
	if err != nil {
		// A missing leaf directory is not an error for discovery; walking up
		// from a path that does not exist yet is exactly what a caller in a
		// fresh subdirectory does.
		t.Fatalf("FindRoot on a non-existent leaf errored: %v", err)
	}
}

func TestFindRootReportsNothingWhenNoLedgerIsAnywhereAbove(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := FindRoot(deep)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("FindRoot = %q, want \"\" — nothing above it holds a ledger", got)
	}
}

// TestFindRootIgnoresADsxDirectoryWithNoLedger: `.dsx` alone is not a binding.
// fetch creates the directory to hold baseline.json, so a directory that has
// only ever been fetched into must keep walking rather than answer as a root
// whose LoadState would come back unbound.
func TestFindRootIgnoresADsxDirectoryWithNoLedger(t *testing.T) {
	root := t.TempDir()
	bindAt(t, root, "proj-A")

	half := filepath.Join(root, "half")
	if err := os.MkdirAll(StateDir(half), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := FindRoot(half)
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Errorf("FindRoot = %q, want %q — a .dsx holding no state.json is not a binding", got, root)
	}
}

// TestFindRootIgnoresADsxThatIsNotADirectory mirrors LoadState's own ENOTDIR
// branch: a regular file named .dsx makes state.json unreachable the same way
// a missing one does, and discovery must read that as "not a root" rather than
// fail the whole command.
func TestFindRootIgnoresADsxThatIsNotADirectory(t *testing.T) {
	root := t.TempDir()
	bindAt(t, root, "proj-A")

	odd := filepath.Join(root, "odd")
	if err := os.MkdirAll(odd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(odd, DirName), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := FindRoot(odd)
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Errorf("FindRoot = %q, want %q", got, root)
	}
}

// TestFindRootTerminatesAtTheFilesystemRoot: filepath.Dir("/") is "/", so a
// loop that does not compare against its own parent never ends.
func TestFindRootTerminatesAtTheFilesystemRoot(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = FindRoot(string(filepath.Separator))
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("FindRoot did not terminate at the filesystem root")
	}
}
