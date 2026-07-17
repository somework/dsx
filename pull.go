package main

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
)

// readFull retrieves a complete file, walking windows when the server's
// 256 KiB per-read cap truncates it.
func (c *client) readFull(ctx context.Context, projectID, path string) (body string, etag string, err error) {
	var (
		sb     strings.Builder
		offset = 0
	)
	for {
		args := map[string]any{"project_id": projectID, "path": path}
		if offset > 0 {
			args["offset"] = offset
		}
		text, err := c.callTool(ctx, "read_file", args)
		if err != nil {
			return "", "", err
		}
		env, err := parseEnvelope(text)
		if err != nil {
			return "", "", fmt.Errorf("%s: %w", path, err)
		}
		if env.Truncated {
			return "", "", fmt.Errorf("%s: a single line exceeds the 256 KiB read cap; the server cannot return it whole", path)
		}
		if etag != "" && env.Etag != etag {
			return "", "", fmt.Errorf("%s: changed on the server mid-read (etag %s -> %s); retry", path, etag, env.Etag)
		}
		etag = env.Etag
		sb.WriteString(env.Body)

		if env.Complete() {
			return sb.String(), etag, nil
		}
		if env.Lines[1] <= offset-1 {
			return "", "", fmt.Errorf("%s: read made no progress at offset %d", path, offset)
		}
		offset = env.Lines[1] + 1
	}
}

type pullOpts struct {
	projectID   string
	dir         string
	concurrency int
	prune       bool
	force       bool
	dryRun      bool
}

type pullReport struct {
	Fetched   []string `json:"fetched"`
	Unchanged int      `json:"unchanged"`
	Deleted   []string `json:"deleted"`
	// Conflicts names EVERY path a human must look at, prune conflicts included.
	// It has always meant that, and narrowing it to "overwrite conflicts only"
	// made `status --json` report zero for the one case where --force destroys
	// the only copy.
	Conflicts []string `json:"conflicts"`
	// PruneConflicts is the subset of Conflicts that --force resolves by
	// DELETING rather than overwriting. A discriminator, never a replacement.
	PruneConflicts []string `json:"prune_conflicts,omitempty"`
	// Irregular are paths that are not regular files here. Reported, never
	// conflicts: nothing the caller can type resolves a symlink, so exit 3
	// would be a demand with no answer.
	Irregular []string `json:"irregular,omitempty"`
	Binary    []string `json:"binary"`
	Bytes     int64    `json:"bytes"`
}

// isBinaryRefusal reports the server's refusal to return a binary file.
//
// read_file only serves text. Binary blobs are stored base64 and there is no
// way to read them back: the server advertises no `resources` capability and
// read_file takes no encoding parameter. write_files does accept them, so the
// asymmetry is the service's, not ours -- pull reports them and moves on.
func isBinaryRefusal(err error) bool {
	var te *toolError
	if !errors.As(err, &te) {
		return false
	}
	return strings.Contains(te.Text, "is a binary file")
}

func runPull(ctx context.Context, c *client, o pullOpts) (pullReport, error) {
	var rep pullReport

	st, err := loadState(o.dir)
	if err != nil {
		return rep, err
	}
	if st.ProjectID != "" && st.ProjectID != o.projectID {
		return rep, fmt.Errorf("%s is bound to project %s; refusing to pull %s into it",
			filepath.Join(o.dir, stateFileName), st.ProjectID, o.projectID)
	}

	ig, err := loadIgnore(o.dir)
	if err != nil {
		return rep, err
	}
	remote, err := c.walkTree(ctx, o.projectID, o.concurrency)
	if err != nil {
		return rep, err
	}
	// Both sides are filtered, never just one. An ignored path that stayed in
	// the listing but vanished from the scan is indistinguishable from a local
	// delete, and --prune acts on that difference.
	remote = filterRemote(remote, ig)
	local, err := scanLocal(o.dir, ig)
	if err != nil {
		return rep, err
	}
	// Before anything is fetched: paths the server keeps apart may be one file
	// here -- either two listing entries, or a listing entry and a file already
	// on disk under a folded name. Writing them destroys all but the last, and
	// each write's size assertion passes on its own, so nothing downstream
	// notices.
	if err := checkPathCollisions(remote, local, o.dir); err != nil {
		return rep, err
	}

	d := planPull(remote, local, st, o.force, o.prune)
	rep.Unchanged = d.Unchanged
	rep.Binary = d.Binary
	// Sorted here, at the union site, not at the end of the function: the tail
	// sort sits after the dry-run return, and `status` is always a dry run --
	// so identical state rendered in two different orders depending on the mode,
	// while every sibling field was sorted.
	rep.Conflicts = append(append([]string(nil), d.Conflicts...), d.PruneConflicts...)
	slices.Sort(rep.Conflicts)
	rep.PruneConflicts = d.PruneConflicts
	rep.Irregular = d.Irregular
	rep.Deleted = d.Delete

	if o.dryRun {
		for _, path := range d.Fetch {
			rep.Fetched = append(rep.Fetched, path)
			rep.Bytes += remote[path].Size
		}
		return rep, nil
	}

	// Refuse hostile remote paths before any of them reaches the disk.
	for _, path := range append(append([]string{}, d.Fetch...), d.Delete...) {
		if err := checkRemotePath(path); err != nil {
			return rep, err
		}
	}

	var (
		mu sync.Mutex
		wg sync.WaitGroup
		// Clamped beside the semaphore for the same reason as walkTree's: zero
		// deadlocks the fetch, negative panics make, and neither says so.
		sem  = make(chan struct{}, max(o.concurrency, 1))
		errs []error
	)
	// The caller's context stays separate: the derived one is cancelled by our
	// own error path too, so only the parent can report an interruption.
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

			body, etag, err := c.readFull(fetchCtx, o.projectID, path)
			if err != nil {
				mu.Lock()
				if isBinaryRefusal(err) {
					rep.Binary = append(rep.Binary, path)
					// Remember the refusal against this etag so later syncs
					// stop re-asking. A new etag re-tries it.
					st = st.withFile(path, fileState{Etag: remote[path].Etag, Binary: true})
				} else {
					errs = append(errs, err)
					cancel()
				}
				mu.Unlock()
				return
			}

			// The listing told us the byte size. If the decoded body does not
			// match it, the decode is wrong -- refuse to write a corrupt file.
			if want := remote[path].Size; int64(len(body)) != want {
				mu.Lock()
				errs = append(errs, fmt.Errorf(
					"%s: decoded %d bytes, server reports %d — refusing to write",
					path, len(body), want))
				mu.Unlock()
				cancel()
				return
			}

			full, err := safeJoin(o.dir, path)
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
			st = st.withFile(path, fileState{Etag: etag, Size: int64(len(body)), SHA: sha256hex([]byte(body))})
			mu.Unlock()
		}(path)
	}
	wg.Wait()

	// Files already written are on disk whether or not the run finished. Save
	// the ledger for them, or the next sync sees bytes it has no record of and
	// calls its own work a conflict.
	st.ProjectID = o.projectID
	st.Endpoint = c.endpoint

	if len(errs) > 0 {
		_ = st.save(o.dir)
		return rep, errs[0]
	}
	if err := parent.Err(); err != nil {
		_ = st.save(o.dir)
		return rep, fmt.Errorf("pull interrupted: %w", err)
	}

	// The delete loop must not return before the ledger is saved. Every file
	// this run fetched is already on disk, and a bare return here throws away
	// the etag and sha for all of them -- so the next pull sees bytes it has no
	// record of, calls its own download a conflict, and demands --force for
	// every one. That is invariant 5's stated harm, and it arrived through this
	// loop: one unwritable directory was enough.
	var pruneErr error
	for _, path := range rep.Deleted {
		full, err := safeJoin(o.dir, path)
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

	// The prune failure is the news; a save error after it is a second-order
	// symptom, usually of the same unwritable directory. Returning the save
	// error first hid the prune failure entirely.
	saveErr := st.save(o.dir)
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

func (r pullReport) render(asJSON bool) string {
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
	fmt.Fprintf(&sb, " (%s)", humanBytes(r.Bytes))
	// One line per path, each carrying the advice its class deserves. --force
	// overwrites the first kind (the bytes survive on the server) and DELETES
	// the second (they survive nowhere) -- advertising an overwrite for both is
	// how a user reaching for --force to fix one file loses another.
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
