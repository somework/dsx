package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/fmtutil"
	"github.com/somework/dsx/internal/mcp"
)

type PullOpts struct {
	ProjectID   string
	Dir         string
	Concurrency int
	Prune       bool
	Force       bool
	DryRun      bool
}

type PullReport struct {
	Fetched   []string `json:"fetched"`
	Unchanged int      `json:"unchanged"`
	Deleted   []string `json:"deleted"`

	Conflicts []string `json:"conflicts"`

	PruneConflicts []string `json:"prune_conflicts,omitempty"`

	// Gone from the server but not prunable at any force level. Distinct from
	// PruneConflicts because --force resolves that one by deleting and cannot
	// resolve this one at all.
	PruneBinary []string `json:"prune_binary,omitempty"`

	Irregular []string `json:"irregular,omitempty"`
	Binary    []string `json:"binary"`
	Bytes     int64    `json:"bytes"`
}

func isBinaryRefusal(err error) bool {
	var te *mcp.ToolError
	if !errors.As(err, &te) {
		return false
	}
	return strings.Contains(te.Text, "is a binary file")
}

func Pull(ctx context.Context, c *mcp.Client, o PullOpts) (PullReport, error) {
	var rep PullReport

	st, err := LoadState(o.Dir)
	if err != nil {
		return rep, err
	}
	if st.ProjectID != "" && st.ProjectID != o.ProjectID {
		return rep, &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
			"%s is bound to project %s; refusing to pull %s into it",
			filepath.Join(o.Dir, StateFileName), st.ProjectID, o.ProjectID)}
	}

	remote, err := WalkTree(ctx, c, o.ProjectID, o.Concurrency)
	if err != nil {
		return rep, err
	}

	remote, local, err := survey(o.Dir, remote)
	if err != nil {
		return rep, err
	}

	if err := checkPathCollisions(remote, local, o.Dir); err != nil {
		return rep, err
	}

	d := planPull(remote, local, st, o.Force, o.Prune)
	rep.Unchanged = d.Unchanged
	rep.Binary = d.Binary

	rep.Conflicts = append(append([]string(nil), d.Conflicts...), d.PruneConflicts...)
	rep.Conflicts = append(rep.Conflicts, d.PruneBinary...)
	slices.Sort(rep.Conflicts)
	rep.PruneConflicts = d.PruneConflicts
	rep.PruneBinary = d.PruneBinary
	rep.Irregular = d.Irregular

	if o.DryRun {
		// The one place a plan may legitimately be reported as an outcome:
		// the caller asked for a preview, not for the bytes to move.
		rep.Deleted = d.Delete
		for _, path := range d.Fetch {
			rep.Fetched = append(rep.Fetched, path)
			rep.Bytes += remote[path].Size
		}
		return rep, nil
	}

	for _, path := range append(append([]string{}, d.Fetch...), d.Delete...) {
		if err := checkRemotePath(path); err != nil {
			return rep, err
		}
	}

	var (
		mu sync.Mutex
		wg sync.WaitGroup

		sem  = make(chan struct{}, max(o.Concurrency, 1))
		errs []error
	)

	// Kept distinct from fetchCtx so a caller-side interrupt (parent.Err())
	// can still be told apart from a peer-triggered cancel below (invariant 3).
	parent := ctx
	fetchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// One shape for every failure, mirroring WalkTree: record under the lock,
	// then cancel. Append-then-cancel ordering is load-bearing (invariant 3) —
	// the error is visible before peers observe fetchCtx.Done().
	fail := func(err error) {
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
		cancel()
	}

	for _, path := range d.Fetch {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-fetchCtx.Done():
				return
			}
			defer func() { <-sem }()

			body, etag, err := c.ReadFull(fetchCtx, o.ProjectID, path)
			if err != nil {
				if isBinaryRefusal(err) {
					mu.Lock()
					rep.Binary = append(rep.Binary, path)
					st = st.withFile(path, FileState{Etag: remote[path].Etag, Binary: true})
					mu.Unlock()
					return
				}
				fail(err)
				return
			}

			// A decoded length that disagrees with list_files' size means a
			// corrupt decode; refuse rather than land a bad file on disk.
			if want := remote[path].Size; int64(len(body)) != want {
				fail(fmt.Errorf(
					"%s: decoded %d bytes, server reports %d — refusing to write",
					path, len(body), want))
				return
			}

			full, err := safeJoin(o.Dir, path)
			if err != nil {
				fail(err)
				return
			}
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				fail(err)
				return
			}
			if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
				fail(err)
				return
			}

			mu.Lock()
			rep.Fetched = append(rep.Fetched, path)
			rep.Bytes += int64(len(body))
			st = st.withFile(path, FileState{Etag: etag, Size: int64(len(body)), SHA: SHA256Hex([]byte(body))})
			mu.Unlock()
		}(path)
	}
	wg.Wait()

	// Sorted here, not on the success path alone: the error returns below are
	// a machine surface too, and goroutine-completion order is churn.
	slices.Sort(rep.Fetched)
	slices.Sort(rep.Binary)

	st.ProjectID = o.ProjectID
	st.Endpoint = c.Endpoint()

	if len(errs) > 0 {
		_ = st.save(o.Dir)
		return rep, errs[0]
	}
	if err := parent.Err(); err != nil {
		_ = st.save(o.Dir)
		return rep, fmt.Errorf("pull interrupted: %w", err)
	}

	var pruneErr error
	for _, path := range d.Delete {
		full, err := safeJoin(o.Dir, path)
		if err != nil {
			pruneErr = err
			break
		}
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			pruneErr = err
			break
		}
		// Ledger and report must not disagree about a path, so
		// both record the same removal at the same point.
		delete(st.Files, path)
		rep.Deleted = append(rep.Deleted, path)
	}

	// Above the error returns, like Fetched/Binary: --json is a machine surface
	// on the failure paths too. Inert while d.Delete is built in SortedPaths
	// order and appended in iteration order (a break leaves a sorted prefix) --
	// kept so the guarantee is positional, not a property of the planner.
	slices.Sort(rep.Deleted)

	saveErr := st.save(o.Dir)
	if pruneErr != nil {
		return rep, pruneErr
	}
	if saveErr != nil {
		return rep, saveErr
	}

	slices.Sort(rep.Conflicts)
	slices.Sort(rep.PruneConflicts)
	slices.Sort(rep.PruneBinary)
	slices.Sort(rep.Irregular)
	return rep, nil
}

func (r PullReport) Render(asJSON bool) string {
	if asJSON {
		b, _ := json.Marshal(r)
		return string(b)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "pulled %d, unchanged %d", len(r.Fetched), r.Unchanged)
	if len(r.Deleted) > 0 {
		fmt.Fprintf(&sb, ", deleted %d", len(r.Deleted))
	}
	if len(r.Conflicts) > 0 {
		fmt.Fprintf(&sb, ", conflicts %d", len(r.Conflicts))
	}
	if len(r.Binary) > 0 {
		fmt.Fprintf(&sb, ", binary %d", len(r.Binary))
	}
	fmt.Fprintf(&sb, " (%s)", fmtutil.Bytes(r.Bytes))

	for _, p := range r.Conflicts {
		if slices.Contains(r.PruneBinary, p) {
			fmt.Fprintf(&sb, "\n  ! %s — gone from the server; dsx cannot re-fetch it (binary), so it was kept — not even --force will prune it; delete it yourself if you meant to", p)
			continue
		}
		if slices.Contains(r.PruneConflicts, p) {
			fmt.Fprintf(&sb, "\n  ! %s — gone from the server, edited here; --force would DELETE your only copy", p)
			continue
		}
		fmt.Fprintf(&sb, "\n  ! %s — local differs; --force to overwrite", p)
	}
	for _, p := range r.Irregular {
		fmt.Fprintf(&sb, "\n  ~ %s — not a regular file here; dsx left it alone", p)
	}
	if len(r.Binary) > 0 {
		fmt.Fprintf(&sb, "\n  ~ %d binary file(s) skipped — read_file serves text only: %s",
			len(r.Binary), strings.Join(r.Binary, ", "))
	}
	return sb.String()
}
