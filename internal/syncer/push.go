package syncer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/fmtutil"
	"github.com/somework/dsx/internal/mcp"
)

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

type PushOpts struct {
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

type PushReport struct {
	Written   []string `json:"written"`
	Unchanged int      `json:"unchanged"`
	Deleted   []string `json:"deleted"`

	// A proven byte match (invariant 17): a determination, not an act, so it
	// is filled from the plan on every path, DryRun included.
	Verified int `json:"verified"`

	Conflicts []string `json:"conflicts"`

	// Untracked and never proven equal to the server (invariant 17): a
	// subset of Conflicts, disjoint from BinaryConflicts/BinaryGone/
	// PruneConflicts. `dsx fetch` is the only thing that can ever clear one.
	Unverified []string `json:"unverified,omitempty"`

	BinaryConflicts []string `json:"binary_conflicts,omitempty"`

	BinaryGone []string `json:"binary_gone,omitempty"`

	PruneConflicts []string `json:"prune_conflicts,omitempty"`

	Irregular []string `json:"irregular,omitempty"`
	Bytes     int64    `json:"bytes"`

	// See PullReport.Incomplete.
	Incomplete bool `json:"incomplete,omitempty"`
}

func Push(ctx context.Context, c *mcp.Client, o PushOpts) (PushReport, error) {
	var rep PushReport

	st, err := LoadState(o.Dir)
	if err != nil {
		return rep, err
	}
	if st.ProjectID != "" && st.ProjectID != o.ProjectID {
		return rep, &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
			"%s is bound to project %s; refusing to push it to %s",
			StatePath(o.Dir), st.ProjectID, o.ProjectID)}
	}
	if st.Endpoint != "" && !sameEndpoint(st.Endpoint, c.Endpoint()) {
		return rep, endpointRefusal(o.Dir, st.Endpoint, c.Endpoint(), "push")
	}
	if err := checkLedgerHome(o.Dir); err != nil {
		return rep, err
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

	d := planPush(remote, local, st, baseline, o.Force, o.Prune)
	rep.Unchanged = d.Unchanged
	rep.Verified = d.Verified
	rep.Conflicts = append(append([]string(nil), d.Conflicts...), d.BinaryConflicts...)
	rep.Conflicts = append(rep.Conflicts, d.PruneConflicts...)
	rep.Conflicts = append(rep.Conflicts, d.BinaryGone...)
	rep.Conflicts = append(rep.Conflicts, d.Unverified...)
	slices.Sort(rep.Conflicts)
	rep.Unverified = d.Unverified
	rep.BinaryConflicts = d.BinaryConflicts
	rep.BinaryGone = d.BinaryGone
	rep.PruneConflicts = d.PruneConflicts
	rep.Irregular = d.Irregular

	// Deleted and Bytes name acts: bound to locals here, assigned to rep only
	// where the act has happened, or in the DryRun branch (invariant 12).
	toDelete := d.Delete
	var plannedBytes int64

	specs := make([]writeSpec, 0, len(d.Write))
	for _, cand := range d.Write {
		full, err := safeJoin(o.Dir, cand.Path)
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
		plannedBytes += int64(len(body))
	}

	if o.DryRun {
		for _, s := range specs {
			rep.Written = append(rep.Written, s.Path)
		}
		rep.Deleted = toDelete
		rep.Bytes = plannedBytes
		return rep, nil
	}

	st.ProjectID = o.ProjectID
	st.Endpoint = c.Endpoint()

	prog := newProgress(o.Progress, "pushing", len(specs))

	// Save the ledger whenever bytes moved, error paths included (invariant 5).
	for _, batch := range batches(specs) {
		if err := writeBatch(ctx, c, o.ProjectID, batch, &st, &rep); err != nil {
			prog.clear()
			_ = st.save(o.Dir)
			rep.addConflicts(err)
			return rep, err
		}
		for _, s := range batch {
			prog.step(s.Path)
		}
	}
	prog.clear()

	// A cancel landing after the last call returns leaves every loop above with
	// a nil error (invariant 3). The batch loop is sequential and derives no
	// context, so ctx is already the parent. Checked before the deletes so
	// --prune cannot run against a tree the user already cancelled.
	if err := ctx.Err(); err != nil {
		_ = st.save(o.Dir)
		return rep, fmt.Errorf("push interrupted after %d of %d files; the rest were not sent: %w",
			len(rep.Written), len(specs), err)
	}

	if len(toDelete) > 0 {
		if err := deletePaths(ctx, c, o.ProjectID, toDelete, st); err != nil {
			_ = st.save(o.Dir)
			rep.addPruneConflicts(err)
			return rep, err
		}
		rep.Deleted = toDelete
		for _, p := range toDelete {
			delete(st.Files, p)
		}
	}

	// Again after the deletes.
	if err := ctx.Err(); err != nil {
		_ = st.save(o.Dir)
		return rep, fmt.Errorf("push interrupted after %d of %d files: %w",
			len(rep.Written), len(specs), err)
	}

	if err := st.save(o.Dir); err != nil {
		return rep, err
	}
	return rep, nil
}

// addConflicts folds a server-side conflict's paths into the report.
func (r *PushReport) addConflicts(err error) {
	e := dsxerr.Classify(err)
	if e == nil || e.Kind != dsxerr.KindConflict {
		return
	}
	for _, p := range e.Paths {
		if !slices.Contains(r.Conflicts, p) {
			r.Conflicts = append(r.Conflicts, p)
		}
	}
	slices.Sort(r.Conflicts)
}

// addPruneConflicts is addConflicts for the delete lane; it also records the
// paths in PruneConflicts, which Render and Outcome word differently.
func (r *PushReport) addPruneConflicts(err error) {
	r.addConflicts(err)
	e := dsxerr.Classify(err)
	if e == nil || e.Kind != dsxerr.KindConflict {
		return
	}
	for _, p := range e.Paths {
		if !slices.Contains(r.PruneConflicts, p) {
			r.PruneConflicts = append(r.PruneConflicts, p)
		}
	}
	slices.Sort(r.PruneConflicts)
}

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

type writeResult struct {
	Etags   map[string]string `json:"etags"`
	Written int               `json:"written"`
	URL     string            `json:"url"`
}

func writeBatch(ctx context.Context, c *mcp.Client, projectID string, batch []writeSpec, st *State, rep *PushReport) error {
	paths := make([]string, 0, len(batch))
	for _, s := range batch {
		paths = append(paths, s.Path)
	}

	args := map[string]any{"project_id": projectID, "files": batch}
	text, err := CallWithGrant(ctx, c, "write_files", args, projectID, paths)
	if err != nil {
		if paths, ok := mcp.ConflictFromToolError(err); ok {
			return dsxerr.Conflict(paths, "the server changed while dsx was writing; nothing was written — `dsx pull` first, or --force")
		}
		return err
	}

	var res writeResult
	if jsonErr := json.Unmarshal([]byte(text), &res); jsonErr != nil || len(res.Etags) == 0 {
		slices.Sort(paths)
		return &dsxerr.Error{Kind: dsxerr.KindProtocol, Paths: paths, Msg: fmt.Sprintf(
			"write reply was unrecognised, so etags were not recorded; "+
				"run `dsx pull` to resynchronise. reply: %s", fmtutil.Truncate(text, 300))}
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
		// An empty etag is not a usable ledger key; skipped here, it falls into
		// the unacknowledged set below.
		if etag == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(spec.Data)
		if err != nil {
			return err
		}
		// withFile REPLACES the entry wholesale rather than merging fields, so
		// the Binary marker has to be carried across by hand.
		*st = st.withFile(path, FileState{
			Etag: etag, Size: int64(len(raw)), SHA: SHA256Hex(raw),
			Binary: st.Files[path].Binary,
		})
		rep.Written = append(rep.Written, path)
		rep.Bytes += int64(len(raw))
	}
	slices.Sort(rep.Written)

	var unacknowledged []string
	for _, s := range batch {
		if et, ok := res.Etags[s.Path]; !ok || et == "" {
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

func deletePaths(ctx context.Context, c *mcp.Client, projectID string, paths []string, st State) error {
	token, err := PlanToken(ctx, c, map[string]any{"project_id": projectID, "deletes": paths})
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
	_, err = c.CallTool(ctx, "delete_files", map[string]any{
		"project_id": projectID,
		"plan_token": token,
		"files":      files,
	})
	if err != nil {
		if paths, ok := mcp.ConflictFromToolError(err); ok {
			// Do not say "pull first": on a --force rerun this path is deleted,
			// so --force DELETES the server's newer copy (invariant 4).
			return dsxerr.Conflict(paths, "the server moved ahead of a path dsx was pruning; nothing was deleted — --force would DELETE the server's newer copy")
		}
		return err
	}
	return nil
}

func (r PushReport) Render(asJSON bool) string {
	if asJSON {
		b, _ := json.Marshal(r)
		return string(b)
	}
	var sb strings.Builder
	if r.Incomplete {
		sb.WriteString("incomplete: ")
	}
	fmt.Fprintf(&sb, "pushed %d, unchanged %d", len(r.Written), r.Unchanged)
	if len(r.Deleted) > 0 {
		fmt.Fprintf(&sb, ", deleted %d", len(r.Deleted))
	}
	if r.Verified > 0 {
		fmt.Fprintf(&sb, ", verified %d", r.Verified)
	}
	if len(r.Conflicts) > 0 {
		fmt.Fprintf(&sb, ", conflicts %d", len(r.Conflicts))
	}
	fmt.Fprintf(&sb, " (%s)", fmtutil.Bytes(r.Bytes))
	for _, p := range r.Conflicts {
		if slices.Contains(r.BinaryConflicts, p) {
			fmt.Fprintf(&sb, "\n  ! %s — dsx cannot read the server's copy, so it cannot merge; "+
				"--force overwrites it and the only copy is gone", p)
			continue
		}
		if slices.Contains(r.BinaryGone, p) {
			fmt.Fprintf(&sb, "\n  ! %s — gone from the server; dsx did not re-create it "+
				"— delete it here, or --force to re-upload it", p)
			continue
		}
		if slices.Contains(r.PruneConflicts, p) {
			fmt.Fprintf(&sb, "\n  ! %s — deleted here but moved ahead on the server; "+
				"--force would DELETE the server's newer copy", p)
			continue
		}
		if slices.Contains(r.Unverified, p) {
			fmt.Fprintf(&sb, "\n  ! %s — never verified against the server; "+
				"`dsx fetch` checks without writing, or --force to overwrite the server's copy", p)
			continue
		}
		fmt.Fprintf(&sb, "\n  ! %s — server moved ahead; `dsx pull` first, or --force", p)
	}
	for _, p := range r.Irregular {
		fmt.Fprintf(&sb, "\n  ~ %s — not a regular file here; dsx sent nothing and left the server's copy alone", p)
	}
	// See the pull half: last, only the plain rung, no claim about --force.
	if len(r.Conflicts)-len(r.BinaryConflicts)-len(r.BinaryGone)-len(r.PruneConflicts) > 0 {
		sb.WriteString("\n" + conflictHint)
	}
	return sb.String()
}
