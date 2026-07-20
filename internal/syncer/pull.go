package syncer

import (
	"context"
	"encoding/json"
	"errors"
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

type PullOpts struct {
	ProjectID   string
	Dir         string
	Concurrency int
	Prune       bool
	Force       bool
	DryRun      bool

	// Where the transfer counter draws, nil for silence. The caller decides;
	// syncer never asks the terminal itself.
	Progress io.Writer
}

type PullReport struct {
	Fetched   []string `json:"fetched"`
	Unchanged int      `json:"unchanged"`
	Deleted   []string `json:"deleted"`

	Conflicts []string `json:"conflicts"`

	PruneConflicts []string `json:"prune_conflicts,omitempty"`

	// Gone from the server but not prunable at any force level.
	PruneBinary []string `json:"prune_binary,omitempty"`

	Irregular []string `json:"irregular,omitempty"`
	Binary    []string `json:"binary"`
	Bytes     int64    `json:"bytes"`

	// Set by the caller when the run ended in an error. The report goes to
	// stdout and the error to stderr, so a redirected stdout otherwise keeps
	// only the reassuring half. omitempty keeps success bytes unchanged.
	Incomplete bool `json:"incomplete,omitempty"`
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
			StatePath(o.Dir), st.ProjectID, o.ProjectID)}
	}
	if st.Endpoint != "" && !sameEndpoint(st.Endpoint, c.Endpoint()) {
		return rep, endpointRefusal(o.Dir, st.Endpoint, c.Endpoint(), "pull")
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
		// DryRun: the plan is the requested outcome (invariant 12).
		rep.Deleted = d.Delete
		for _, path := range d.Fetch {
			rep.Fetched = append(rep.Fetched, path)
			rep.Bytes += remote[path].Size
		}
		return rep, nil
	}

	// First contact into a directory that already disagrees: write nothing.
	// With an empty ledger no path here was ever asked for, so writing the
	// non-conflicting ones leaves a half-foreign tree the caller never agreed
	// to and cannot tell from their own work. An established sync keeps the
	// opposite behaviour on purpose — there, a conflict on one path must not
	// stop the others, which is what makes conflicts recoverable one file at a
	// time. Nothing is written and nothing is saved, so the report carries the
	// conflicts and Outcome still supplies the exit code.
	if len(st.Files) == 0 && len(rep.Conflicts) > 0 {
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

		prog = newProgress(o.Progress, "pulling", len(d.Fetch))
	)

	// Kept distinct from fetchCtx so a caller-side interrupt (parent.Err())
	// can still be told apart from a peer-triggered cancel below (invariant 3).
	parent := ctx
	fetchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Record under the lock, then cancel. The append-then-cancel ordering is
	// load-bearing (invariant 3).
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

			// A decoded length must agree with list_files' size (invariant 1).
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
			if err := writeAtomic(full, []byte(body)); err != nil {
				fail(err)
				return
			}

			mu.Lock()
			rep.Fetched = append(rep.Fetched, path)
			rep.Bytes += int64(len(body))
			st = st.withFile(path, FileState{Etag: etag, Size: int64(len(body)), SHA: SHA256Hex([]byte(body))})
			mu.Unlock()

			prog.step(path)
		}(path)
	}
	wg.Wait()
	prog.clear()

	// Sorted above the error returns below, which are a machine surface too.
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
		delete(st.Files, path)
		rep.Deleted = append(rep.Deleted, path)
	}

	// Above the error returns: --json is a machine surface on the failure paths too.
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
	if r.Incomplete {
		sb.WriteString("incomplete: ")
	}
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
	// Last, and only for the plain rung: cat cannot fetch a binary, and a path
	// gone from the server has no copy to fetch. The line makes no claim about
	// --force — Conflicts is merged, and on the destructive rungs --force deletes.
	if len(r.Conflicts)-len(r.PruneConflicts)-len(r.PruneBinary) > 0 {
		sb.WriteString("\n" + conflictHint)
	}
	return sb.String()
}
