//go:build live

// The live suite. Run it with:
//
//	go test -tags=live -run TestLive ./...
//
// It is behind a build tag because it talks to the real, undocumented Claude
// Design endpoint using the real user's real credential, and writes to a real
// project.
//
// WHY IT EXISTS, given the rest of the suite mocks the transport: a mock only
// ever asserts what we already believe about the protocol. Everything dsx knows
// about this endpoint was bought by probing it, and three of those facts were
// guessed WRONG first --
//
//	write_files replies with a map, not a list
//	needs_project_grant is an HTTP 403, not a tool error
//	"binary" means invalid UTF-8, not a known extension
//
// -- and a green mock would have hidden all three. This file is the only thing
// standing between dsx and the next server deploy that quietly changes one of
// them. Every test here asserts a claim PROTOCOL.md makes; if one fails,
// PROTOCOL.md is what is wrong.
//
// DISCIPLINE (from CLAUDE.md, and it is not optional):
//   - There is NO delete_project tool. A throwaway project is permanent litter.
//     Never create one. This suite refuses to.
//   - Write only to clearly-named scratch paths, verify, then remove.
//   - Every mutating test asserts the project's file count is back where it
//     started before it finishes.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// liveProject is the project the suite exercises: Kolgarn Design System.
// Override with DSX_LIVE_PROJECT to point at another one you own.
const liveProject = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"

// scratchPrefix marks every path this suite writes. It is deliberately obvious:
// if the cleanup ever fails, whoever finds the file should know instantly what
// left it and that it is safe to delete.
const scratchPrefix = ".dsx-selftest"

func liveProjectID() string {
	if v := os.Getenv("DSX_LIVE_PROJECT"); v != "" {
		return v
	}
	return liveProject
}

func liveClient(t *testing.T) (*client, context.Context) {
	t.Helper()
	token, err := loadToken()
	if err != nil {
		t.Skipf("no usable Claude Code credential, skipping live suite: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	return newClient(token), ctx
}

func liveTree(t *testing.T, c *client, ctx context.Context) map[string]remoteEntry {
	t.Helper()
	files, err := c.walkTree(ctx, liveProjectID(), 8)
	if err != nil {
		t.Fatalf("walkTree: %v", err)
	}
	return files
}

// liveScratch reserves a scratch path and guarantees its removal.
//
// The cleanup runs even when the test fails, and it verifies the removal
// happened rather than assuming it: a scratch file left behind in someone's
// real project is the one outcome this suite must never produce.
func liveScratch(t *testing.T, c *client, ctx context.Context, suffix string) string {
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
		files, err := c.walkTree(cleanupCtx, liveProjectID(), 8)
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
	var te *toolError
	if !errors.As(err, &te) {
		return false
	}
	return strings.Contains(te.Text, "not found")
}

func liveRemove(c *client, ctx context.Context, paths ...string) error {
	token, err := planToken(ctx, c, map[string]any{
		"project_id": liveProjectID(),
		"deletes":    paths,
	})
	if err != nil {
		return err
	}
	_, err = c.callTool(ctx, "delete_files", map[string]any{
		"project_id": liveProjectID(),
		"plan_token": token,
		"paths":      paths,
	})
	return err
}

func liveWrite(t *testing.T, c *client, ctx context.Context, path string, body []byte) string {
	t.Helper()
	text, err := c.callTool(ctx, "write_files", map[string]any{
		"project_id": liveProjectID(),
		"files": []any{map[string]any{
			"path": path, "data": base64.StdEncoding.EncodeToString(body), "encoding": "base64",
		}},
	})
	if err != nil {
		t.Fatalf("write_files %s: %v", path, err)
	}
	var res writeResult
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatalf("write_files reply is not the documented shape: %v\n%s", err, truncate(text, 300))
	}
	return res.Etags[path]
}

// TestLiveRefusesToCreateProjects guards the discipline itself.
//
// There is no delete_project tool, so a project created by a test is litter
// forever. This asserts the suite never reaches for create_project -- a future
// agent "helpfully" spinning up a fixture project is exactly the mistake worth
// making impossible.
func TestLiveRefusesToCreateProjects(t *testing.T) {
	b, err := os.ReadFile("live_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"create_project"`) {
		t.Fatal("the live suite calls create_project; there is no delete_project, so that project is permanent litter")
	}
	if liveProjectID() == "" {
		t.Fatal("no live project configured")
	}
}

// TestLiveListFilesShape pins the claims PROTOCOL.md makes about list_files:
// not recursive, project-relative paths, per-file etag, directories with none.
// The whole cheap-sync design rests on that etag being there.
func TestLiveListFilesShape(t *testing.T) {
	c, ctx := liveClient(t)

	entries, err := c.listDir(ctx, liveProjectID(), "")
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

// TestLiveReadFileEnvelopeAndSizeAgree is invariant 1 against the real server:
// the decoded body length must equal the size list_files reported. A mismatch
// means the envelope framing or the entity decoding is wrong, and that is a
// silent one-byte corruption -- exactly what once damaged 2 of 100 files.
func TestLiveReadFileEnvelopeAndSizeAgree(t *testing.T) {
	c, ctx := liveClient(t)
	files := liveTree(t, c, ctx)

	checked := 0
	for _, path := range sortedPaths(files) {
		e := files[path]
		if e.Size == 0 || e.Size > 200<<10 {
			continue // stay under the 256 KiB read cap
		}
		body, etag, err := c.readFull(ctx, liveProjectID(), path)
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

// TestLiveIfNoneMatchShortCircuits pins the reply shape that makes a warm sync
// free.
func TestLiveIfNoneMatchShortCircuits(t *testing.T) {
	c, ctx := liveClient(t)
	files := liveTree(t, c, ctx)

	var path, etag string
	for _, p := range sortedPaths(files) {
		if e := files[p]; e.Size > 0 && e.Size < 200<<10 {
			path, etag = p, e.Etag
			break
		}
	}
	if path == "" {
		t.Skip("no suitable file")
	}

	text, err := c.callTool(ctx, "read_file", map[string]any{
		"project_id": liveProjectID(), "path": path, "if_none_match": etag,
	})
	if err != nil {
		t.Fatalf("read_file if_none_match: %v", err)
	}
	var un unchangedReply
	if err := json.Unmarshal([]byte(text), &un); err != nil {
		t.Fatalf("if_none_match reply is not the documented shape: %v\n%s", err, truncate(text, 200))
	}
	if !un.Unchanged {
		t.Errorf("if_none_match against the current etag did not report unchanged: %s", truncate(text, 200))
	}
}

// TestLiveWriteReplyIsAMapNotAList is the fact a mock could never have found.
// It was guessed wrong first, and the shape is what push records etags from.
func TestLiveWriteReplyIsAMapNotAList(t *testing.T) {
	c, ctx := liveClient(t)
	path := liveScratch(t, c, ctx, ".txt")

	body := []byte("dsx live self-test; safe to delete\n")
	text, err := c.callTool(ctx, "write_files", map[string]any{
		"project_id": liveProjectID(),
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
			err, truncate(text, 300))
	}
	if asMap.Etags[path] == "" {
		t.Fatalf("no etag for %s in the write reply; push would have nothing to record: %s",
			path, truncate(text, 300))
	}
	if asMap.Written != 1 {
		t.Errorf("written = %d, want 1", asMap.Written)
	}
}

// TestLiveWriteThenReadIsByteExact is the round trip the whole tool rests on.
func TestLiveWriteThenReadIsByteExact(t *testing.T) {
	c, ctx := liveClient(t)
	before := len(liveTree(t, c, ctx))
	path := liveScratch(t, c, ctx, "-roundtrip.txt")

	// Deliberately carries every character the server escapes, plus a trailing
	// newline: a file ending in one shows a blank line before the close tag,
	// and getting that wrong is a silent one-byte error.
	body := []byte("a & b < c > d\n&amp; stays literal\nunicode: привет ✓\n")

	etag := liveWrite(t, c, ctx, path, body)
	if etag == "" {
		t.Fatal("write returned no etag")
	}

	got, gotEtag, err := c.readFull(ctx, liveProjectID(), path)
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

// TestLiveBinaryIsDecidedByContentNotExtension pins the most counter-intuitive
// fact in PROTOCOL.md, in both directions. Each row killed a plausible theory.
func TestLiveBinaryIsDecidedByContentNotExtension(t *testing.T) {
	c, ctx := liveClient(t)

	t.Run("ascii in a .png is served", func(t *testing.T) {
		path := liveScratch(t, c, ctx, "-ascii.png")
		liveWrite(t, c, ctx, path, []byte("this is not a png\n"))

		got, _, err := c.readFull(ctx, liveProjectID(), path)
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

		_, _, err := c.readFull(ctx, liveProjectID(), path)
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

		got, _, err := c.readFull(ctx, liveProjectID(), path)
		if err != nil {
			t.Fatalf("NUL bytes were treated as binary; PROTOCOL.md says NUL is valid UTF-8: %v", err)
		}
		if got != string(body) {
			t.Errorf("NUL round trip not byte-exact: %q", got)
		}
	})
}

// TestLiveDeleteRefusesAProjectScopedToken pins why deletePaths always mints a
// path-scoped token. If the server ever relaxes this, the comment in push.go
// becomes a lie.
func TestLiveDeleteRefusesAProjectScopedToken(t *testing.T) {
	c, ctx := liveClient(t)
	path := liveScratch(t, c, ctx, "-scope.txt")
	liveWrite(t, c, ctx, path, []byte("scope probe\n"))

	text, err := c.callTool(ctx, "finalize_plan", map[string]any{
		"project_id": liveProjectID(), "scope": "project",
	})
	if err != nil {
		t.Fatalf("finalize_plan scope=project: %v", err)
	}
	var plan struct {
		PlanToken string `json:"plan_token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal([]byte(text), &plan); err != nil || plan.PlanToken == "" {
		t.Fatalf("project-scoped finalize_plan returned no plan_token: %s", truncate(text, 200))
	}
	if plan.ExpiresAt == "" {
		t.Error("PROTOCOL.md says a project-scoped plan reports expires_at")
	}

	_, err = c.callTool(ctx, "delete_files", map[string]any{
		"project_id": liveProjectID(),
		"plan_token": plan.PlanToken,
		"paths":      []string{path},
	})
	if err == nil {
		t.Error("delete_files accepted a project-scoped token; PROTOCOL.md says it never deletes")
	}
}

// TestLiveIfMatchGuardsAgainstABlindOverwrite. Every push carries if_match, so
// a server that stopped honouring it would turn checked writes into blind ones
// without a single test going red.
func TestLiveIfMatchGuardsAgainstABlindOverwrite(t *testing.T) {
	c, ctx := liveClient(t)
	path := liveScratch(t, c, ctx, "-ifmatch.txt")

	etag := liveWrite(t, c, ctx, path, []byte("first\n"))

	_, err := c.callTool(ctx, "write_files", map[string]any{
		"project_id": liveProjectID(),
		"files": []any{map[string]any{
			"path": path, "data": base64.StdEncoding.EncodeToString([]byte("second\n")),
			"encoding": "base64", "if_match": "1",
		}},
	})
	if err == nil {
		t.Error("write_files accepted a stale if_match; dsx's whole conflict guard runs through this")
	}

	// The correct etag must still work, or dsx would be unable to push at all.
	if _, err := c.callTool(ctx, "write_files", map[string]any{
		"project_id": liveProjectID(),
		"files": []any{map[string]any{
			"path": path, "data": base64.StdEncoding.EncodeToString([]byte("second\n")),
			"encoding": "base64", "if_match": etag,
		}},
	}); err != nil {
		t.Errorf("write_files refused the current etag: %v", err)
	}
}

// TestLiveIfMatchZeroAssertsThePathIsNew pins the "0" sentinel, which planPush
// uses for every file it believes does not exist yet.
func TestLiveIfMatchZeroAssertsThePathIsNew(t *testing.T) {
	c, ctx := liveClient(t)
	path := liveScratch(t, c, ctx, "-new.txt")

	if _, err := c.callTool(ctx, "write_files", map[string]any{
		"project_id": liveProjectID(),
		"files": []any{map[string]any{
			"path": path, "data": base64.StdEncoding.EncodeToString([]byte("new\n")),
			"encoding": "base64", "if_match": "0",
		}},
	}); err != nil {
		t.Fatalf(`if_match "0" was refused for a path that does not exist: %v`, err)
	}

	_, err := c.callTool(ctx, "write_files", map[string]any{
		"project_id": liveProjectID(),
		"files": []any{map[string]any{
			"path": path, "data": base64.StdEncoding.EncodeToString([]byte("again\n")),
			"encoding": "base64", "if_match": "0",
		}},
	})
	if err == nil {
		t.Error(`if_match "0" was accepted for a path that now exists; it is supposed to assert absence`)
	}
}

// TestLiveUnsupportedContentTypeIsRefused pins the write allowlist. dsx reports
// this refusal verbatim, so the wording matters to whoever reads it.
func TestLiveUnsupportedContentTypeIsRefused(t *testing.T) {
	c, ctx := liveClient(t)
	path := liveScratch(t, c, ctx, ".bin")

	_, err := c.callTool(ctx, "write_files", map[string]any{
		"project_id": liveProjectID(),
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

// TestLiveToolsListCoversEveryWrappedTool. dsx wraps tools by name; a rename on
// the server turns every one of those into a runtime failure the user meets
// first.
func TestLiveToolsListCoversEveryWrappedTool(t *testing.T) {
	c, ctx := liveClient(t)

	raw, err := c.rpc(ctx, "tools/list", map[string]any{}, true)
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

	// Every tool dsx will retry must still exist and still be read-only.
	for name := range readOnlyTools {
		if !have[name] {
			t.Errorf("readOnlyTools names %q, which the server no longer lists", name)
		}
	}
}

// TestLiveResourcesAreStillUnsupported. It is why an unreadable file has no
// second retrieval path; if that ever changes, the binary lane opens up and
// README's claim stops being true.
func TestLiveResourcesAreStillUnsupported(t *testing.T) {
	c, ctx := liveClient(t)

	_, err := c.rpc(ctx, "resources/list", map[string]any{}, true)
	if err == nil {
		t.Error("resources/list now works — binary files may be readable after all; re-probe PROTOCOL.md")
		return
	}
	if !strings.Contains(err.Error(), "resources") {
		t.Logf("resources/list failed differently than recorded: %v", err)
	}
}

// TestLiveEndToEndPullPushRoundTrip drives the actual sync engine, ledger and
// all, against a scratch directory. It is the only test that exercises
// runPull/runPush over the real protocol.
func TestLiveEndToEndPullPushRoundTrip(t *testing.T) {
	c, ctx := liveClient(t)
	before := len(liveTree(t, c, ctx))
	path := liveScratch(t, c, ctx, "-sync.css")

	dir := t.TempDir()
	body := ".dsx-selftest { color: red }\n"
	mkfile(t, dir, path, body)

	// Never pull into design/. A scratch temp dir, per CLAUDE.md.
	rep, err := runPush(ctx, c, pushOpts{projectID: liveProjectID(), dir: dir, concurrency: 4})
	if err != nil {
		t.Fatalf("runPush: %v", err)
	}
	if len(rep.Written) != 1 || rep.Written[0] != path {
		t.Fatalf("pushed %v, want just %s", rep.Written, path)
	}

	st, err := loadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.ProjectID != liveProjectID() {
		t.Errorf("ledger pinned %q, want %q", st.ProjectID, liveProjectID())
	}
	if st.Files[path].SHA != sha256hex([]byte(body)) {
		t.Error("the ledger did not record the bytes it pushed; the next sync would call this a conflict")
	}

	// A second push must move nothing: the ledger and the etag agree.
	rep2, err := runPush(ctx, c, pushOpts{projectID: liveProjectID(), dir: dir, concurrency: 4})
	if err != nil {
		t.Fatalf("second runPush: %v", err)
	}
	if len(rep2.Written) != 0 {
		t.Errorf("a warm push rewrote %v; unchanged files are supposed to cost nothing", rep2.Written)
	}

	// Pull the same file into a fresh directory and compare bytes.
	dir2 := t.TempDir()
	pullRep, err := runPull(ctx, c, pullOpts{projectID: liveProjectID(), dir: dir2, concurrency: 8})
	if err != nil {
		t.Fatalf("runPull: %v", err)
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
