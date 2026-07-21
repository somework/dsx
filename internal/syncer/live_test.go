//go:build live

package syncer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/somework/dsx/internal/auth"
	"github.com/somework/dsx/internal/fmtutil"
	"github.com/somework/dsx/internal/mcp"
)

type unchangedReply struct {
	Unchanged bool   `json:"unchanged"`
	Etag      string `json:"etag"`
	Path      string `json:"path"`
}

const liveProject = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

const scratchPrefix = ".dsx-selftest"

func liveProjectID() string {
	if v := os.Getenv("DSX_LIVE_PROJECT"); v != "" {
		return v
	}
	return liveProject
}

func liveClient(t *testing.T) (*mcp.Client, context.Context) {
	t.Helper()
	token, err := auth.LoadToken()
	if err != nil {
		t.Skipf("no usable Claude Code credential, skipping live suite: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	return mcp.New(token), ctx
}

func liveTree(t *testing.T, c *mcp.Client, ctx context.Context) map[string]RemoteEntry {
	t.Helper()
	files, err := WalkTree(ctx, c, liveProjectID(), 8)
	if err != nil {
		t.Fatalf("WalkTree: %v", err)
	}
	return files
}

func liveScratch(t *testing.T, c *mcp.Client, ctx context.Context, suffix string) string {
	t.Helper()
	path := scratchPrefix + suffix
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := liveRemove(c, cleanupCtx, path); err != nil && !liveIsMissing(err) {
			t.Errorf("SCRATCH FILE LEFT BEHIND: %s in project %s (%v) — remove it by hand",
				path, liveProjectID(), err)
			return
		}
		files, err := WalkTree(cleanupCtx, c, liveProjectID(), 8)
		if err != nil {
			t.Errorf("could not confirm %s is gone: %v", path, err)
			return
		}
		if _, still := files[path]; still {
			t.Errorf("SCRATCH FILE STILL PRESENT after delete: %s", path)
		}
	})
	return path
}

func liveIsMissing(err error) bool {
	var te *mcp.ToolError
	if !errors.As(err, &te) {
		return false
	}
	return strings.Contains(te.Text, "not found")
}

func liveRemove(c *mcp.Client, ctx context.Context, paths ...string) error {
	token, err := PlanToken(ctx, c, map[string]any{
		"project_id": liveProjectID(),
		"deletes":    paths,
	})
	if err != nil {
		return err
	}
	_, err = c.CallTool(ctx, "delete_files", map[string]any{
		"project_id": liveProjectID(),
		"plan_token": token,
		"paths":      paths,
	})
	return err
}

func liveWriteRaw(c *mcp.Client, ctx context.Context, path string, body []byte, extra map[string]any) (string, error) {
	file := map[string]any{
		"path": path, "data": base64.StdEncoding.EncodeToString(body), "encoding": "base64",
	}
	for k, v := range extra {
		file[k] = v
	}
	return c.CallTool(ctx, "write_files", map[string]any{
		"project_id": liveProjectID(),
		"files":      []any{file},
	})
}

func liveWrite(t *testing.T, c *mcp.Client, ctx context.Context, path string, body []byte) string {
	t.Helper()

	text, err := liveWriteRaw(c, ctx, path, body, nil)
	var ge *mcp.GrantError
	if errors.As(err, &ge) {
		token, planErr := planFor(ctx, c, liveProjectID(), []string{path})
		if planErr != nil {
			t.Fatalf("could not self-authorise the write of %s: %v", path, planErr)
		}
		text, err = c.CallTool(ctx, "write_files", map[string]any{
			"project_id": liveProjectID(),
			"plan_token": token,
			"files": []any{map[string]any{
				"path": path, "data": base64.StdEncoding.EncodeToString(body), "encoding": "base64",
			}},
		})
	}
	if err != nil {
		t.Fatalf("write_files %s: %v", path, err)
	}

	var res writeResult
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatalf("write_files reply is not the documented shape: %v\n%s", err, fmtutil.Truncate(text, 300))
	}
	return res.Etags[path]
}

func liveAuthorised(t *testing.T, c *mcp.Client, ctx context.Context, paths ...string) string {
	t.Helper()
	token, err := planFor(ctx, c, liveProjectID(), paths)
	if err != nil {
		t.Fatalf("finalize_plan for %v: %v", paths, err)
	}
	return token
}

func TestLiveNeedsProjectGrantIsAnHTTP403NotAToolError(t *testing.T) {
	c, ctx := liveClient(t)
	path := liveScratch(t, c, ctx, "-grant.txt")

	_, err := liveWriteRaw(c, ctx, path, []byte("grant probe\n"), nil)
	if err == nil {
		t.Skip("this project has a standing write grant, so the 403 path cannot be probed here")
	}

	var ge *mcp.GrantError
	if !errors.As(err, &ge) {
		var te *mcp.ToolError
		if errors.As(err, &te) {
			t.Fatalf("needs_project_grant now arrives as a TOOL error, not an HTTP 403 — "+
				"push's self-authorisation is dead code and users will be sent to a browser: %v", te)
		}
		t.Fatalf("a write without a grant failed in a way dsx does not model: %v", err)
	}
	if ge.ProjectID != liveProjectID() {
		t.Errorf("grant error names project %q, want %q", ge.ProjectID, liveProjectID())
	}

	token, err := planFor(ctx, c, liveProjectID(), []string{path})
	if err != nil {
		t.Fatalf("finalize_plan could not authorise the write the 403 demanded: %v", err)
	}
	if _, err := c.CallTool(ctx, "write_files", map[string]any{
		"project_id": liveProjectID(),
		"plan_token": token,
		"files": []any{map[string]any{
			"path": path, "data": base64.StdEncoding.EncodeToString([]byte("grant probe\n")),
			"encoding": "base64",
		}},
	}); err != nil {
		t.Fatalf("a path-scoped plan_token did not authorise the write it named: %v", err)
	}
}

func TestLiveRefusesToCreateProjects(t *testing.T) {
	b, err := os.ReadFile("live_test.go")
	if err != nil {
		t.Fatal(err)
	}

	needle := `callTool(ctx, "` + "create" + `_project"`
	if strings.Contains(string(b), needle) {
		t.Fatal("the live suite creates a project; there is no delete_project, so that project is permanent litter")
	}
	if liveProjectID() == "" {
		t.Fatal("no live project configured")
	}
}

func TestLiveListFilesShape(t *testing.T) {
	c, ctx := liveClient(t)

	entries, err := listDir(ctx, c, liveProjectID(), "")
	if err != nil {
		t.Fatalf("list_files: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the project root listed empty; the rest of this suite would prove nothing")
	}

	var sawFile, sawDir bool
	for _, e := range entries {
		if strings.HasPrefix(e.Path, "/") {
			t.Errorf("path %q is absolute; PROTOCOL.md says project-relative", e.Path)
		}
		if e.isDir() {
			sawDir = true
			if e.Etag != "" {
				t.Errorf("directory %q carries etag %q; PROTOCOL.md says directories have none", e.Path, e.Etag)
			}
			continue
		}
		sawFile = true
		if e.Etag == "" {
			t.Errorf("file %q has no etag — one listing per directory is supposed to price the whole tree", e.Path)
		}
	}
	if !sawFile {
		t.Error("no files in the root listing")
	}
	if !sawDir {
		t.Log("no directories in the root listing; the directory claims went untested")
	}
}

func TestLiveReadFileEnvelopeAndSizeAgree(t *testing.T) {
	c, ctx := liveClient(t)
	files := liveTree(t, c, ctx)

	checked := 0
	for _, path := range SortedPaths(files) {
		e := files[path]
		if e.Size == 0 || e.Size > 200<<10 {
			continue
		}
		body, etag, err := c.ReadFull(ctx, liveProjectID(), path)
		if err != nil {
			if isBinaryRefusal(err) {
				continue
			}
			t.Fatalf("readFull %s: %v", path, err)
		}
		if int64(len(body)) != e.Size {
			t.Fatalf("%s: decoded %d bytes, list_files reported %d — the envelope framing or the entity decoding is wrong",
				path, len(body), e.Size)
		}
		if etag != e.Etag {
			t.Errorf("%s: read_file etag %q disagrees with list_files etag %q", path, etag, e.Etag)
		}
		if checked++; checked == 5 {
			break
		}
	}
	if checked == 0 {
		t.Skip("no readable text file small enough to check")
	}
}

func TestLiveIfNoneMatchShortCircuits(t *testing.T) {
	c, ctx := liveClient(t)
	files := liveTree(t, c, ctx)

	var path, etag string
	for _, p := range SortedPaths(files) {
		e := files[p]
		if e.Size == 0 || e.Size >= 200<<10 {
			continue
		}
		if _, _, err := c.ReadFull(ctx, liveProjectID(), p); err != nil {
			continue
		}
		path, etag = p, e.Etag
		break
	}
	if path == "" {
		t.Skip("no readable text file to probe if_none_match with")
	}

	text, err := c.CallTool(ctx, "read_file", map[string]any{
		"project_id": liveProjectID(), "path": path, "if_none_match": etag,
	})
	if err != nil {
		t.Fatalf("read_file if_none_match: %v", err)
	}
	var un unchangedReply
	if err := json.Unmarshal([]byte(text), &un); err != nil {
		t.Fatalf("if_none_match reply is not the documented shape: %v\n%s", err, fmtutil.Truncate(text, 200))
	}
	if !un.Unchanged {
		t.Errorf("if_none_match against the current etag did not report unchanged: %s", fmtutil.Truncate(text, 200))
	}
}

func TestLiveWriteReplyIsAMapNotAList(t *testing.T) {
	c, ctx := liveClient(t)
	path := liveScratch(t, c, ctx, ".txt")

	body := []byte("dsx live self-test; safe to delete\n")
	text, err := c.CallTool(ctx, "write_files", map[string]any{
		"project_id": liveProjectID(),
		"plan_token": liveAuthorised(t, c, ctx, path),
		"files": []any{map[string]any{
			"path": path, "data": base64.StdEncoding.EncodeToString(body), "encoding": "base64",
		}},
	})
	if err != nil {
		t.Fatalf("write_files: %v", err)
	}

	var asMap writeResult
	if err := json.Unmarshal([]byte(text), &asMap); err != nil {
		t.Fatalf("write_files reply no longer parses as {etags:{path:etag},written,url}: %v\n%s",
			err, fmtutil.Truncate(text, 300))
	}
	if asMap.Etags[path] == "" {
		t.Fatalf("no etag for %s in the write reply; push would have nothing to record: %s",
			path, fmtutil.Truncate(text, 300))
	}
	if asMap.Written != 1 {
		t.Errorf("written = %d, want 1", asMap.Written)
	}
}

func TestLiveWriteThenReadIsByteExact(t *testing.T) {
	c, ctx := liveClient(t)
	before := len(liveTree(t, c, ctx))
	path := liveScratch(t, c, ctx, "-roundtrip.txt")

	body := []byte("a & b < c > d\n&amp; stays literal\nunicode: привет ✓\n")

	etag := liveWrite(t, c, ctx, path, body)
	if etag == "" {
		t.Fatal("write returned no etag")
	}

	got, gotEtag, err := c.ReadFull(ctx, liveProjectID(), path)
	if err != nil {
		t.Fatalf("readFull: %v", err)
	}
	if got != string(body) {
		t.Errorf("round trip is not byte-exact:\n want %q\n  got %q", body, got)
	}
	if gotEtag != etag {
		t.Errorf("etag changed with no write between: %q -> %q", etag, gotEtag)
	}

	if err := liveRemove(c, ctx, path); err != nil {
		t.Fatalf("delete_files: %v", err)
	}
	if after := len(liveTree(t, c, ctx)); after != before {
		t.Errorf("file count %d -> %d; the project did not return to its original state", before, after)
	}
}

func TestLiveBinaryIsDecidedByContentNotExtension(t *testing.T) {
	c, ctx := liveClient(t)

	t.Run("ascii in a .png is served", func(t *testing.T) {
		path := liveScratch(t, c, ctx, "-ascii.png")
		liveWrite(t, c, ctx, path, []byte("this is not a png\n"))

		got, _, err := c.ReadFull(ctx, liveProjectID(), path)
		if err != nil {
			t.Fatalf("a .png holding ASCII was refused; PROTOCOL.md says the extension buys nothing: %v", err)
		}
		if got != "this is not a png\n" {
			t.Errorf("body = %q", got)
		}
	})

	t.Run("invalid utf-8 in a .txt is refused", func(t *testing.T) {
		path := liveScratch(t, c, ctx, "-binary.txt")
		liveWrite(t, c, ctx, path, []byte{0xff, 0xfe, 0x00, 0x01})

		_, _, err := c.ReadFull(ctx, liveProjectID(), path)
		if err == nil {
			t.Fatal("a .txt holding invalid UTF-8 was served; PROTOCOL.md says the criterion is UTF-8 validity")
		}
		if !isBinaryRefusal(err) {
			t.Fatalf("refused, but not with the refusal dsx recognises — pull would report an error instead of skipping: %v", err)
		}
	})

	t.Run("NUL is valid utf-8 and is served", func(t *testing.T) {
		path := liveScratch(t, c, ctx, "-nul.txt")
		body := []byte{'a', 0x00, 0x01, 0x02, 'b', '\n'}
		liveWrite(t, c, ctx, path, body)

		got, _, err := c.ReadFull(ctx, liveProjectID(), path)
		if err != nil {
			t.Fatalf("NUL bytes were treated as binary; PROTOCOL.md says NUL is valid UTF-8: %v", err)
		}
		if got != string(body) {
			t.Errorf("NUL round trip not byte-exact: %q", got)
		}
	})
}

func TestLiveWindowedReadReassemblesByteExactly(t *testing.T) {
	c, ctx := liveClient(t)
	before := len(liveTree(t, c, ctx))
	path := liveScratch(t, c, ctx, "-windowed.txt")

	var sb strings.Builder
	for i := 0; sb.Len() < 300<<10; i++ {
		fmt.Fprintf(&sb, "line %06d — dsx live self-test, safe to delete, padding padding\n", i)
	}
	body := sb.String()

	liveWrite(t, c, ctx, path, []byte(body))

	text, err := c.CallTool(ctx, "read_file", map[string]any{
		"project_id": liveProjectID(), "path": path,
	})
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	env, err := mcp.ParseEnvelope(text)
	if err != nil {
		t.Fatalf("parseEnvelope: %v", err)
	}
	if env.Complete() {
		t.Skipf("the server returned %d bytes whole; the 256 KiB cap has moved and this test no longer probes windowing", len(env.Body))
	}
	t.Logf("first window: lines %d-%d of %d, %d bytes", env.Lines[0], env.Lines[1], env.TotalLines, len(env.Body))

	got, _, err := c.ReadFull(ctx, liveProjectID(), path)
	if err != nil {
		t.Fatalf("readFull: %v", err)
	}
	if got == body {
		if err := liveRemove(c, ctx, path); err != nil {
			t.Fatalf("delete_files: %v", err)
		}
		if after := len(liveTree(t, c, ctx)); after != before {
			t.Errorf("file count %d -> %d", before, after)
		}
		return
	}

	n := min(len(got), len(body))
	at := n
	for i := range n {
		if got[i] != body[i] {
			at = i
			break
		}
	}
	t.Errorf("windowed read is NOT byte-exact: got %d bytes, want %d; first difference at offset %d\n"+
		"  want %q\n   got %q\n"+
		"If the offset lands on a line boundary, readFull is dropping the newline that ends each window.",
		len(got), len(body), at,
		body[max(0, at-40):min(len(body), at+40)],
		got[max(0, at-40):min(len(got), at+40)])
}

func TestLiveDeleteRefusesAProjectScopedToken(t *testing.T) {
	c, ctx := liveClient(t)
	path := liveScratch(t, c, ctx, "-scope.txt")
	liveWrite(t, c, ctx, path, []byte("scope probe\n"))

	text, err := c.CallTool(ctx, "finalize_plan", map[string]any{
		"project_id": liveProjectID(), "scope": "project",
	})
	if err != nil {
		t.Fatalf("finalize_plan scope=project: %v", err)
	}
	var plan struct {
		PlanToken string `json:"plan_token"`

		ExpiresAt int64  `json:"expires_at"`
		Scope     string `json:"scope"`
	}
	if err := json.Unmarshal([]byte(text), &plan); err != nil || plan.PlanToken == "" {
		t.Fatalf("project-scoped finalize_plan returned no plan_token: %v\n%s", err, fmtutil.Truncate(text, 300))
	}
	if plan.ExpiresAt == 0 {
		t.Error("PROTOCOL.md says a project-scoped plan reports expires_at")
	}
	if plan.Scope != "project" {
		t.Errorf("scope = %q, want project", plan.Scope)
	}

	if life := time.Until(time.Unix(plan.ExpiresAt, 0)); life < time.Hour || life > 8*time.Hour {
		t.Errorf("a project-scoped token lives %s; PROTOCOL.md records about 4h", life.Round(time.Minute))
	}

	_, err = c.CallTool(ctx, "delete_files", map[string]any{
		"project_id": liveProjectID(),
		"plan_token": plan.PlanToken,
		"paths":      []string{path},
	})
	if err == nil {
		t.Error("delete_files accepted a project-scoped token; PROTOCOL.md says it never deletes")
	}
}

func TestLiveIfMatchGuardsAgainstABlindOverwrite(t *testing.T) {
	c, ctx := liveClient(t)
	path := liveScratch(t, c, ctx, "-ifmatch.txt")

	etag := liveWrite(t, c, ctx, path, []byte("first\n"))
	token := liveAuthorised(t, c, ctx, path)

	_, err := c.CallTool(ctx, "write_files", map[string]any{
		"project_id": liveProjectID(),
		"plan_token": token,
		"files": []any{map[string]any{
			"path": path, "data": base64.StdEncoding.EncodeToString([]byte("second\n")),
			"encoding": "base64", "if_match": "1",
		}},
	})
	if err == nil {
		t.Fatal("write_files accepted a stale if_match; dsx's whole conflict guard runs through this")
	}

	paths, ok := mcp.ConflictFromToolError(err)
	if !ok {
		t.Fatalf("a stale if_match no longer parses as {\"conflicts\":[…]}; server-detected "+
			"conflicts would exit 1 instead of 3: %v", err)
	}
	if len(paths) != 1 || paths[0] != path {
		t.Errorf("conflict names %v, want [%s]", paths, path)
	}
	var sc mcp.ServerConflict
	var te *mcp.ToolError
	if errors.As(err, &te) && json.Unmarshal([]byte(te.Text), &sc) == nil {
		if !strings.Contains(sc.Message, "Nothing was written") {
			t.Errorf("the server no longer promises atomicity; dsx reports this as a plain "+
				"conflict on the strength of that promise: %q", sc.Message)
		}
		if sc.Conflicts[0].Etag == "" || sc.Conflicts[0].Etag == "1" {
			t.Errorf("the reply did not carry the current etag: %+v", sc.Conflicts[0])
		}
	}

	if _, err := c.CallTool(ctx, "write_files", map[string]any{
		"project_id": liveProjectID(),
		"plan_token": token,
		"files": []any{map[string]any{
			"path": path, "data": base64.StdEncoding.EncodeToString([]byte("second\n")),
			"encoding": "base64", "if_match": etag,
		}},
	}); err != nil {
		t.Errorf("write_files refused the current etag: %v", err)
	}
}

func TestLiveIfMatchZeroAssertsThePathIsNew(t *testing.T) {
	c, ctx := liveClient(t)
	path := liveScratch(t, c, ctx, "-new.txt")

	token := liveAuthorised(t, c, ctx, path)

	if _, err := c.CallTool(ctx, "write_files", map[string]any{
		"project_id": liveProjectID(),
		"plan_token": token,
		"files": []any{map[string]any{
			"path": path, "data": base64.StdEncoding.EncodeToString([]byte("new\n")),
			"encoding": "base64", "if_match": "0",
		}},
	}); err != nil {
		t.Fatalf(`if_match "0" was refused for a path that does not exist: %v`, err)
	}

	_, err := c.CallTool(ctx, "write_files", map[string]any{
		"project_id": liveProjectID(),
		"plan_token": token,
		"files": []any{map[string]any{
			"path": path, "data": base64.StdEncoding.EncodeToString([]byte("again\n")),
			"encoding": "base64", "if_match": "0",
		}},
	})
	if err == nil {
		t.Error(`if_match "0" was accepted for a path that now exists; it is supposed to assert absence`)
	}
}

func TestLiveUnsupportedContentTypeIsRefused(t *testing.T) {
	c, ctx := liveClient(t)
	path := liveScratch(t, c, ctx, ".bin")

	_, err := c.CallTool(ctx, "write_files", map[string]any{
		"project_id": liveProjectID(),
		"plan_token": liveAuthorised(t, c, ctx, path),
		"files": []any{map[string]any{
			"path": path, "data": base64.StdEncoding.EncodeToString([]byte("x")), "encoding": "base64",
		}},
	})
	if err == nil {
		t.Skip("the server now accepts .bin; PROTOCOL.md's allowlist needs re-probing")
	}
	if !strings.Contains(err.Error(), "content type") {
		t.Logf("refused, but not the way PROTOCOL.md records: %v", err)
	}
}

func TestLiveToolsListCoversEveryWrappedTool(t *testing.T) {
	c, ctx := liveClient(t)

	raw, err := c.ToolsList(ctx)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	var list struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tl := range list.Tools {
		have[tl.Name] = true
	}

	for _, name := range []string{
		"list_projects", "get_project", "create_project", "list_design_systems",
		"list_files", "read_file", "write_files", "delete_files", "copy_files",
		"finalize_plan", "render_preview", "create_support_js",
		"get_conversation", "put_conversation",
		"list_members", "add_member", "remove_member", "update_member_role",
		"update_sharing", "get_claude_design_prompt",
	} {
		if !have[name] {
			t.Errorf("dsx wraps %q but the server no longer lists it", name)
		}
	}

	for _, name := range mcp.ReadOnlyToolNames() {
		if !have[name] {
			t.Errorf("readOnlyTools names %q, which the server no longer lists", name)
		}
	}
}

func TestLiveResourcesAreStillUnsupported(t *testing.T) {
	c, ctx := liveClient(t)

	_, err := c.RPC(ctx, "resources/list", map[string]any{}, true)
	if err == nil {
		t.Error("resources/list now works — binary files may be readable after all; re-probe PROTOCOL.md")
		return
	}
	if !strings.Contains(err.Error(), "resources") {
		t.Logf("resources/list failed differently than recorded: %v", err)
	}
}

func TestLiveEndToEndPullPushRoundTrip(t *testing.T) {
	c, ctx := liveClient(t)
	before := len(liveTree(t, c, ctx))
	path := liveScratch(t, c, ctx, "-sync.css")

	dir := t.TempDir()
	body := ".dsx-selftest { color: red }\n"
	mkfile(t, dir, path, body)

	rep, err := Push(ctx, c, PushOpts{ProjectID: liveProjectID(), Dir: dir, Concurrency: 4})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if len(rep.Written) != 1 || rep.Written[0] != path {
		t.Fatalf("pushed %v, want just %s", rep.Written, path)
	}

	st, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.ProjectID != liveProjectID() {
		t.Errorf("ledger pinned %q, want %q", st.ProjectID, liveProjectID())
	}
	if st.Files[path].SHA != SHA256Hex([]byte(body)) {
		t.Error("the ledger did not record the bytes it pushed; the next sync would call this a conflict")
	}

	rep2, err := Push(ctx, c, PushOpts{ProjectID: liveProjectID(), Dir: dir, Concurrency: 4})
	if err != nil {
		t.Fatalf("second Push: %v", err)
	}
	if len(rep2.Written) != 0 {
		t.Errorf("a warm push rewrote %v; unchanged files are supposed to cost nothing", rep2.Written)
	}

	dir2 := t.TempDir()
	pullRep, err := Pull(ctx, c, PullOpts{ProjectID: liveProjectID(), Dir: dir2, Concurrency: 8})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	got, err := os.ReadFile(fmt.Sprintf("%s/%s", dir2, path))
	if err != nil {
		t.Fatalf("the pushed file did not come back: %v (pulled %d)", err, len(pullRep.Fetched))
	}
	if string(got) != body {
		t.Errorf("push→pull is not byte-exact:\n want %q\n  got %q", body, got)
	}

	if err := liveRemove(c, ctx, path); err != nil {
		t.Fatalf("delete_files: %v", err)
	}
	if after := len(liveTree(t, c, ctx)); after != before {
		t.Errorf("file count %d -> %d; the project did not return to its original state", before, after)
	}
}

// TestLiveFetchBaselineMatchesReadFull proves binary detection by content —
// one of the three protocol facts already guessed wrong once (see
// PROTOCOL.md) — actually governs what Fetch records, rather than trusting a
// mock that only repeats the belief. It creates no project, writes no server
// path beyond the two scratch files liveScratch itself owns and removes, and
// leaves the project's file count unchanged.
func TestLiveFetchBaselineMatchesReadFull(t *testing.T) {
	c, ctx := liveClient(t)
	before := len(liveTree(t, c, ctx))

	textPath := liveScratch(t, c, ctx, "-fetch-text.css")
	textBody := []byte(".dsx-selftest { color: blue }\n")
	liveWrite(t, c, ctx, textPath, textBody)

	// .txt, not .bin: the write allowlist refuses .bin (see
	// TestLiveUnsupportedContentTypeIsRefused), while binary detection reads
	// the content, so invalid UTF-8 in a .txt is what makes this path binary.
	binPath := liveScratch(t, c, ctx, "-fetch-binary.txt")
	binBody := []byte{0xff, 0xfe, 0x00, 0x01}
	liveWrite(t, c, ctx, binPath, binBody)

	// Fetch's narrow set is present-and-untracked: no `dsx pin` here, just a
	// bare directory holding placeholders at the same paths. The binary
	// placeholder's own bytes are irrelevant — the server refuses read_file
	// on binPath regardless of what sits locally.
	dir := t.TempDir()
	mkfile(t, dir, textPath, string(textBody))
	mkfile(t, dir, binPath, "placeholder\n")

	// Anchored after the two scratch writes, not before them: the claim is
	// about what Fetch does, and the setup writes are not Fetch.
	beforeFetch := len(liveTree(t, c, ctx))

	rep, err := Fetch(ctx, c, FetchOpts{ProjectID: liveProjectID(), Dir: dir, Concurrency: 4})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !slices.Contains(rep.Fetched, textPath) {
		t.Fatalf("Fetched = %v, want %s present", rep.Fetched, textPath)
	}
	if slices.Contains(rep.Fetched, binPath) {
		t.Errorf("Fetched = %v, %s must not be recorded — it is binary", rep.Fetched, binPath)
	}

	bl, err := loadBaseline(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := bl.Verified[binPath]; ok {
		t.Errorf("baseline holds an entry for the binary path %s: %+v", binPath, bl.Verified[binPath])
	}
	if len(bl.Verified) == 0 {
		t.Fatal("baseline holds no entries at all")
	}
	for path, entry := range bl.Verified {
		body, _, err := c.ReadFull(ctx, liveProjectID(), path)
		if err != nil {
			t.Fatalf("ReadFull(%s) for independent verification: %v", path, err)
		}
		if want := SHA256Hex([]byte(body)); entry.SHA != want {
			t.Errorf("baseline[%s].SHA = %s, want %s from an independent re-read", path, entry.SHA, want)
		}
	}

	if after := len(liveTree(t, c, ctx)); after != beforeFetch {
		t.Errorf("file count %d -> %d; fetch must not write any server path", beforeFetch, after)
	}

	if err := liveRemove(c, ctx, textPath, binPath); err != nil {
		t.Fatalf("delete_files: %v", err)
	}
	if after := len(liveTree(t, c, ctx)); after != before {
		t.Errorf("file count %d -> %d; the project did not return to its original state", before, after)
	}
}

// checkListedEtagAndSize cross-checks the etag write_files returned against
// what list_files reports for the same path, and the listed size against what
// was written.
func checkListedEtagAndSize(t *testing.T, c *mcp.Client, ctx context.Context, path, wantEtag string, wantSize int) {
	t.Helper()
	files := liveTree(t, c, ctx)
	e, ok := files[path]
	if !ok {
		t.Fatalf("list_files does not show %s right after a write", path)
	}
	if e.Etag != wantEtag {
		t.Errorf("%s: list_files etag %q disagrees with write_files etag %q", path, e.Etag, wantEtag)
	}
	if e.Size != int64(wantSize) {
		t.Errorf("%s: list_files size %d, want %d", path, e.Size, wantSize)
	}
}

// TestLiveEtagIsRevisionDerivedNotContentDerived asserts that re-putting
// byte-identical content yields a different etag: content is not an input to
// the etag, only the write itself is. The etag's format (a microsecond
// timestamp) is not asserted here — dsx treats etags as opaque strings
// everywhere, and pinning the format would fail on a server-side change that
// costs dsx nothing.
//
// The middle write of B is the positive control, not scaffolding: without a
// real revision bump between the two writes of A, a server handing out one
// constant etag would pass the final assertion for the wrong reason.
func TestLiveEtagIsRevisionDerivedNotContentDerived(t *testing.T) {
	c, ctx := liveClient(t)
	before := len(liveTree(t, c, ctx))
	path := liveScratch(t, c, ctx, "-etag-probe.txt")

	bodyA := []byte("dsx live self-test — etag probe A; safe to delete\n")
	bodyB := []byte("dsx live self-test — etag probe B, longer than A; safe to delete\n")

	etagA1 := liveWrite(t, c, ctx, path, bodyA)
	if etagA1 == "" {
		t.Fatal("the first write of A returned no etag")
	}
	checkListedEtagAndSize(t, c, ctx, path, etagA1, len(bodyA))

	etagB := liveWrite(t, c, ctx, path, bodyB)
	if etagB == "" {
		t.Fatal("the write of B returned no etag")
	}
	if etagB == etagA1 {
		t.Fatalf("positive control failed: writing genuinely different content kept the etag "+
			"(%s == %s); either the write did not land or the server hands out a constant etag, "+
			"and either way the rest of this test proves nothing", etagA1, etagB)
	}
	checkListedEtagAndSize(t, c, ctx, path, etagB, len(bodyB))

	etagA2 := liveWrite(t, c, ctx, path, bodyA)
	if etagA2 == "" {
		t.Fatal("the second write of A returned no etag")
	}
	checkListedEtagAndSize(t, c, ctx, path, etagA2, len(bodyA))

	if etagA1 == etagA2 {
		t.Fatalf("etag is content-derived, contradicting the measured behaviour this test claims: "+
			"re-putting the original bytes (A1=%s) after an intervening different write (B=%s) "+
			"produced the same etag again (A2=%s)", etagA1, etagB, etagA2)
	}

	if err := liveRemove(c, ctx, path); err != nil {
		t.Fatalf("delete_files: %v", err)
	}
	if after := len(liveTree(t, c, ctx)); after != before {
		t.Errorf("file count %d -> %d; the project did not return to its original state", before, after)
	}
}
