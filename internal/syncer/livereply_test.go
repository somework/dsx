//go:build live

package syncer

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/mcp"
	"github.com/somework/dsx/internal/reply"
)

// These are the plumbing half only, deliberately. The judgment — "is this
// reply the shape dsx renders" — is `internal/reply`'s decoders, which carry
// no build tag and are table-tested against the captured bytes by an ordinary
// `go test`. That is the same split `etagVerdict` and `roundTripVerdict` were
// bought with, arrived at from the other side: here the verdict function had
// to exist anyway, because a renderer that believes one shape and a PROTOCOL
// claim that states another is exactly the drift this suite exists to catch.
//
// So a mutation inside this file can only break which endpoint is called or
// which argument is sent. That residue is irreducible without the network and
// is the whole reason these tests are here.

func TestLiveDesignSystemsIsABareArrayOfIDNameDefault(t *testing.T) {
	c, ctx := liveClient(t)

	text, err := c.CallTool(ctx, "list_design_systems", map[string]any{})
	if err != nil {
		t.Fatalf("list_design_systems: %v", err)
	}
	rows, ok := reply.DecodeDesignSystems(text)
	if !ok {
		t.Fatalf("PROTOCOL.md's list_design_systems shape no longer matches:\n%s", text)
	}
	if len(rows) == 0 {
		t.Skip("this account has no design systems; the element claims went untested")
	}
	// The id width and the presence of a name are DecodeDesignSystems' own
	// refusals now, pinned by an ordinary `go test`. What is left here is the
	// one claim only a real account can answer: PROTOCOL.md says the default is
	// the one a fresh project would use, which is a claim that exactly one row
	// carries it.
	defaults := 0
	for _, r := range rows {
		if r.IsDefault {
			defaults++
		}
	}
	if defaults != 1 {
		t.Errorf("%d design systems marked default, want exactly 1", defaults)
	}
}

func TestLiveGetProjectCarriesNameAndSharing(t *testing.T) {
	c, ctx := liveClient(t)

	text, err := c.CallTool(ctx, "get_project", map[string]any{"project_id": liveProjectID()})
	if err != nil {
		t.Fatalf("get_project: %v", err)
	}
	p, ok := reply.DecodeProject(text)
	if !ok {
		t.Fatalf("PROTOCOL.md's get_project shape no longer matches:\n%s", text)
	}
	// name, type and sharing.scope are DecodeProject's own refusals, so the
	// decode above already asserted them under a bare `go test`. Only the
	// identity claim needs the network.
	if p.ID != liveProjectID() {
		t.Errorf("get_project(%s) answered for %s", liveProjectID(), p.ID)
	}
	if p.Type != "PROJECT_TYPE_PROJECT" {
		t.Errorf("type = %q; PROTOCOL.md spells the enum PROJECT_TYPE_PROJECT", p.Type)
	}
}

// The empty case is the only one dsx renders, and it is the one this account
// can reach: list_members excludes the owner, so a project with no invited
// teammate answers with an empty array. If that ever stops being true here the
// renderer must learn the element, and this is where it will be noticed.
func TestLiveListMembersIsABareArray(t *testing.T) {
	c, ctx := liveClient(t)

	text, err := c.CallTool(ctx, "list_members", map[string]any{"project_id": liveProjectID()})
	if err != nil {
		t.Fatalf("list_members: %v", err)
	}
	// Asserted directly, not through reply.Members: that bool is overloaded —
	// false means "not a bare array" AND "a non-empty one", and only the first
	// is a protocol claim failing. A wrapper object would otherwise arrive as
	// the anticipated, logged case.
	var rows []json.RawMessage
	if err := json.Unmarshal([]byte(text), &rows); err != nil || rows == nil {
		t.Fatalf("PROTOCOL.md says list_members is a bare array:\n%s", text)
	}
	if len(rows) > 0 {
		t.Logf("list_members is no longer empty for this project; dsx renders only the empty case:\n%s", text)
	}
}

// One scratch path through write, copy and delete, checking each reply against
// the decoder that renders it. The file count is asserted back where it
// started, as every mutating test here does.
func TestLiveWriteCopyDeleteRepliesStillMatchTheirRenderers(t *testing.T) {
	c, ctx := liveClient(t)
	before := len(liveTree(t, c, ctx))

	// liveScratch, not a hand-built path: it registers the removal BEFORE the
	// first write, so a failure between the write and the delete still cleans
	// up. Building the path by hand made these two the only mutating live tests
	// in the package that leaked their scratch file on any early t.Fatalf — and
	// there is no delete_project, so a leak is permanent.
	src := liveScratch(t, c, ctx, "-reply.css")
	dst := liveScratch(t, c, ctx, "-reply-copy.css")

	text, err := c.CallTool(ctx, "write_files", map[string]any{
		"project_id": liveProjectID(),
		"files": []any{map[string]any{
			"path": src, "data": base64.StdEncoding.EncodeToString([]byte("a{}\n")), "encoding": "base64",
		}},
	})
	if err != nil {
		t.Fatalf("write_files: %v", err)
	}
	if _, ok := reply.DecodeWritten(text); !ok {
		t.Errorf("PROTOCOL.md's write_files shape no longer matches:\n%s", text)
	}

	text, err = c.CallTool(ctx, "copy_files", map[string]any{
		"project_id": liveProjectID(),
		"files":      []any{map[string]any{"src": src, "dest": dst}},
	})
	if err != nil {
		t.Fatalf("copy_files: %v", err)
	}
	if _, ok := reply.DecodeCopied(text); !ok {
		t.Errorf("PROTOCOL.md's copy_files shape no longer matches:\n%s", text)
	}

	token, err := PlanToken(ctx, c, map[string]any{
		"project_id": liveProjectID(), "deletes": []string{src, dst},
	})
	if err != nil {
		t.Fatalf("finalize_plan: %v", err)
	}
	text, err = c.CallTool(ctx, "delete_files", map[string]any{
		"project_id": liveProjectID(), "plan_token": token, "paths": []string{src, dst},
	})
	if err != nil {
		t.Fatalf("delete_files: %v", err)
	}
	n, ok := reply.DecodeDeleted(text)
	if !ok {
		t.Fatalf("PROTOCOL.md's delete_files shape no longer matches:\n%s", text)
	}
	if n != 2 {
		t.Errorf("deleted = %d, want 2", n)
	}

	if after := len(liveTree(t, c, ctx)); after != before {
		t.Errorf("file count %d → %d; the scratch paths were not cleaned up", before, after)
	}
}

// create_support_js is deliberately checked against its own decoder rather
// than write_files': the reply carries `bytes` and `path` and no `written` at
// all, and reading one as the other reports a write of nothing.
func TestLiveSupportJSReplyIsNotAWriteFilesReply(t *testing.T) {
	c, ctx := liveClient(t)
	before := len(liveTree(t, c, ctx))

	path := liveScratch(t, c, ctx, "-reply/support.js")
	text, err := c.CallTool(ctx, "create_support_js", map[string]any{
		"project_id": liveProjectID(), "path": path,
	})
	if err != nil {
		t.Fatalf("create_support_js: %v", err)
	}
	s, ok := reply.DecodeSupportJS(text)
	if !ok {
		t.Fatalf("PROTOCOL.md's create_support_js shape no longer matches:\n%s", text)
	}
	// bytes and the etag key are DecodeSupportJS' own refusals. What only the
	// network answers is that the reply echoes the path that was asked for.
	if s.Path != path {
		t.Errorf("asked for %q, reply names %q", path, s.Path)
	}
	if _, ok := reply.DecodeWritten(text); ok {
		t.Error("a create_support_js reply decoded as a write_files reply; " +
			"the two shapes are supposed to be distinguishable")
	}

	token, err := PlanToken(ctx, c, map[string]any{
		"project_id": liveProjectID(), "deletes": []string{path},
	})
	if err != nil {
		t.Fatalf("finalize_plan: %v", err)
	}
	if _, err := c.CallTool(ctx, "delete_files", map[string]any{
		"project_id": liveProjectID(), "plan_token": token, "paths": []string{path},
	}); err != nil {
		t.Fatalf("delete_files: %v", err)
	}
	if after := len(liveTree(t, c, ctx)); after != before {
		t.Errorf("file count %d → %d; the scratch path was not cleaned up", before, after)
	}
}

// list_files is the one shape with two readers: syncer decodes it into
// RemoteEntry to sync, and reply.DecodeFiles decodes it to render `files ls`.
// TestLiveListFilesShape judges only the first, which left the seventh
// renderer the only one whose belief no live test touched.
func TestLiveListFilesAlsoSatisfiesTheRenderersDecoder(t *testing.T) {
	c, ctx := liveClient(t)

	text, err := c.CallTool(ctx, "list_files", map[string]any{
		"project_id": liveProjectID(), "path": "",
	})
	if err != nil {
		t.Fatalf("list_files: %v", err)
	}
	rows, ok := reply.DecodeFiles(text)
	if !ok {
		t.Fatalf("PROTOCOL.md's list_files shape no longer matches what `files ls` renders:\n%s", text)
	}
	if len(rows) == 0 {
		t.Fatal("the project root listed empty; this would prove nothing")
	}
}

// get_conversation's own blurb says the reply is wrapped "the same way
// read_file wraps file content". It is not, and dsx believed it for as long as
// CLAUDE.md carried that sentence as a known unknown. Both halves are asserted
// here because the interesting claim is a NEGATIVE — read_file's parser must
// refuse this framing — and a negative asserted against a mock proves only that
// the mock was built to fail.
func TestLiveConversationIsNotWrappedLikeReadFile(t *testing.T) {
	c, ctx := liveClient(t)

	text, err := c.CallTool(ctx, "get_conversation", map[string]any{"project_id": liveProjectID()})
	if err != nil {
		t.Fatalf("get_conversation: %v", err)
	}
	conv, ok := reply.DecodeConversation(text)
	if !ok {
		t.Fatalf("PROTOCOL.md's get_conversation framing no longer matches:\n%s", firstLine(text))
	}
	if conv.ProjectID != liveProjectID() {
		t.Errorf("wrapper names project %q, asked for %q", conv.ProjectID, liveProjectID())
	}
	// The tag carries no etag, so read_file's reassembly parser cannot read it
	// — which is exactly why `conv get` passes the body through instead of
	// running it through ParseEnvelope.
	if _, err := mcp.ParseEnvelope(text); err == nil {
		t.Error("ParseEnvelope accepted a get_conversation reply; if the framing has " +
			"gained an etag, conv should reassemble rather than pass through")
	}

	// The sandbox transcript is far under the cap, so the truncated wordings
	// cannot be reached from here: put_conversation would have to write a
	// quarter of a megabyte of chat and there is no tool to remove a chat
	// again. Point DSX_LIVE_PROJECT at a project that exceeds the cap to cover
	// the other half.
	if !conv.Truncated {
		t.Logf("this project's transcript is under the 256 KiB cap; the truncation " +
			"notice wordings went untested (set DSX_LIVE_PROJECT to a busier project)")
		return
	}
	if conv.Dropped <= 0 {
		t.Errorf("a truncated reply reported %d bytes dropped", conv.Dropped)
	}
	if out, ok := reply.Conversation(text); !ok {
		t.Error("a truncated reply decoded but did not render")
	} else if strings.Contains(out, "untrusted-project-content") {
		t.Errorf("the wire framing reached a person:\n%s", out)
	}
	for _, id := range conv.Chats {
		text, err := c.CallTool(ctx, "get_conversation", map[string]any{
			"project_id": liveProjectID(), "chat_id": id,
		})
		if err != nil {
			t.Errorf("the notice offered chat %q and it does not resolve: %v", id, err)
			continue
		}
		// The SECOND notice wording is reachable only from here. Narrowing to a
		// chat that is itself over the cap leaves no chat_id left to suggest,
		// and the server swaps `pass chat_id to narrow` for `this single chat
		// exceeds the cap`. Asserting only that the call succeeded — which is
		// all this loop used to do — leaves that wording measured by hand and
		// pinned by nothing.
		narrowed, ok := reply.DecodeConversation(text)
		if !ok {
			t.Errorf("a narrowed reply for %q no longer decodes:\n%s", id, firstLine(text))
			continue
		}
		if !narrowed.Truncated {
			t.Logf("chat %s fits under the cap; the single-chat wording went untested here", id)
			continue
		}
		if len(narrowed.Chats) != 0 {
			t.Errorf("a chat already narrowed to still offers %v to narrow to", narrowed.Chats)
		}
		out, ok := reply.Conversation(text)
		if !ok {
			t.Errorf("a truncated narrowed reply decoded but did not render")
			continue
		}
		if strings.Contains(out, "--chat") {
			t.Errorf("dsx offered --chat for a chat that is itself over the cap — "+
				"a loop with no exit:\n%s", out)
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// list_comments and read_design_skill reached the server without dsx noticing,
// because reference/mcp-tools.json had never heard of them and every offline
// guard judges against it. These pin the shapes now that they are wrapped.
func TestLiveCommentsCarryAWatermark(t *testing.T) {
	c, ctx := liveClient(t)

	text, err := c.CallTool(ctx, "list_comments", map[string]any{"project_id": liveProjectID()})
	if err != nil {
		t.Fatalf("list_comments: %v", err)
	}
	r, ok := reply.DecodeComments(text)
	if !ok {
		t.Fatalf("PROTOCOL.md's list_comments shape no longer matches:\n%s", text)
	}
	if len(r.Comments) > 0 {
		t.Logf("this project now has comments; dsx renders only the empty case:\n%s", text)
		return
	}
	// The watermark is the claim only the network can answer: the server must
	// accept its own server_time back verbatim, which is the whole incremental
	// contract and the thing get_conversation has no equivalent of.
	again, err := c.CallTool(ctx, "list_comments", map[string]any{
		"project_id": liveProjectID(), "changed_since": r.ServerTime,
	})
	if err != nil {
		t.Fatalf("the server refused its own server_time as changed_since: %v", err)
	}
	if _, ok := reply.DecodeComments(again); !ok {
		t.Errorf("a watermarked listing is not the same shape:\n%s", again)
	}
}

// ack_comments is probed with a well-formed but nonexistent id on purpose: a
// real queued id belongs to a person waiting for an answer, and clearing that
// flag is not something a test may do.
func TestLiveAckOfANonexistentCommentIsNotAnError(t *testing.T) {
	c, ctx := liveClient(t)

	const absent = "00000000-0000-4000-8000-000000000000"
	text, err := c.CallTool(ctx, "ack_comments", map[string]any{
		"project_id": liveProjectID(), "comment_ids": []string{absent},
	})
	if err != nil {
		t.Fatalf("ack_comments: %v", err)
	}
	r, ok := reply.DecodeAcked(text)
	if !ok {
		t.Fatalf("PROTOCOL.md's ack_comments shape no longer matches:\n%s", text)
	}
	if len(r.Acked) != 0 {
		t.Errorf("acked %v — an id that was never queued must not be reported as handled", r.Acked)
	}
	if len(r.NotQueued) != 1 || r.NotQueued[0] != absent {
		t.Errorf("not_queued = %v, want the id back", r.NotQueued)
	}
}

func TestLiveDesignSkillsAreProseAndTheNamesAreClosed(t *testing.T) {
	c, ctx := liveClient(t)

	for _, name := range []string{"hifi-design", "frontend-design"} {
		text, err := c.CallTool(ctx, "read_design_skill", map[string]any{"skill": name})
		if err != nil {
			t.Fatalf("read_design_skill %s: %v", name, err)
		}
		// Prose, not JSON — which is why `dsx skill` carries no renderer. If this
		// ever becomes a JSON document the indented fallback would start
		// reformatting guidance meant to be read.
		if json.Valid([]byte(text)) {
			t.Errorf("%s now answers in JSON; `dsx skill` assumes prose", name)
		}
		if len(text) == 0 {
			t.Errorf("%s answered empty", name)
		}
	}
	if _, err := c.CallTool(ctx, "read_design_skill", map[string]any{"skill": "dsx-selftest-nope"}); err == nil {
		t.Error("an unknown skill was accepted; dsx relies on the server's refusal " +
			"rather than a local enum that would go stale")
	}
}

// The one claim that matters about `depth: -1`, and the only one a mock cannot
// make: that the flat listing and the recursive walk describe the SAME tree.
// Every sync verb reads that map and `--prune` deletes what is missing from it,
// so a flat listing quietly one file short is not a performance regression, it
// is deletion of the user's work. A fake can only repeat what dsx already
// believes; this compares the server against itself.
func TestLiveTheFlatListingAgreesWithTheRecursiveWalk(t *testing.T) {
	c, ctx := liveClient(t)

	flat, ok := walkFlat(ctx, c, liveProjectID())
	if !ok {
		t.Skip("this project's flat listing was declined; nothing to compare")
	}
	walked, err := walkRecursive(ctx, c, liveProjectID(), 8)
	if err != nil {
		t.Fatalf("walkRecursive: %v", err)
	}

	if len(flat) != len(walked) {
		t.Errorf("flat listing holds %d files, the walk %d", len(flat), len(walked))
	}
	for p, w := range walked {
		f, ok := flat[p]
		if !ok {
			t.Errorf("%s is in the walk and missing from the flat listing — "+
				"push --prune would read it as a user deletion", p)
			continue
		}
		// Size and etag are what planPull/planPush decide on, so agreeing on
		// the path alone would not be agreement.
		if f.Size != w.Size || f.Etag != w.Etag {
			t.Errorf("%s: flat %d/%s vs walk %d/%s", p, f.Size, f.Etag, w.Size, w.Etag)
		}
	}
	for p := range flat {
		if _, ok := walked[p]; !ok {
			t.Errorf("%s is in the flat listing and not in the walk", p)
		}
	}
	if len(walked) == 0 {
		t.Fatal("the project listed empty; this would agree vacuously")
	}
	t.Logf("%d files agreed", len(walked))
}

// PROTOCOL.md claims create_support_js refuses any basename but support.js.
// The claim arrived with the reply-shape work and was the only one in that
// batch with no test, which is the shape of every protocol claim that later
// turned out to be wrong.
func TestLiveSupportJSRefusesAnyOtherBasename(t *testing.T) {
	c, ctx := liveClient(t)

	_, err := c.CallTool(ctx, "create_support_js", map[string]any{
		"project_id": liveProjectID(), "path": scratchPrefix + "-not-support.js",
	})
	if err == nil {
		t.Fatal("a non-support.js basename was accepted; PROTOCOL.md says it is refused " +
			"— and a file was just written that this test does not clean up")
	}
	if !strings.Contains(err.Error(), "support.js") {
		t.Errorf("refusal does not name the required basename: %v", err)
	}
}
