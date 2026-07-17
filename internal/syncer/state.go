package syncer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/somework/dsx/internal/dsxerr"
)

const StateFileName = ".dsx-state.json"

const caseProbeName = ".dsx-case-probe"

type FileState struct {
	Etag   string `json:"etag"`
	Size   int64  `json:"size"`
	SHA    string `json:"sha256"`
	Binary bool   `json:"binary,omitempty"`
}

type State struct {
	ProjectID string               `json:"project_id"`
	Endpoint  string               `json:"endpoint,omitempty"`
	Files     map[string]FileState `json:"files"`
}

func LoadState(dir string) (State, error) {
	b, err := os.ReadFile(filepath.Join(dir, StateFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return State{Files: map[string]FileState{}}, nil
	}
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, fmt.Errorf("%s is corrupt: %w", StateFileName, err)
	}
	if s.Files == nil {
		s.Files = map[string]FileState{}
	}
	return s, nil
}

func (s State) save(dir string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')

	tmp, err := os.CreateTemp(dir, StateFileName+".*")
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
	return os.Rename(tmp.Name(), filepath.Join(dir, StateFileName))
}

func (s State) withFile(path string, fst FileState) State {
	files := make(map[string]FileState, len(s.Files)+1)
	for k, v := range s.Files {
		files[k] = v
	}
	files[path] = fst
	return State{ProjectID: s.ProjectID, Endpoint: s.Endpoint, Files: files}
}

func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type localFile struct {
	Path string
	Size int64
	SHA  string

	Irregular bool
}

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
			if ig.canSkipDir(rel) {
				return fs.SkipDir
			}
			return nil
		}
		if ig.match(rel) {
			return nil
		}

		if !d.Type().IsRegular() {
			out[rel] = localFile{Path: rel, Irregular: true}
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = localFile{Path: rel, Size: int64(len(b)), SHA: SHA256Hex(b)}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// checkRemotePath compares case-insensitively because APFS folds case: on a
// case-insensitive filesystem `.GIT/config` is `.git/config`, so a
// case-sensitive guard is no guard.
func checkRemotePath(rel string) error {
	if strings.EqualFold(rel, StateFileName) {
		return fmt.Errorf("refusing remote path %q: it would overwrite dsx's own ledger", rel)
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if isBuiltinIgnoredName(part) {
			return fmt.Errorf("refusing remote path %q: %q is local-only", rel, part)
		}
	}
	return nil
}

func isBuiltinIgnoredName(name string) bool {
	for _, b := range builtinIgnores {
		if strings.EqualFold(b, name) {
			return true
		}
	}
	return false
}

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

func caseInsensitiveDir(dir string) bool {
	name := filepath.Join(dir, caseProbeName)
	f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return false
	}
	f.Close()
	defer os.Remove(name)

	_, err = os.Stat(filepath.Join(dir, strings.ToUpper(caseProbeName)))
	return err == nil
}

func checkPathCollisions(remote map[string]RemoteEntry, local map[string]localFile, dir string) error {
	if len(remote) == 0 || !caseInsensitiveDir(dir) {
		return nil
	}

	collided := map[string]bool{}

	for path := range remote {
		if _, exact := local[path]; exact {
			continue
		}
		full, err := safeJoin(dir, path)
		if err != nil {
			continue
		}
		if _, err := os.Lstat(full); err != nil {
			continue
		}
		collided[path] = true
		for other := range local {
			if strings.EqualFold(other, path) {
				collided[other] = true
			}
		}
	}

	byFold := map[string][]string{}
	for path := range remote {
		k := strings.ToLower(path)
		byFold[k] = append(byFold[k], path)
	}
	for _, paths := range byFold {
		if len(paths) > 1 {
			for _, p := range paths {
				collided[p] = true
			}
		}
	}

	if len(collided) == 0 {
		return nil
	}
	names := make([]string, 0, len(collided))
	for p := range collided {
		names = append(names, p)
	}
	slices.Sort(names)
	return dsxerr.Conflict(names, fmt.Sprintf(
		"%s cannot hold these paths apart — its filesystem folds names that the server keeps "+
			"distinct, so syncing them would land several files in one and destroy all but the last. "+
			"Rename them in the project, or sync to a case-sensitive volume", dir))
}
