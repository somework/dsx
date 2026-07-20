package syncer

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/somework/dsx/internal/dsxerr"
)

// tempPrefix reuses the ledger's name so builtinIgnores already covers it:
// StateFileName+"*" compiles unanchored and matches at any depth, which is
// what these need now that temps are born in subdirectories. Four protections
// come free — and so does the one that matters most, an OLD dsx binary hiding
// them too. A private glob would wedge the versions: a new binary drops a file
// the old one does not know, the old one pushes that fragment to the server,
// and the new one then refuses the whole pull until it is deleted by hand.
const tempPrefix = StateFileName + ".part-"

// writeAtomic replaces path's contents in one step. os.WriteFile opens with
// O_TRUNC, so its destroying step strictly precedes its creating one: a kill
// mid-write leaves a dense prefix of the new bytes and none of the old, which
// the next run reports to its owner as "local differs" — dsx's own wreckage,
// blamed on the caller, with --force offered as the cure. save() has written
// the ledger this way all along; the files dsx exists to move did not get it.
//
// No fsync: the threat model is a killed process, against which a returned
// rename is complete protection. Power loss is a different property, needs the
// file and its parent synced, and on darwin costs an F_FULLFSYNC per file —
// save() does not pay it either.
//
// WriteAtomic is the same for callers outside the package: `cat --out` writes
// a path the user named, and it is the very command conflictHint prescribes as
// the way out of a conflict — leaving it destructive would half-fix the
// asymmetry and leave the documented cure able to destroy.
func WriteAtomic(path string, data []byte) error { return writeAtomic(path, data) }

func writeAtomic(path string, data []byte) error {
	return writeAtomicInto(filepath.Dir(path), path, data, nil)
}

// writeAtomicInto is writeAtomic with a seam for tests: afterOpen runs with the
// temp's path once it exists and before the rename.
func writeAtomicInto(_ string, path string, data []byte, afterOpen func(string) error) error {
	keepPerm, err := inspectDestination(path)
	if err != nil {
		return err
	}

	// Not os.CreateTemp: it hardcodes 0600, so umask can no longer apply and a
	// Chmod afterwards would bypass umask entirely — widening the mode for a
	// caller running umask 077. Opening with 0644 reproduces what os.WriteFile
	// did byte for byte, because the kernel applies umask at creation.
	dir := filepath.Dir(path)
	f, tmp, err := createTemp(dir)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)

	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	// The mode is preserved, never assigned: os.WriteFile applies its perm only
	// when creating, so a chmod +x on a pulled script survives every pull today.
	// Through the fd, not the path, so nothing can be swapped underneath.
	if keepPerm != 0 {
		if err := f.Chmod(keepPerm); err != nil {
			f.Close()
			return err
		}
	}
	// Checked, not deferred: a delayed write error surfaces only here.
	if err := f.Close(); err != nil {
		return err
	}
	if afterOpen != nil {
		if err := afterOpen(tmp); err != nil {
			return err
		}
	}
	return os.Rename(tmp, path)
}

// inspectDestination reads the existing file once and answers three questions:
// whether to refuse, and with which of two messages, and which mode to carry
// over. It returns 0 when there is nothing to carry.
func inspectDestination(path string) (os.FileMode, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if fi.IsDir() {
		return 0, &dsxerr.Error{Kind: dsxerr.KindLocal, Msg: fmt.Sprintf(
			"%s is a directory here, so the server's file of that name cannot be written",
			path)}
	}
	perm := fi.Mode().Perm()
	// A rename checks write permission on the DIRECTORY, so a read-only file
	// would be replaced without a word — today os.WriteFile fails and the file
	// survives. The gesture is kept, and the refusal lands before the act
	// (invariant 16).
	if fi.Mode().IsRegular() && perm&0o200 == 0 {
		return 0, &dsxerr.Error{Kind: dsxerr.KindLocal, Msg: fmt.Sprintf(
			"%s is read-only (%o) — dsx left it alone; `chmod +w %s` to let the sync replace it",
			path, perm, path)}
	}
	if !fi.Mode().IsRegular() {
		return 0, nil
	}
	return perm, nil
}

func createTemp(dir string) (*os.File, string, error) {
	var last error
	for range 100 {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, "", err
		}
		name := filepath.Join(dir, tempPrefix+hex.EncodeToString(b[:]))
		f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			return f, name, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
		last = err
	}
	return nil, "", last
}
