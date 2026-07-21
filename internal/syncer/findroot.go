package syncer

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// FindRoot walks up from start looking for the nearest directory that holds a
// ledger, the way git finds .git. It answers "" rather than an error when
// nothing above start is bound: an unbound directory is an ordinary state the
// caller has its own words for, not a failure.
//
// The probe is state.json, never .dsx itself. fetch creates the directory to
// hold baseline.json, so a directory that has only ever been fetched into
// holds a .dsx and no binding — answering with it would hand back a root whose
// LoadState comes straight back unbound. ErrNotExist and ENOTDIR both mean
// keep walking, pairing them for the reason LoadState does: a .dsx that is a
// regular file makes state.json unreachable exactly as a missing one does.
//
// The nearest binding wins. Whoever bound an inner directory chose it, and
// reaching past it would sync a tree they never named.
//
// The walk is lexical, with no EvalSymlinks. filepath.Abs resolves a relative
// start through os.Getwd, which is already the physical path, so the case that
// matters — a caller standing somewhere inside a synced tree — is physical
// without asking. Resolving on top of that would only rewrite the path the
// caller typed into one they did not, in every message printed below.
func FindRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		switch _, err := os.Stat(filepath.Join(dir, DirName, stateBaseName)); {
		case err == nil:
			return dir, nil
		case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
			// Not a root. Keep walking.
		default:
			return "", err
		}

		// filepath.Dir("/") is "/", so the loop has to compare against its own
		// parent or it never ends.
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}
