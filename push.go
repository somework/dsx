package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/somework/dsx/internal/fmtutil"
	"os"
	"slices"
	"strings"

	"github.com/somework/dsx/internal/dsxerr"
)

// Batch limits. The server accepts up to 256 entries per call; the byte cap is
// ours, to keep a single request from ballooning once base64 inflates it by 4/3.
const (
	maxBatchFiles = 128
	maxBatchBytes = 3 << 20
)

type writeSpec struct {
	Path     string `json:"path"`
	Data     string `json:"data"`
	Encoding string `json:"encoding"`
	IfMatch  string `json:"if_match,omitempty"`
}

type pushOpts struct {
	projectID   string
	dir         string
	concurrency int
	prune       bool
	force       bool
	dryRun      bool
}

type pushReport struct {
	Written   []string `json:"written"`
	Unchanged int      `json:"unchanged"`
	Deleted   []string `json:"deleted"`
	// Conflicts names EVERY path a human must look at, binary ones included.
	Conflicts []string `json:"conflicts"`
	// BinaryConflicts is the subset only --force resolves, unrecoverably.
	BinaryConflicts []string `json:"binary_conflicts,omitempty"`
	// Irregular are paths that are not regular files here, so there were no
	// bytes to send. Reported, never conflicts -- see pullReport.Irregular.
	Irregular []string `json:"irregular,omitempty"`
	Bytes     int64    `json:"bytes"`
}

func runPush(ctx context.Context, c *client, o pushOpts) (pushReport, error) {
	var rep pushReport

	st, err := loadState(o.dir)
	if err != nil {
		return rep, err
	}
	if st.ProjectID != "" && st.ProjectID != o.projectID {
		return rep, fmt.Errorf("%s is bound to project %s; refusing to push it to %s",
			stateFileName, st.ProjectID, o.projectID)
	}

	remote, err := c.walkTree(ctx, o.projectID, o.concurrency)
	if err != nil {
		return rep, err
	}
	// survey filters the listing too, which is what stops --prune from reading
	// "ignored here" as "deleted here" and removing the file from the server.
	remote, local, err := survey(o.dir, remote)
	if err != nil {
		return rep, err
	}

	d := planPush(remote, local, st, o.force, o.prune)
	rep.Unchanged = d.Unchanged
	rep.Conflicts = append(append([]string(nil), d.Conflicts...), d.BinaryConflicts...)
	slices.Sort(rep.Conflicts)
	rep.BinaryConflicts = d.BinaryConflicts
	rep.Irregular = d.Irregular
	rep.Deleted = d.Delete

	specs := make([]writeSpec, 0, len(d.Write))
	for _, cand := range d.Write {
		full, err := safeJoin(o.dir, cand.Path)
		if err != nil {
			return rep, err
		}
		body, err := os.ReadFile(full)
		if err != nil {
			return rep, err
		}
		specs = append(specs, writeSpec{
			Path:     cand.Path,
			Data:     base64.StdEncoding.EncodeToString(body),
			Encoding: "base64",
			IfMatch:  cand.IfMatch,
		})
		rep.Bytes += int64(len(body))
	}

	if o.dryRun {
		for _, s := range specs {
			rep.Written = append(rep.Written, s.Path)
		}
		return rep, nil
	}

	// Pin the directory before the first write. An error path that saves the
	// ledger without this leaves project_id empty, and an empty pin is no pin:
	// the guards above short-circuit on it and the next sync could target a
	// different project against this project's etags.
	st.ProjectID = o.projectID
	st.Endpoint = c.endpoint

	for _, batch := range batches(specs) {
		if err := c.writeBatch(ctx, o.projectID, batch, &st, &rep); err != nil {
			_ = st.save(o.dir) // keep whatever succeeded
			return rep, err
		}
	}

	if len(rep.Deleted) > 0 {
		if err := c.deletePaths(ctx, o.projectID, rep.Deleted, st); err != nil {
			_ = st.save(o.dir)
			return rep, err
		}
		for _, p := range rep.Deleted {
			delete(st.Files, p)
		}
	}

	if err := st.save(o.dir); err != nil {
		return rep, err
	}
	return rep, nil
}

// batches splits specs into requests bounded by both count and payload size.
func batches(specs []writeSpec) [][]writeSpec {
	var (
		out   [][]writeSpec
		cur   []writeSpec
		bytes int
	)
	for _, s := range specs {
		if len(cur) > 0 && (len(cur) >= maxBatchFiles || bytes+len(s.Data) > maxBatchBytes) {
			out = append(out, cur)
			cur, bytes = nil, 0
		}
		cur = append(cur, s)
		bytes += len(s.Data)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// writeResult is what write_files replies with: a path -> etag map, not a list.
type writeResult struct {
	Etags   map[string]string `json:"etags"`
	Written int               `json:"written"`
	URL     string            `json:"url"`
}

func (c *client) writeBatch(ctx context.Context, projectID string, batch []writeSpec, st *syncState, rep *pushReport) error {
	files := make([]map[string]any, 0, len(batch))
	paths := make([]string, 0, len(batch))
	for _, s := range batch {
		m := map[string]any{"path": s.Path, "data": s.Data, "encoding": s.Encoding}
		if s.IfMatch != "" {
			m["if_match"] = s.IfMatch
		}
		files = append(files, m)
		paths = append(paths, s.Path)
	}

	args := map[string]any{"project_id": projectID, "files": files}
	text, err := c.callWithGrant(ctx, "write_files", args, projectID, paths)
	if err != nil {
		// A stale if_match is a conflict, and the server is the only party that
		// can see this one: it lost the race between our listing and our write.
		// Reporting it as a generic failure would send exit 1 to a caller
		// watching for exit 3.
		if paths, ok := conflictFromToolError(err); ok {
			return dsxerr.Conflict(paths, "the server changed while dsx was writing; nothing was written — `dsx pull` first, or --force")
		}
		return err
	}

	var res writeResult
	if jsonErr := json.Unmarshal([]byte(text), &res); jsonErr != nil || len(res.Etags) == 0 {
		// The bytes may be up but the ledger is not. Say so rather than
		// recording an etag we never saw.
		return fmt.Errorf("write reply was unrecognised, so etags were not recorded; "+
			"run `dsx pull` to resynchronise. reply: %s", fmtutil.Truncate(text, 300))
	}

	byPath := make(map[string]writeSpec, len(batch))
	for _, s := range batch {
		byPath[s.Path] = s
	}
	for path, etag := range res.Etags {
		spec, ok := byPath[path]
		if !ok {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(spec.Data)
		if err != nil {
			return err
		}
		*st = st.withFile(path, fileState{Etag: etag, Size: int64(len(raw)), SHA: sha256hex(raw)})
		rep.Written = append(rep.Written, path)
	}
	slices.Sort(rep.Written)

	// A reply naming only some of the paths is not a smaller success. The
	// unacknowledged ones may well be on the server, and we have no etag for
	// them -- so they are absent from the ledger, and the next pull sees bytes
	// it has no record of and calls its own work a conflict. That pushes the
	// user toward --force, which is the spiral invariant 5 exists to prevent.
	// The caller saves the ledger for whatever did land, then reports this.
	var unacknowledged []string
	for _, s := range batch {
		if _, ok := res.Etags[s.Path]; !ok {
			unacknowledged = append(unacknowledged, s.Path)
		}
	}
	if len(unacknowledged) > 0 {
		slices.Sort(unacknowledged)
		return &dsxerr.Error{Kind: dsxerr.KindProtocol, Paths: unacknowledged, Msg: fmt.Sprintf(
			"write_files returned no etag for %d of %d paths, so they are not in the ledger even though "+
				"the server may hold them; run `dsx pull` to resynchronise",
			len(unacknowledged), len(batch))}
	}
	return nil
}

// deletePaths removes remote files. Deletes always require a path-scoped
// plan_token; a project-scoped one is refused by the server.
func (c *client) deletePaths(ctx context.Context, projectID string, paths []string, st syncState) error {
	token, err := planToken(ctx, c, map[string]any{"project_id": projectID, "deletes": paths})
	if err != nil {
		return err
	}

	files := make([]map[string]any, 0, len(paths))
	for _, p := range paths {
		m := map[string]any{"path": p}
		if fst, ok := st.Files[p]; ok && fst.Etag != "" {
			m["if_match"] = fst.Etag
		}
		files = append(files, m)
	}
	_, err = c.callTool(ctx, "delete_files", map[string]any{
		"project_id": projectID,
		"plan_token": token,
		"files":      files,
	})
	return err
}

func (r pushReport) render(asJSON bool) string {
	if asJSON {
		b, _ := json.Marshal(r)
		return string(b)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "pushed %d, unchanged %d", len(r.Written), r.Unchanged)
	if len(r.Deleted) > 0 {
		fmt.Fprintf(&sb, ", deleted %d", len(r.Deleted))
	}
	if len(r.Conflicts) > 0 {
		fmt.Fprintf(&sb, ", conflicts %d", len(r.Conflicts))
	}
	fmt.Fprintf(&sb, " (%s)", fmtutil.Bytes(r.Bytes))
	for _, p := range r.Conflicts {
		if slices.Contains(r.BinaryConflicts, p) {
			// Not "server moved ahead": for these it usually has not. And not
			// "`dsx pull` first": pull cannot fetch what read_file will not
			// serve, so that advice is an infinite loop.
			fmt.Fprintf(&sb, "\n  ! %s — dsx cannot read the server's copy, so it cannot merge; "+
				"--force overwrites it and the only copy is gone", p)
			continue
		}
		fmt.Fprintf(&sb, "\n  ! %s — server moved ahead; `dsx pull` first, or --force", p)
	}
	for _, p := range r.Irregular {
		fmt.Fprintf(&sb, "\n  ~ %s — not a regular file here; dsx sent nothing and left the server's copy alone", p)
	}
	return sb.String()
}
