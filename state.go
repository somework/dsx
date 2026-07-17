package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const stateFileName = ".dsx-state.json"

// fileState records what we last agreed on with the server for one path:
// the server's etag, and the bytes we held at that etag.
//
// Binary marks a path the server will not serve back (read_file is text-only).
// Such an entry has no local bytes, so SHA and Size stay zero and it must
// never be treated as a file we hold -- see prune.
type fileState struct {
	Etag   string `json:"etag"`
	Size   int64  `json:"size"`
	SHA    string `json:"sha256"`
	Binary bool   `json:"binary,omitempty"`
}

type syncState struct {
	ProjectID string               `json:"project_id"`
	Endpoint  string               `json:"endpoint,omitempty"`
	Files     map[string]fileState `json:"files"`
}

func loadState(dir string) (syncState, error) {
	b, err := os.ReadFile(filepath.Join(dir, stateFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return syncState{Files: map[string]fileState{}}, nil
	}
	if err != nil {
		return syncState{}, err
	}
	var s syncState
	if err := json.Unmarshal(b, &s); err != nil {
		return syncState{}, fmt.Errorf("%s is corrupt: %w", stateFileName, err)
	}
	if s.Files == nil {
		s.Files = map[string]fileState{}
	}
	return s, nil
}

// save writes the state atomically so an interrupted run cannot leave a
// half-written ledger that would desync every later sync.
func (s syncState) save(dir string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')

	tmp, err := os.CreateTemp(dir, stateFileName+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, stateFileName))
}

// withFile returns a copy carrying one updated entry; the receiver is untouched.
func (s syncState) withFile(path string, fst fileState) syncState {
	files := make(map[string]fileState, len(s.Files)+1)
	for k, v := range s.Files {
		files[k] = v
	}
	files[path] = fst
	return syncState{ProjectID: s.ProjectID, Endpoint: s.Endpoint, Files: files}
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// localFile is one path found on disk.
type localFile struct {
	Path string // project-relative, slash-separated
	Size int64
	SHA  string

	// Irregular marks a path that exists but is not a regular file -- a
	// symlink, a fifo, a device. dsx holds no bytes for it: Size and SHA stay
	// zero.
	//
	// Such a path is still recorded, and that is the point. Dropping it from the
	// scan entirely made it indistinguishable from a file the user deleted, and
	// `push --prune` deleted it from the server -- with a matching if_match, so
	// the server complied. A symlink is not proof of a deletion; it is proof of
	// nothing, and invariant 4 says dsx deletes only what it can prove.
	Irregular bool
}

// scanLocal walks dir and returns every file keyed by project-relative path,
// minus whatever the ignore set excludes.
func scanLocal(dir string, ig *ignoreSet) (map[string]localFile, error) {
	out := map[string]localFile{}

	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			// Pruning the walk at an excluded directory matters beyond speed:
			// node_modules can hold hundreds of thousands of files, and
			// scanLocal reads every file it does not skip.
			if ig.canSkipDir(rel) {
				return fs.SkipDir
			}
			return nil
		}
		if ig.match(rel) {
			return nil
		}
		// Symlinks are reported by WalkDir without following. dsx must not
		// upload whatever they point at -- but it must not forget them either,
		// or prune reads their absence as a deletion. Record, do not read.
		if !d.Type().IsRegular() {
			out[rel] = localFile{Path: rel, Irregular: true}
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = localFile{Path: rel, Size: int64(len(b)), SHA: sha256hex(b)}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// checkRemotePath refuses remote paths that must never be written locally even
// though they stay inside the target directory, so safeJoin cannot catch them:
// VCS metadata, dependency trees, and dsx's own ledger. A project we do not
// control decides these names.
func checkRemotePath(rel string) error {
	if strings.EqualFold(rel, stateFileName) {
		return fmt.Errorf("refusing remote path %q: it would overwrite dsx's own ledger", rel)
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if isBuiltinIgnoredName(part) {
			return fmt.Errorf("refusing remote path %q: %q is local-only", rel, part)
		}
	}
	return nil
}

// isBuiltinIgnoredName reports a path segment that is never the project's, no
// matter what the server calls it. Reading the list the ignore rules are built
// from is what keeps this guard and those rules from drifting apart.
//
// The comparison is case-insensitive because the filesystem is. macOS ships
// case-insensitive APFS by default, so ".GIT/config" IS ".git/config" and
// ".DSX-STATE.JSON" IS the ledger -- a case-sensitive guard would let a project
// we do not control walk straight past invariant 7. Refusing a genuine `.GIT`
// on a case-sensitive volume costs nothing: nobody ships one.
func isBuiltinIgnoredName(name string) bool {
	for _, b := range builtinIgnores {
		if strings.EqualFold(b, name) {
			return true
		}
	}
	return false
}

// safeJoin resolves a project-relative path under root, refusing anything that
// would escape it. Remote paths are untrusted input.
func safeJoin(root, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("empty path")
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("absolute path refused: %q", rel)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes target directory: %q", rel)
	}
	full := filepath.Join(root, clean)

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	if absFull != absRoot && !strings.HasPrefix(absFull, absRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes target directory: %q", rel)
	}

	// The checks above are lexical, and a symlink is not visible to them: a
	// link planted inside the target directory keeps every path string within
	// the root while sending the actual write anywhere on the filesystem.
	// Resolving the deepest existing ancestor is what closes that.
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", fmt.Errorf("resolving target directory %q: %w", root, err)
	}
	if fi, err := os.Lstat(absFull); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing to write through the symlink %q", rel)
	}
	ancestor := absFull
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("no existing ancestor for %q", rel)
		}
		ancestor = parent
	}
	realAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", fmt.Errorf("resolving %q: %w", rel, err)
	}
	if realAncestor != realRoot && !strings.HasPrefix(realAncestor, realRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes target directory via a symlink: %q", rel)
	}
	return full, nil
}
