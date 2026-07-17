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
		return rep, fmt.Errorf("%s is bound to project %s; refusing to pull %s into it",
			filepath.Join(o.Dir, StateFileName), st.ProjectID, o.ProjectID)
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
	slices.Sort(rep.Conflicts)
	rep.PruneConflicts = d.PruneConflicts
	rep.Irregular = d.Irregular
	rep.Deleted = d.Delete

	if o.DryRun {
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

	parent := ctx
	fetchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

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
				mu.Lock()
				if isBinaryRefusal(err) {
					rep.Binary = append(rep.Binary, path)

					st = st.withFile(path, FileState{Etag: remote[path].Etag, Binary: true})
				} else {
					errs = append(errs, err)
					cancel()
				}
				mu.Unlock()
				return
			}

			// A decoded length that disagrees with list_files' size means a
			// corrupt decode; refuse rather than land a bad file on disk.
			if want := remote[path].Size; int64(len(body)) != want {
				mu.Lock()
				errs = append(errs, fmt.Errorf(
					"%s: decoded %d bytes, server reports %d — refusing to write",
					path, len(body), want))
				mu.Unlock()
				cancel()
				return
			}

			full, err := safeJoin(o.Dir, path)
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				cancel()
				return
			}
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				cancel()
				return
			}
			if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				cancel()
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
	for _, path := range rep.Deleted {
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
	}

	saveErr := st.save(o.Dir)
	if pruneErr != nil {
		return rep, pruneErr
	}
	if saveErr != nil {
		return rep, saveErr
	}

	slices.Sort(rep.Fetched)
	slices.Sort(rep.Deleted)
	slices.Sort(rep.Conflicts)
	slices.Sort(rep.PruneConflicts)
	slices.Sort(rep.Irregular)
	slices.Sort(rep.Binary)
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
