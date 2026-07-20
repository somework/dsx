package syncer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func readBack(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// os.WriteFile opens with O_TRUNC, so the destroying step strictly precedes
// the creating one: a kill mid-write leaves a dense prefix of the new content
// and none of the old. dsx then reports that prefix to its owner as "local
// differs; --force to overwrite" — its own wreckage, blamed on the caller,
// with the one destructive flag offered as the cure.
func TestWriteAtomicKeepsTheOldBytesWhenTheWriteFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.css")
	if err := os.WriteFile(path, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A directory where the temp file wants to be is the cheapest injected
	// failure that lands after the destination has been inspected.
	inject := func(string) error { return errors.New("injected") }
	if err := writeAtomicInto(dir, path, []byte("new"), inject); err == nil {
		t.Fatal("the injected failure did not surface")
	}
	if got := readBack(t, path); got != "ORIGINAL" {
		t.Errorf("destination = %q, want the exact pre-write bytes", got)
	}
}

// A file dsx pulled and the caller marked executable must stay executable:
// os.WriteFile applies its perm argument only when creating, so today a
// chmod +x survives every pull. A flat Chmod(0644) would strip it on every run.
func TestWriteAtomicPreservesTheDestinationsMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hook.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := writeAtomic(path, []byte("#!/bin/sh\ntrue\n")); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, path); got != 0o755 {
		t.Errorf("mode = %o, want 0755 — the executable bit did not survive", got)
	}
}

// A new file gets its mode from the kernel applying umask, exactly as
// os.WriteFile(…, 0o644) does. os.CreateTemp cannot be used for this: it
// hardcodes 0600, and a flat Chmod(0644) afterwards bypasses umask and would
// WIDEN the mode for a caller running umask 077.
func TestWriteAtomicHonoursUmaskOnANewFile(t *testing.T) {
	// Both cases are needed. Under 077 a hardcoded 0600 and a umask-filtered
	// 0644 agree, so that case alone proves nothing; under 022 they differ, and
	// that is the case that catches os.CreateTemp's 0600.
	for _, tc := range []struct {
		umask int
		want  os.FileMode
	}{
		{0o077, 0o600},
		{0o022, 0o644},
	} {
		// syscall.Umask is process-global; this test must not run in parallel.
		old := syscall.Umask(tc.umask)

		dir := t.TempDir()
		path := filepath.Join(dir, "new.css")
		err := writeAtomic(path, []byte("a{}"))
		var got os.FileMode
		if err == nil {
			if fi, statErr := os.Stat(path); statErr == nil {
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
			t.Errorf("mode = %o under umask %o, want %o — os.WriteFile(0o644) gives %o here",
				got, tc.umask, tc.want, tc.want)
		}
	}
}

func TestWriteAtomicWritesTheBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.css")
	if err := writeAtomic(path, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if got := readBack(t, path); got != "hello" {
		t.Errorf("content = %q", got)
	}
}

// A read-only file is a gesture. Today os.WriteFile fails with EACCES and the
// file survives; after a rename the write permission is checked on the
// directory, so the file would be replaced without a word. The refusal lands
// before the act (invariant 15) and keeps today's outcome.
func TestWriteAtomicRefusesAReadOnlyDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "locked.css")
	if err := os.WriteFile(path, []byte("LOCKED"), 0o444); err != nil {
		t.Fatal(err)
	}

	err := writeAtomic(path, []byte("new"))
	if err == nil {
		t.Fatal("a read-only destination was replaced silently")
	}
	if !strings.Contains(err.Error(), "locked.css") {
		t.Errorf("refusal does not name the file:\n%s", err)
	}
	if got := readBack(t, path); got != "LOCKED" {
		t.Errorf("content = %q, want it untouched", got)
	}
	// Nothing left behind: a refusal leaves no trace.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want only the destination", len(entries))
	}
}

// A path held by a directory must be named as such, not surfaced as a
// synthesised EEXIST from the rename.
func TestWriteAtomicRefusesAPathHeldByADirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "components")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}

	err := writeAtomic(path, []byte("x"))
	if err == nil {
		t.Fatal("a directory was accepted as a write destination")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("refusal does not say it is a directory:\n%s", err)
	}
}

// The temp lands beside the destination, never in os.TempDir(): a repository
// under /Volumes or on a disk image would give EXDEV, and that implementation
// passes every test on a developer's machine.
func TestWriteAtomicPutsItsTempBesideTheDestination(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "deep", "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sub, "a.css")

	var seen string
	if err := writeAtomicInto(dir, path, []byte("x"), func(tmp string) error {
		seen = tmp
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := filepath.Dir(seen); got != sub {
		t.Errorf("temp was created in %s, want %s — a cross-device rename is EXDEV", got, sub)
	}
}

// The temp name reuses the ledger's prefix so builtinIgnores already hides it
// — four protections and, crucially, an OLD dsx binary hides it too. A private
// glob would wedge the versions: the new binary drops a file the old one does
// not know, the old one pushes that fragment to the server, and the new one
// then refuses the whole pull until it is deleted by hand.
func TestTheTempPrefixIsAlreadyCoveredByBuiltinIgnores(t *testing.T) {
	name := tempPrefix + "abc123"
	if !isBuiltinIgnoredName(name) {
		t.Errorf("%q is not a builtin ignore; it would be scanned, pushed, and pruned", name)
	}
	ig, err := parseIgnore("")
	if err != nil {
		t.Fatal(err)
	}
	if !ig.match("components/" + name) {
		t.Errorf("%q is not ignored at depth — temps are born in subdirectories now", name)
	}
	if err := checkRemotePath("components/" + name); err == nil {
		t.Errorf("a remote path named %q was accepted", name)
	}
}

// The unit tests above prove the helper; these prove Pull actually calls it.
// Without them the wiring is a one-line change nothing holds in place.
func TestPullPreservesAnExecutableBitAcrossARefetch(t *testing.T) {
	dir := t.TempDir()
	body := "#!/bin/sh\n"
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(fileEntry("hook.sh", "e2", int64(len(body))))}
		}
		return fakeReply{Text: envelopeFor("hook.sh", "e2", body)}
	})

	path := filepath.Join(dir, "hook.sh")
	if err := os.WriteFile(path, []byte("old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Pull(t.Context(), fakeClient(f), PullOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 1, Force: true,
	}); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, path); got != 0o755 {
		t.Errorf("mode = %o after a pull, want 0755 — Pull is not going through writeAtomic", got)
	}
}

func TestPullRefusesToReplaceAReadOnlyFile(t *testing.T) {
	dir := t.TempDir()
	body := "new\n"
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(fileEntry("locked.css", "e2", int64(len(body))))}
		}
		return fakeReply{Text: envelopeFor("locked.css", "e2", body)}
	})

	path := filepath.Join(dir, "locked.css")
	if err := os.WriteFile(path, []byte("LOCKED"), 0o444); err != nil {
		t.Fatal(err)
	}
	_, err := Pull(t.Context(), fakeClient(f), PullOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 1, Force: true,
	})
	if err == nil {
		t.Fatal("Pull replaced a read-only file")
	}
	// os.WriteFile also fails here, with a bare "permission denied", and also
	// leaves the file intact — so neither of those discriminates. The actionable
	// text is what only the atomic path produces, and asserting it is what holds
	// the wiring in place.
	if !strings.Contains(err.Error(), "chmod +w") {
		t.Errorf("refusal does not name the fix, so Pull is not going through writeAtomic:\n%s", err)
	}
	if got := readBack(t, path); got != "LOCKED" {
		t.Errorf("content = %q, want it untouched", got)
	}
}
