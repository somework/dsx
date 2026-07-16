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

// localFile is one file found on disk.
type localFile struct {
	Path string // project-relative, slash-separated
	Size int64
	SHA  string
}

// scanLocal walks dir and returns every file keyed by project-relative path.
// The state ledger and any VCS metadata are not part of the project.
func scanLocal(dir string) (map[string]localFile, error) {
	out := map[string]localFile{}

	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if isIgnoredDir(d.Name()) && p != dir {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == stateFileName {
			return nil
		}
		// Symlinks are reported by WalkDir without following; refuse to
		// upload whatever they point at.
		if !d.Type().IsRegular() {
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

func isIgnoredDir(name string) bool {
	switch name {
	case ".git", ".svn", ".hg", "node_modules", ".DS_Store":
		return true
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
	return full, nil
}
