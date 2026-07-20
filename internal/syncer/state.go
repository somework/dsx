package syncer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/somework/dsx/internal/dsxerr"
)

// DirName is the ledger's home directory; exported because cmd packages need
// it (put's nearby-ledger warning, checkCloneTarget's collision probe).
const DirName = ".dsx"

// legacyStateFileName is the frozen wire-compatible spelling of the ledger's
// pre-.dsx/ name. Anything an already-shipped binary must still recognise —
// tempPrefix, checkRemotePath's ledger refusal, the builtinIgnores glob —
// reads this literal directly, never through StatePath.
const legacyStateFileName = ".dsx-state.json"

const caseProbeName = ".dsx-case-probe"

// StateDir is the ledger's home directory.
func StateDir(dir string) string { return filepath.Join(dir, DirName) }

// StatePath is the ledger's on-disk location. It still resolves to the
// pre-.dsx/ root path here; a later commit flips it to StateDir.
func StatePath(dir string) string { return filepath.Join(dir, legacyStateFileName) }

// LegacyStatePath is the frozen pre-.dsx/ ledger location. A later commit
// reads it as LoadState's fallback once StatePath moves.
func LegacyStatePath(dir string) string { return filepath.Join(dir, legacyStateFileName) }

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
	b, err := os.ReadFile(StatePath(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return State{Files: map[string]FileState{}}, nil
	}
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, fmt.Errorf("%s is corrupt: %w", legacyStateFileName, err)
	}
	if s.Files == nil {
		s.Files = map[string]FileState{}
	}
	// A files map with no project is the one shape both bind guards short-circuit
	// on — they compare only when ProjectID is set — so such a ledger is accepted
	// as any project's while its etags stay fully trusted by the prune loop. An
	// empty files map with no project is just an unsynced directory.
	if s.ProjectID == "" && len(s.Files) > 0 {
		return State{}, &dsxerr.Error{Kind: dsxerr.KindLocal, Msg: fmt.Sprintf(
			"%s tracks %d files but names no project, so nothing can say which server "+
				"they came from; the ledger is damaged — remove it and run "+
				"`dsx pull <project> %s`, which reports the files as conflicts rather "+
				"than overwriting them",
			StatePath(dir), len(s.Files), dir)}
	}
	return s, nil
}

func (s State) save(dir string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')

	tmp, err := os.CreateTemp(dir, legacyStateFileName+".*")
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
	return os.Rename(tmp.Name(), StatePath(dir))
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

	// filepath.WalkDir does not descend a symlinked root: Lstat sees a symlink,
	// not a directory, so the walk stops after one call and the scan comes back
	// empty. An empty scan is a data-loss trigger — pull overwrites every real
	// file through the link with no conflict, push --prune deletes the whole
	// tracked tree. Resolve a symlinked root so its contents are actually walked;
	// rel keys stay relative to the resolved root, matching what safeJoin
	// resolves for writes. The common non-symlink path is untouched.
	root := dir
	if fi, err := os.Lstat(dir); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return nil, err
		}
		if ri, err := os.Stat(resolved); err == nil && ri.IsDir() {
			root = resolved
		}
	}

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
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

// sameEndpoint reports whether two endpoints name the same server. Only scheme
// and host decide: a vendor moving /v1/design/mcp to /v2 must not strand every
// ledger on disk, while a different host is a different server whose listing
// says nothing about our files. Unparseable strings fall back to an exact
// comparison rather than resolving to a permissive zero value.
func sameEndpoint(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return strings.EqualFold(ua.Scheme, ub.Scheme) && strings.EqualFold(ua.Host, ub.Host)
}

// conflictHint names the one route out of a conflict. It says "synced dir"
// because conflict paths are relative to the directory that was synced, which
// is not necessarily the shell's, and `dsx cat` reads its project from the
// ledger of the shell's.
const conflictHint = "  in the synced dir: dsx cat <path> --out /tmp/a && diff /tmp/a <path>"

// endpointRefusal names both servers and the one env var that explains the
// drift. It never suggests removing the ledger: clearing it makes every path
// untracked, and planPush then leaves IfMatch empty under --force.
func endpointRefusal(dir, ledger, target, verb string) error {
	return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
		"%s was synced against %s, but this run targets %s; refusing to %s — "+
			"that server's listing describes different files, and acting on it would "+
			"read every tracked path as deleted (unset DSX_ENDPOINT to go back)",
		StatePath(dir), ledger, target, verb)}
}

// checkRemotePath compares case-insensitively because APFS folds case: on a
// case-insensitive filesystem `.GIT/config` is `.git/config`, so a
// case-sensitive guard is no guard.
func checkRemotePath(rel string) error {
	if strings.EqualFold(rel, legacyStateFileName) {
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
	return builtinIgnoreSet().match(name)
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
	// Refuse an in-tree symlinked directory component too. Writing through it
	// lands the bytes under the resolved name while the ledger keys off this
	// path, so scanLocal finds the same bytes under both names and reports a
	// perpetual "changed" churn. Ask the filesystem — Lstat each existing
	// intermediate component rather than fold names in Go.
	parts := strings.Split(clean, string(filepath.Separator))
	for i := 1; i < len(parts); i++ {
		inter := filepath.Join(root, filepath.Join(parts[:i]...))
		fi, err := os.Lstat(inter)
		if err != nil {
			break // this component does not exist yet, so nothing below it can
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing to write through the symlinked directory %q", filepath.ToSlash(filepath.Join(parts[:i]...)))
		}
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

// nfcProbeName and nfdProbeName spell the same accented name two ways: é as one
// code point, and e + a combining acute accent. A filesystem that stores names
// normalization-insensitively (APFS) resolves both to one file.
const (
	nfcProbeName = "\u00e9" + caseProbeName  // \u00e9 as one code point
	nfdProbeName = "e\u0301" + caseProbeName // e + combining acute accent
)

func normalizationInsensitiveDir(dir string) bool {
	name := filepath.Join(dir, nfcProbeName)
	f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return false
	}
	f.Close()
	defer os.Remove(name)

	_, err = os.Stat(filepath.Join(dir, nfdProbeName))
	return err == nil
}

// dirFolds reports whether this filesystem collapses distinct names onto one
// file — by case or by Unicode normalization. Both destroy data on a pull, and
// stdlib can fold neither in Go, so the guard must ask the filesystem.
func dirFolds(dir string) bool {
	return caseInsensitiveDir(dir) || normalizationInsensitiveDir(dir)
}

// remoteFoldCollisions asks the filesystem which remote paths would land on the
// same file, without folding names in Go: it recreates every path in a throwaway
// mirror on the same volume and groups by inode via os.SameFile. This catches
// any folding the filesystem does — case, NFC/NFD, and anything else — where a
// Go-side normaliser would need a dependency we refuse to add.
func remoteFoldCollisions(remote map[string]RemoteEntry, dir string) (map[string]bool, error) {
	probe, err := os.MkdirTemp(dir, caseProbeName+"-fold-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(probe)

	type probed struct {
		path string
		fi   os.FileInfo
	}
	var created []probed
	collided := map[string]bool{}

	for _, path := range SortedPaths(remote) {
		// safeJoin, not filepath.Join: a remote path is untrusted (invariant 7),
		// so an escaping name like "../../evil" must never create a file outside
		// the probe. An unsafe path is skipped here and refused later by
		// checkRemotePath at write time.
		full, err := safeJoin(probe, path)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(full, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		f.Close()
		fi, err := os.Lstat(full)
		if err != nil {
			return nil, err
		}
		for _, pr := range created {
			if os.SameFile(fi, pr.fi) {
				collided[path] = true
				collided[pr.path] = true
			}
		}
		created = append(created, probed{path: path, fi: fi})
	}
	return collided, nil
}

func checkPathCollisions(remote map[string]RemoteEntry, local map[string]localFile, dir string) error {
	if len(remote) == 0 || !dirFolds(dir) {
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

	// remote×remote folds. strings.ToLower cannot see NFC/NFD (café composed vs
	// café decomposed are distinct byte strings that are one file on APFS), so
	// ask the filesystem instead of folding names in Go.
	remoteFolds, err := remoteFoldCollisions(remote, dir)
	if err != nil {
		return err
	}
	for p := range remoteFolds {
		collided[p] = true
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

// LocalIsEmpty reports whether dir holds nothing dsx would sync. It asks
// through survey's own filter rather than reading the directory directly:
// survey is the only filter (invariant 9), and a second view of the same
// directory would answer differently about .DS_Store, .git, a pre-written
// .dsxignore and dsx's own temporary files. A directory that does not exist
// holds nothing.
func LocalIsEmpty(dir string) (bool, error) {
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	// Through survey, not loadIgnore+scanLocal by hand: survey is the only
	// filter (invariant 9) and TestSyncCallersCannotFilterOneSide enforces it.
	// The empty remote is honest — emptiness is a question about one side only,
	// so there is no listing to filter.
	_, local, err := survey(dir, map[string]RemoteEntry{})
	if err != nil {
		return false, err
	}
	return len(local) == 0, nil
}
