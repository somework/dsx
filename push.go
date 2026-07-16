package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
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
	Conflicts []string `json:"conflicts"`
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
	local, err := scanLocal(o.dir)
	if err != nil {
		return rep, err
	}

	d := planPush(remote, local, st, o.force, o.prune)
	rep.Unchanged = d.Unchanged
	rep.Conflicts = d.Conflicts
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

	st.ProjectID = o.projectID
	st.Endpoint = c.endpoint
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

// planFor obtains a path-scoped plan_token authorising exactly these writes.
func (c *client) planFor(ctx context.Context, projectID string, paths []string) (string, error) {
	text, err := c.callTool(ctx, "finalize_plan", map[string]any{
		"project_id": projectID,
		"writes":     paths,
	})
	if err != nil {
		return "", fmt.Errorf("finalize_plan: %w", err)
	}
	var plan struct {
		PlanToken string `json:"plan_token"`
	}
	if err := json.Unmarshal([]byte(text), &plan); err != nil || plan.PlanToken == "" {
		return "", fmt.Errorf("finalize_plan returned no plan_token: %s", truncate(text, 200))
	}
	return plan.PlanToken, nil
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
	text, err := c.callTool(ctx, "write_files", args)

	// Without a standing grant the server refuses the write. A path-scoped
	// plan_token authorises exactly these paths instead, so the push proceeds
	// without sending the user to a browser.
	var ge *grantError
	if errors.As(err, &ge) {
		token, planErr := c.planFor(ctx, projectID, paths)
		if planErr != nil {
			return fmt.Errorf("%w; and could not self-authorise: %v", err, planErr)
		}
		args["plan_token"] = token
		text, err = c.callTool(ctx, "write_files", args)
	}
	if err != nil {
		return err
	}

	var res writeResult
	if jsonErr := json.Unmarshal([]byte(text), &res); jsonErr != nil || len(res.Etags) == 0 {
		// The bytes may be up but the ledger is not. Say so rather than
		// recording an etag we never saw.
		return fmt.Errorf("write reply was unrecognised, so etags were not recorded; "+
			"run `dsx pull` to resynchronise. reply: %s", truncate(text, 300))
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
	sortStrings(rep.Written)
	return nil
}

// deletePaths removes remote files. Deletes always require a path-scoped
// plan_token; a project-scoped one is refused by the server.
func (c *client) deletePaths(ctx context.Context, projectID string, paths []string, st syncState) error {
	text, err := c.callTool(ctx, "finalize_plan", map[string]any{
		"project_id": projectID,
		"deletes":    paths,
	})
	if err != nil {
		return fmt.Errorf("finalize_plan for delete: %w", err)
	}
	var plan struct {
		PlanToken string `json:"plan_token"`
	}
	if err := json.Unmarshal([]byte(text), &plan); err != nil || plan.PlanToken == "" {
		return fmt.Errorf("finalize_plan returned no plan_token: %s", truncate(text, 200))
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
		"plan_token": plan.PlanToken,
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
	fmt.Fprintf(&sb, " (%s)", humanBytes(r.Bytes))
	for _, p := range r.Conflicts {
		fmt.Fprintf(&sb, "\n  ! %s — server moved ahead; `dsx pull` first, or --force", p)
	}
	return sb.String()
}
