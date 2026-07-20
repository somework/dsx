package syncer

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

func TestCheckLedgerHomeAcceptsAnAbsentOrOrdinaryDirectory(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		dir := t.TempDir()
		if err := checkLedgerHome(dir); err != nil {
			t.Errorf("checkLedgerHome refused an absent .dsx: %v", err)
		}
	})
	t.Run("ordinary directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(StateDir(dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := checkLedgerHome(dir); err != nil {
			t.Errorf("checkLedgerHome refused an ordinary .dsx directory: %v", err)
		}
	})
}

func TestCheckLedgerHomeRefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, StateDir(dir)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	err := checkLedgerHome(dir)
	if err == nil {
		t.Fatal("checkLedgerHome accepted a symlinked .dsx")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindLocal {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindLocal)
	}
	// Not just the generic "is not a directory": a symlink needs its own wording
	// because os.Lstat never reports IsDir()==true for one, so the symlink check
	// must run before the generic !IsDir() check or this diagnostic is silently
	// lost while Kind stays KindLocal either way.
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("message does not name the symlink specifically: %s", err.Error())
	}
	// Writes nothing, creates nothing: the symlink's target must stay empty.
	entries, rdErr := os.ReadDir(outside)
	if rdErr != nil {
		t.Fatal(rdErr)
	}
	if len(entries) != 0 {
		t.Errorf("checkLedgerHome wrote through the symlink: %v", entries)
	}
}

func TestCheckLedgerHomeRefusesARegularFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(StateDir(dir), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := checkLedgerHome(dir)
	if err == nil {
		t.Fatal("checkLedgerHome accepted a .dsx regular file")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindLocal {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindLocal)
	}
}

// LoadState must not surface the raw ENOTDIR a .dsx regular file produces
// when os.ReadFile tries to open a path through it: that is a filesystem
// implementation detail of where the ledger lives, not a ledger-corruption
// finding, and dsxerr.Classify would otherwise report it as a generic local
// I/O failure rather than routing through checkLedgerHome's named refusal.
// A missing ledger and an unreadable-because-blocked one both mean "nothing
// to load here" from LoadState's perspective; checkLedgerHome is the one
// place that judges the shape a hazard.
func TestLoadStateTreatsANonDirectoryLedgerHomeAsNoLedger(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(StateDir(dir), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState surfaced the ledger-home shape as an error: %v", err)
	}
	if st.ProjectID != "" || len(st.Files) != 0 {
		t.Errorf("state=%+v, want empty", st)
	}
}

func TestEnsureLedgerHomeHonoursUmaskAndNeverChmods(t *testing.T) {
	// Both cases are needed, matching TestWriteAtomicHonoursUmaskOnANewFile:
	// under 077 a hardcoded 0755 and a umask-filtered 0755 agree, so that case
	// alone proves nothing.
	for _, tc := range []struct {
		umask int
		want  os.FileMode
	}{
		{0o077, 0o700},
		{0o022, 0o755},
	} {
		// syscall.Umask is process-global; this test must not run in parallel.
		old := syscall.Umask(tc.umask)

		dir := t.TempDir()
		err := ensureLedgerHome(dir)
		var got os.FileMode
		if err == nil {
			if fi, statErr := os.Stat(StateDir(dir)); statErr == nil {
				got = fi.Mode().Perm()
			} else {
				err = statErr
			}
		}
		syscall.Umask(old)

		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("mode = %o under umask %o, want %o", got, tc.umask, tc.want)
		}
	}
}

func TestSaveCreatesTheLedgerHomeDirectory(t *testing.T) {
	dir := t.TempDir()
	if _, err := os.Stat(StateDir(dir)); err == nil {
		t.Fatal(".dsx already exists before save")
	}

	if err := (State{Files: map[string]FileState{}}).save(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(StateDir(dir)); err != nil {
		t.Errorf("save did not create .dsx: %v", err)
	}
	if _, err := os.Stat(StatePath(dir)); err != nil {
		t.Errorf("save did not write the ledger: %v", err)
	}
}

// The ledger's on-disk mode is a compatibility contract, unaffected by
// ensureLedgerHome's own MkdirAll mode.
func TestLedgerFileModeIsStill0600(t *testing.T) {
	dir := t.TempDir()
	if err := (State{Files: map[string]FileState{}}).save(dir); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(StatePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("ledger mode = %o, want 0600", got)
	}
}

func TestCheckLedgerHomeNamesThePath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(StateDir(dir), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := checkLedgerHome(dir)
	if err == nil {
		t.Fatal("want a refusal")
	}
	want := filepath.Join(dir, DirName)
	if got := err.Error(); !strings.Contains(got, want) {
		t.Errorf("refusal does not name %q: %s", want, got)
	}
}
