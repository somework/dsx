package syncer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
}

type PushReport struct {
	Written   []string `json:"written"`
	Unchanged int      `json:"unchanged"`
	Deleted   []string `json:"deleted"`

	Conflicts []string `json:"conflicts"`

	BinaryConflicts []string `json:"binary_conflicts,omitempty"`

	PruneConflicts []string `json:"prune_conflicts,omitempty"`

	Irregular []string `json:"irregular,omitempty"`
	Bytes     int64    `json:"bytes"`
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
			StateFileName, st.ProjectID, o.ProjectID)}
	}

	remote, err := WalkTree(ctx, c, o.ProjectID, o.Concurrency)
	if err != nil {
		return rep, err
	}

	remote, local, err := survey(o.Dir, remote)
	if err != nil {
		return rep, err
	}

	d := planPush(remote, local, st, o.Force, o.Prune)
	rep.Unchanged = d.Unchanged
	rep.Conflicts = append(append([]string(nil), d.Conflicts...), d.BinaryConflicts...)
	rep.Conflicts = append(rep.Conflicts, d.PruneConflicts...)
	slices.Sort(rep.Conflicts)
	rep.BinaryConflicts = d.BinaryConflicts
	rep.PruneConflicts = d.PruneConflicts
	rep.Irregular = d.Irregular
	rep.Deleted = d.Delete

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
		rep.Bytes += int64(len(body))
	}

	if o.DryRun {
		for _, s := range specs {
			rep.Written = append(rep.Written, s.Path)
		}
		return rep, nil
	}

	st.ProjectID = o.ProjectID
	st.Endpoint = c.Endpoint()

	// Save the ledger whenever bytes moved, error paths included: a file on disk
	// with no ledger entry becomes a conflict next run, pushing the user to --force.
	for _, batch := range batches(specs) {
		if err := writeBatch(ctx, c, o.ProjectID, batch, &st, &rep); err != nil {
			_ = st.save(o.Dir)
			return rep, err
		}
	}

	if len(rep.Deleted) > 0 {
		if err := deletePaths(ctx, c, o.ProjectID, rep.Deleted, st); err != nil {
			_ = st.save(o.Dir)
			return rep, err
		}
		for _, p := range rep.Deleted {
			delete(st.Files, p)
		}
	}

	if err := st.save(o.Dir); err != nil {
		return rep, err
	}
	return rep, nil
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
		// Symmetry with ParseEnvelope, which refuses a read whose etag is empty:
		// an empty etag is not a usable ledger key — recorded, it reads as
		// unchanged (empty-vs-empty) next run. Skip it here and let it fall into
		// the unacknowledged set below so the caller resynchronises with a pull.
		if etag == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(spec.Data)
		if err != nil {
			return err
		}
		*st = st.withFile(path, FileState{Etag: etag, Size: int64(len(raw)), SHA: SHA256Hex(raw)})
		rep.Written = append(rep.Written, path)
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
			// A prune-delete refusal means the server moved this path ahead of the
			// ledger. On a --force rerun planPush routes it to Delete, so --force
			// DELETES the server's newer copy — do not say "pull first" (invariant
			// 4). Wording mirrors Outcome()/Render for the same prune-conflict.
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
			fmt.Fprintf(&sb, "\n  ! %s — dsx cannot read the server's copy, so it cannot merge; "+
				"--force overwrites it and the only copy is gone", p)
			continue
		}
		if slices.Contains(r.PruneConflicts, p) {
			fmt.Fprintf(&sb, "\n  ! %s — deleted here but moved ahead on the server; "+
				"--force would DELETE the server's newer copy", p)
			continue
		}
		fmt.Fprintf(&sb, "\n  ! %s — server moved ahead; `dsx pull` first, or --force", p)
	}
	for _, p := range r.Irregular {
		fmt.Fprintf(&sb, "\n  ~ %s — not a regular file here; dsx sent nothing and left the server's copy alone", p)
	}
	return sb.String()
}
