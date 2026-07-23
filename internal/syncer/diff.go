package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/fmtutil"
	"github.com/somework/dsx/internal/mcp"
)

type DiffOpts struct {
	ProjectID   string
	Dir         string
	Out         string
	Concurrency int

	// Where the transfer counter draws, nil for silence. See PullOpts.Progress.
	Progress io.Writer
}

// DiffPair names a path present on both sides whose bytes disagree.
type DiffPair struct {
	Path       string `json:"path"`
	LocalSize  int64  `json:"local_size"`
	RemoteSize int64  `json:"remote_size"`
}

// DiffReport carries the four classifications as distinct keys so an agent
// branches on them rather than parsing a rendered line.
type DiffReport struct {
	Same       []string   `json:"same"`
	LocalOnly  []string   `json:"local_only"`
	RemoteOnly []string   `json:"remote_only"`
	Differs    []DiffPair `json:"differs"`

	Incomplete bool `json:"incomplete,omitempty"`
}

// Diff classifies every path into exactly one of same/local-only/remote-only/
// differs. It never prints a hunk — dsx exists so bytes do not pass through a
// model's context, and a unified diff on stdout is bytes in context. Out, when
// set, materialises the remote side of a "differs" path so the caller's own
// diff -ru does the work.
//
// A path is proved "same" with no download by either map that can carry the
// proof — a fresh baseline entry, or a ledger entry recorded at the etag the
// listing still shows. Every other present-both path must be downloaded to
// classify. Diff reads both and refreshes neither: fetch writes, diff reads.
func Diff(ctx context.Context, c *mcp.Client, o DiffOpts) (DiffReport, error) {
	var rep DiffReport

	st, err := LoadState(o.Dir)
	if err != nil {
		return rep, err
	}
	if st.ProjectID != "" && st.ProjectID != o.ProjectID {
		return rep, &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
			"%s is bound to project %s; refusing to diff %s against it",
			StatePath(o.Dir), st.ProjectID, o.ProjectID)}
	}
	if st.Endpoint != "" && !sameEndpoint(st.Endpoint, c.Endpoint()) {
		return rep, endpointRefusal(o.Dir, st.Endpoint, c.Endpoint(), "diff")
	}
	if err := checkLedgerHome(o.Dir); err != nil {
		return rep, err
	}

	// --out is refused before the round trip, following clone's rule
	// (invariant 16): a refusal after list_files has already spent what it
	// was refusing to spend.
	if o.Out != "" {
		empty, err := LocalIsEmpty(o.Out)
		if err != nil {
			return rep, err
		}
		if !empty {
			return rep, &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
				"%s is not empty — diff --out starts a new directory; name an empty one", o.Out)}
		}
		// Created once, up front, the way clone creates its target after
		// checkCloneTarget passes: safeJoin below resolves Out through
		// filepath.EvalSymlinks, which requires the root to already exist.
		if err := os.MkdirAll(o.Out, 0o755); err != nil {
			return rep, err
		}
	}

	bl, err := loadBaseline(o.Dir)
	if err != nil {
		return rep, err
	}
	baseline := map[string]BaselineEntry{}
	if bl.bound(o.ProjectID, c.Endpoint()) {
		baseline = bl.Verified
	}

	remote, err := WalkTree(ctx, c, o.ProjectID, o.Concurrency)
	if err != nil {
		return rep, err
	}

	remote, local, err := survey(o.Dir, remote)
	if err != nil {
		return rep, err
	}

	// Irregular local entries are excluded from both sides of the comparison:
	// dsx cannot trust their content, so a path occupied by one is reported
	// remote-only when the server also holds it, and dropped entirely
	// otherwise — the same "not proof of anything" treatment invariant 4
	// gives them for prune.
	var toDownload []string
	for _, path := range SortedPaths(remote) {
		lf, present := local[path]
		if !present || lf.Irregular {
			rep.RemoteOnly = append(rep.RemoteOnly, path)
			continue
		}
		r := remote[path]
		b := baseline[path]
		proven := b.Etag != "" && r.Etag != "" && b.Etag == r.Etag &&
			b.SHA != "" && b.SHA == lf.SHA

		// The ledger answers the same question for a path dsx itself wrote,
		// and reading only the baseline is why `diff` re-downloaded every
		// tracked path on every run — four of four immediately after the
		// clone that had just written them. A ledger entry says "at etag E
		// these bytes hashed to S": the server still showing E and the disk
		// still hashing to S is the same proof `proven` carries, from the map
		// that has it for the paths the baseline never covers (invariant 17
		// keeps the two apart in the other direction — a real ledger entry
		// always wins over a baseline one).
		//
		// Binary entries are excluded: the marker means dsx did not put those
		// bytes on disk (invariant 23), and the sha beside it is either absent
		// or a record of bytes SENT, so neither states what the file holds now.
		prev := st.Files[path]
		trackedProven := !prev.Binary && prev.Etag != "" && r.Etag != "" && prev.Etag == r.Etag &&
			prev.SHA != "" && prev.SHA == lf.SHA

		if proven || trackedProven {
			rep.Same = append(rep.Same, path)
			continue
		}
		toDownload = append(toDownload, path)
	}

	// --out is refused before any download when its filesystem would fold two
	// differing candidates onto one file (invariant 16: refused before the act
	// it refuses, here the act being the write, not the round trip already
	// spent above to classify).
	if o.Out != "" {
		if err := checkOutCollisions(toDownload, remote, o.Out); err != nil {
			return rep, err
		}
	}

	for _, path := range SortedPaths(local) {
		if local[path].Irregular {
			continue
		}
		if _, onRemote := remote[path]; onRemote {
			continue
		}
		rep.LocalOnly = append(rep.LocalOnly, path)
	}

	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		sem  = make(chan struct{}, max(o.Concurrency, 1))
		errs []error

		prog = newProgress(o.Progress, "diffing", len(toDownload))
	)

	// Kept distinct from dlCtx so a caller-side interrupt can still be told
	// apart from a peer-triggered cancel below (invariant 3), same as Fetch.
	parent := ctx
	dlCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	fail := func(err error) {
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
		cancel()
	}

	for _, path := range toDownload {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-dlCtx.Done():
				return
			}
			defer func() { <-sem }()

			lf := local[path]
			r := remote[path]

			body, _, err := c.ReadFull(dlCtx, o.ProjectID, path)
			if err != nil {
				if isBinaryRefusal(err) {
					// dsx cannot decode a binary's bytes to compare them, so
					// it is never reported "same" on faith — only what was
					// actually verified earns that word.
					mu.Lock()
					rep.Differs = append(rep.Differs, DiffPair{Path: path, LocalSize: lf.Size, RemoteSize: r.Size})
					mu.Unlock()
					prog.step(path)
					return
				}
				fail(err)
				return
			}

			// A decoded length must agree with list_files' size (invariant 1).
			if want := r.Size; int64(len(body)) != want {
				fail(fmt.Errorf(
					"%s: decoded %d bytes, server reports %d — refusing to classify",
					path, len(body), want))
				return
			}

			mu.Lock()
			if SHA256Hex([]byte(body)) == lf.SHA {
				rep.Same = append(rep.Same, path)
			} else {
				rep.Differs = append(rep.Differs, DiffPair{Path: path, LocalSize: lf.Size, RemoteSize: int64(len(body))})
				if o.Out != "" {
					if werr := writeDiffOut(o.Out, path, []byte(body)); werr != nil {
						mu.Unlock()
						fail(werr)
						return
					}
				}
			}
			mu.Unlock()

			prog.step(path)
		}(path)
	}
	wg.Wait()
	prog.clear()

	if len(errs) > 0 {
		return rep, errs[0]
	}
	if err := parent.Err(); err != nil {
		return rep, fmt.Errorf("diff interrupted: %w", err)
	}

	slices.Sort(rep.Same)
	slices.Sort(rep.LocalOnly)
	slices.Sort(rep.RemoteOnly)
	slices.SortFunc(rep.Differs, func(a, b DiffPair) int { return strings.Compare(a.Path, b.Path) })

	return rep, nil
}

// checkOutCollisions is checkPathCollisions' counterpart for --out: it asks
// whether candidates (present-both paths not yet proven same) would fold onto
// one file if written into outDir, without needing outDir's own fold
// detection duplicated — dirFolds already asks the filesystem the same way
// pull does.
func checkOutCollisions(candidates []string, remote map[string]RemoteEntry, outDir string) error {
	if len(candidates) == 0 || !dirFolds(outDir) {
		return nil
	}
	subset := make(map[string]RemoteEntry, len(candidates))
	for _, path := range candidates {
		subset[path] = remote[path]
	}
	collided, err := remoteFoldCollisions(subset, outDir)
	if err != nil {
		return err
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
		"%s cannot hold these paths apart — its filesystem folds names the server keeps "+
			"distinct, so --out would land several files in one and lose all but the last. "+
			"Name a directory on a case-sensitive volume instead", outDir))
}

// writeDiffOut materialises one differing path's remote bytes under outDir.
// path is server-derived, hence untrusted (invariant 7): checkRemotePath and
// safeJoin both run before anything touches disk.
func writeDiffOut(outDir, path string, body []byte) error {
	if err := checkRemotePath(path); err != nil {
		return err
	}
	full, err := safeJoin(outDir, path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return writeAtomic(full, body)
}

func (r DiffReport) Render(asJSON bool) string {
	if asJSON {
		b, _ := json.Marshal(r)
		return string(b)
	}

	type line struct{ path, text string }
	lines := make([]line, 0, len(r.Same)+len(r.LocalOnly)+len(r.RemoteOnly)+len(r.Differs))
	for _, p := range r.Same {
		lines = append(lines, line{p, p + ": same"})
	}
	for _, p := range r.LocalOnly {
		lines = append(lines, line{p, p + ": local-only"})
	}
	for _, p := range r.RemoteOnly {
		lines = append(lines, line{p, p + ": remote-only"})
	}
	for _, d := range r.Differs {
		lines = append(lines, line{d.Path, fmt.Sprintf("%s: differs (local %s, remote %s)",
			d.Path, fmtutil.Bytes(d.LocalSize), fmtutil.Bytes(d.RemoteSize))})
	}
	slices.SortFunc(lines, func(a, b line) int { return strings.Compare(a.path, b.path) })

	var sb strings.Builder
	if r.Incomplete {
		sb.WriteString("incomplete\n")
	}
	for i, l := range lines {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(l.text)
	}
	if len(lines) == 0 && !r.Incomplete {
		sb.WriteString("nothing to compare")
	}
	return sb.String()
}
