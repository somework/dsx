//go:build live

package syncer

import (
	"encoding/base64"
	"testing"

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
	for _, r := range rows {
		if len(r.ID) != 36 {
			t.Errorf("id %q is not a 36-char UUID", r.ID)
		}
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
	if p.ID != liveProjectID() {
		t.Errorf("get_project(%s) answered for %s", liveProjectID(), p.ID)
	}
	if p.Name == "" {
		t.Error("no name; PROTOCOL.md says get_project carries one")
	}
	if p.Sharing.Scope == "" {
		t.Error("no sharing.scope; PROTOCOL.md says the sharing block is there")
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
	if _, ok := reply.Members(text); !ok {
		t.Logf("list_members is no longer empty for this project; dsx renders only the empty case:\n%s", text)
	}
}

// One scratch path through write, copy and delete, checking each reply against
// the decoder that renders it. The file count is asserted back where it
// started, as every mutating test here does.
func TestLiveWriteCopyDeleteRepliesStillMatchTheirRenderers(t *testing.T) {
	c, ctx := liveClient(t)
	before := len(liveTree(t, c, ctx))

	src := scratchPrefix + "-reply.css"
	dst := scratchPrefix + "-reply-copy.css"

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

	path := scratchPrefix + "-reply/support.js"
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
	if s.Bytes == 0 {
		t.Error("no bytes count; PROTOCOL.md says the reply carries one")
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
