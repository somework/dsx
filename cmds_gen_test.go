package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Tests for cmds.go -- the thin per-tool wrappers.
//
// These wrappers exist to spell arguments out, so the thing worth pinning is
// exactly that: for a given command line, WHICH tool is called and with WHICH
// arguments. Asserting that output is non-empty would pass against almost any
// mutation of this file; asserting the recorded call would not.
//
// Nothing here is evidence about the server. The fake only repeats what dsx
// already believes; PROTOCOL.md and the live suite own protocol truth.

// cmdsNormalize round-trips a value through JSON so an expectation written in
// Go types (int, []string) compares equal to what the fake decoded off the wire
// (float64, []any). Without it every expectation would have to be spelled in
// wire types, which reads nothing like the call site under test.
func cmdsNormalize(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	return out
}

// cmdsWantArgs asserts the recorded arguments map equals want exactly -- every
// key present, and no key beyond them. Exactness is the point: an optional flag
// that leaks through as "" is a different request from one that was omitted.
func cmdsWantArgs(t *testing.T, got map[string]any, want map[string]any) {
	t.Helper()
	if !reflect.DeepEqual(any(got), cmdsNormalize(t, want)) {
		t.Errorf("arguments mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

// cmdsToolCalls returns just the tools/call traffic, in order.
func cmdsToolCalls(f *fakeMCP) []recordedCall {
	var out []recordedCall
	for _, c := range f.recorded() {
		if c.Method == "tools/call" {
			out = append(out, c)
		}
	}
	return out
}

// cmdsOnlyCall asserts exactly one tool was called and returns it.
func cmdsOnlyCall(t *testing.T, f *fakeMCP) recordedCall {
	t.Helper()
	calls := cmdsToolCalls(f)
	if len(calls) != 1 {
		t.Fatalf("want exactly 1 tool call, got %d: %#v", len(calls), calls)
	}
	return calls[0]
}

// cmdsReplyJSON answers every tool with one fixed JSON document.
func cmdsReplyJSON(text string) func(string, map[string]any) fakeReply {
	return func(string, map[string]any) fakeReply { return fakeReply{Text: text} }
}

// cmdsTempFile writes content to a throwaway file and returns its path.
func cmdsTempFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

type cmdsFn func(context.Context, *client, []string) error

// cmdsRun runs a command against a fake server, capturing stdout so the test
// log stays readable. Callers must not be parallel: captureStdout swaps
// os.Stdout, which is process-global.
func cmdsRun(t *testing.T, f *fakeMCP, fn cmdsFn, argv ...string) (string, error) {
	t.Helper()
	return captureStdout(t, func() error {
		return fn(context.Background(), f.client(), argv)
	})
}

// ---------------------------------------------------------------------------
// Which tool, with which arguments.
// ---------------------------------------------------------------------------

// TestCmdsCallTheRightToolWithExactlyTheRightArguments is the regression net
// for the whole file. Each case pins one command line onto one tool call.
//
// The wantArgs are exact, which is what catches the failure mode this file is
// most exposed to: an optional flag sent as "" rather than omitted. The server
// is undocumented and an empty if_match is not the same request as no if_match
// -- "0" already means "assert this path does not exist".
func TestCmdsCallTheRightToolWithExactlyTheRightArguments(t *testing.T) {
	cases := []struct {
		name     string
		fn       cmdsFn
		argv     []string
		wantTool string
		wantArgs map[string]any
	}{
		{
			name: "new without --ds omits design_system_id",
			fn:   cmdNew, argv: []string{"My Project"},
			wantTool: "create_project",
			wantArgs: map[string]any{"name": "My Project"},
		},
		{
			name: "new with --ds",
			fn:   cmdNew, argv: []string{"My Project", "--ds", "ds-1"},
			wantTool: "create_project",
			wantArgs: map[string]any{"name": "My Project", "design_system_id": "ds-1"},
		},
		{
			name: "ls without a path omits path",
			fn:   cmdLs, argv: []string{"p1"},
			wantTool: "list_files",
			wantArgs: map[string]any{"project_id": "p1"},
		},
		{
			name: "ls with a path",
			fn:   cmdLs, argv: []string{"p1", "src/components"},
			wantTool: "list_files",
			wantArgs: map[string]any{"project_id": "p1", "path": "src/components"},
		},
		{
			name: "cp within one project omits src_project_id",
			fn:   cmdCp, argv: []string{"p1", "a.css", "b.css"},
			wantTool: "copy_files",
			wantArgs: map[string]any{
				"project_id": "p1",
				"files":      []any{map[string]any{"src": "a.css", "dest": "b.css"}},
			},
		},
		{
			name: "cp across projects carries src_project_id in the file entry",
			fn:   cmdCp, argv: []string{"p1", "a.css", "b.css", "--from", "p0"},
			wantTool: "copy_files",
			wantArgs: map[string]any{
				"project_id": "p1",
				"files": []any{map[string]any{
					"src": "a.css", "dest": "b.css", "src_project_id": "p0",
				}},
			},
		},
		{
			name: "cp puts if-match in the file entry but plan at the top level",
			fn:   cmdCp, argv: []string{"p1", "a.css", "b.css", "--if-match", "e1", "--plan", "tok"},
			wantTool: "copy_files",
			wantArgs: map[string]any{
				"project_id": "p1",
				"plan_token": "tok",
				"files": []any{map[string]any{
					"src": "a.css", "dest": "b.css", "if_match": "e1",
				}},
			},
		},
		{
			name: "plan with neither list sends neither key",
			fn:   cmdPlan, argv: []string{"p1"},
			wantTool: "finalize_plan",
			wantArgs: map[string]any{"project_id": "p1"},
		},
		{
			name: "plan splits comma lists and drops blanks",
			fn:   cmdPlan, argv: []string{"p1", "--writes", "a.css, b.css,", "--deletes", "c.css"},
			wantTool: "finalize_plan",
			wantArgs: map[string]any{
				"project_id": "p1",
				"writes":     []any{"a.css", "b.css"},
				"deletes":    []any{"c.css"},
			},
		},
		{
			name: "plan --scope project",
			fn:   cmdPlan, argv: []string{"p1", "--scope", "project"},
			wantTool: "finalize_plan",
			wantArgs: map[string]any{"project_id": "p1", "scope": "project"},
		},
		{
			name: "preview without --render omits render",
			fn:   cmdPreview, argv: []string{"p1", "index.html"},
			wantTool: "render_preview",
			wantArgs: map[string]any{"project_id": "p1", "path": "index.html"},
		},
		{
			name: "preview with --render and validators",
			fn:   cmdPreview, argv: []string{"p1", "index.html", "--render", "--validators", "a11y,css"},
			wantTool: "render_preview",
			wantArgs: map[string]any{
				"project_id": "p1", "path": "index.html",
				"render": true, "validators": []any{"a11y", "css"},
			},
		},
		{
			name: "support-js sends only the project when nothing else is set",
			fn:   cmdSupportJS, argv: []string{"p1"},
			wantTool: "create_support_js",
			wantArgs: map[string]any{"project_id": "p1"},
		},
		{
			name: "support-js with every optional argument",
			fn:   cmdSupportJS, argv: []string{"p1", "--path", "s.js", "--if-match", "e1", "--plan", "tok"},
			wantTool: "create_support_js",
			wantArgs: map[string]any{
				"project_id": "p1", "path": "s.js", "if_match": "e1", "plan_token": "tok",
			},
		},
		{
			name: "conv without --chat omits chat_id",
			fn:   cmdConv, argv: []string{"p1"},
			wantTool: "get_conversation",
			wantArgs: map[string]any{"project_id": "p1"},
		},
		{
			name: "conv with --chat",
			fn:   cmdConv, argv: []string{"p1", "--chat", "c1"},
			wantTool: "get_conversation",
			wantArgs: map[string]any{"project_id": "p1", "chat_id": "c1"},
		},
		{
			name: "member-add by email",
			fn:   cmdMemberAdd, argv: []string{"p1", "--role", "editor", "--email", "a@b.c"},
			wantTool: "add_member",
			wantArgs: map[string]any{"project_id": "p1", "role": "editor", "email": "a@b.c"},
		},
		{
			name: "member-add by uuid",
			fn:   cmdMemberAdd, argv: []string{"p1", "--role", "viewer", "--uuid", "u1"},
			wantTool: "add_member",
			wantArgs: map[string]any{"project_id": "p1", "role": "viewer", "account_uuid": "u1"},
		},
		{
			name: "member-rm",
			fn:   cmdMemberRm, argv: []string{"p1", "u1"},
			wantTool: "remove_member",
			wantArgs: map[string]any{"project_id": "p1", "account_uuid": "u1"},
		},
		{
			name: "member-role takes the role positionally",
			fn:   cmdMemberRole, argv: []string{"p1", "u1", "viewer"},
			wantTool: "update_member_role",
			wantArgs: map[string]any{"project_id": "p1", "account_uuid": "u1", "role": "viewer"},
		},
		{
			name: "sharing with no options sends only the project",
			fn:   cmdSharing, argv: []string{"p1"},
			wantTool: "update_sharing",
			wantArgs: map[string]any{"project_id": "p1"},
		},
		{
			name: "sharing with both options",
			fn:   cmdSharing, argv: []string{"p1", "--scope", "org", "--link-permission", "read"},
			wantTool: "update_sharing",
			wantArgs: map[string]any{"project_id": "p1", "scope": "org", "link_permission": "read"},
		},
		{
			name: "prompt with no flags sends an empty argument map",
			fn:   cmdPrompt, argv: []string{},
			wantTool: "get_claude_design_prompt",
			wantArgs: map[string]any{},
		},
		{
			name: "prompt with both flags",
			fn:   cmdPrompt, argv: []string{"--project", "p1", "--ds", "ds-1"},
			wantTool: "get_claude_design_prompt",
			wantArgs: map[string]any{"project_id": "p1", "design_system_id": "ds-1"},
		},
		{
			name: "raw passes the tool name and parsed object straight through",
			fn:   cmdRaw, argv: []string{"some_undocumented_tool", `{"a":1,"b":["x"]}`},
			wantTool: "some_undocumented_tool",
			wantArgs: map[string]any{"a": 1, "b": []any{"x"}},
		},
		{
			name: "raw with no argument string sends an empty object",
			fn:   cmdRaw, argv: []string{"some_undocumented_tool"},
			wantTool: "some_undocumented_tool",
			wantArgs: map[string]any{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeMCP(t, cmdsReplyJSON(`{"ok":true}`))
			if _, err := cmdsRun(t, f, tc.fn, tc.argv...); err != nil {
				t.Fatalf("command failed: %v", err)
			}
			call := cmdsOnlyCall(t, f)
			if call.Tool != tc.wantTool {
				t.Errorf("tool = %q, want %q", call.Tool, tc.wantTool)
			}
			cmdsWantArgs(t, call.Args, tc.wantArgs)
		})
	}
}

// TestCmdPutSendsIfMatchOnlyWhenAskedAndNeverAsAnEmptyString pins the guard
// most likely to be quietly broken by a refactor.
//
// "" and "0" are not the same request: "0" asserts the path does not exist yet,
// so a wrapper that forwarded an unset flag as "" would turn every plain put
// into a create-only put, and every one of them would fail against an existing
// file.
func TestCmdPutSendsIfMatchOnlyWhenAskedAndNeverAsAnEmptyString(t *testing.T) {
	body := "h1 { color: red; }"
	src := cmdsTempFile(t, "a.css", body)

	cases := []struct {
		name        string
		argv        []string
		wantIfMatch any // nil means the key must be absent
	}{
		{"unset", []string{"p1", "a.css", src}, nil},
		{`"0" asserts the path is new`, []string{"p1", "a.css", src, "--if-match", "0"}, "0"},
		{"a real etag", []string{"p1", "a.css", src, "--if-match", "etag-7"}, "etag-7"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeMCP(t, cmdsReplyJSON(`{"written":{"a.css":"e2"}}`))
			if _, err := cmdsRun(t, f, cmdPut, tc.argv...); err != nil {
				t.Fatalf("put failed: %v", err)
			}
			call := cmdsOnlyCall(t, f)
			if call.Tool != "write_files" {
				t.Fatalf("tool = %q, want write_files", call.Tool)
			}

			files, ok := call.Args["files"].([]any)
			if !ok || len(files) != 1 {
				t.Fatalf("files = %#v, want one entry", call.Args["files"])
			}
			file, ok := files[0].(map[string]any)
			if !ok {
				t.Fatalf("file entry = %#v, want an object", files[0])
			}

			got, present := file["if_match"]
			switch {
			case tc.wantIfMatch == nil && present:
				t.Errorf("if_match present as %#v; an unset flag must be omitted, not sent empty", got)
			case tc.wantIfMatch != nil && !present:
				t.Errorf("if_match absent, want %#v", tc.wantIfMatch)
			case tc.wantIfMatch != nil && got != tc.wantIfMatch:
				t.Errorf("if_match = %#v, want %#v", got, tc.wantIfMatch)
			}
		})
	}
}

// TestCmdPutBase64sTheFileAndDeclaresTheEncoding pins the wire shape of a write:
// the bytes must arrive base64 with encoding declared, or the server stores the
// literal text of whatever we sent.
func TestCmdPutBase64sTheFileAndDeclaresTheEncoding(t *testing.T) {
	// Bytes that are not valid UTF-8, to prove nothing on the way is treating
	// the payload as text.
	body := "\x00\x01\xff\xfe binary-ish"
	src := cmdsTempFile(t, "blob.bin", body)

	f := newFakeMCP(t, cmdsReplyJSON(`{"written":{"blob.bin":"e1"}}`))
	if _, err := cmdsRun(t, f, cmdPut, "p1", "assets/blob.bin", src); err != nil {
		t.Fatalf("put failed: %v", err)
	}

	call := cmdsOnlyCall(t, f)
	file := call.Args["files"].([]any)[0].(map[string]any)

	if file["path"] != "assets/blob.bin" {
		t.Errorf("path = %#v, want the remote path, not the local one", file["path"])
	}
	if file["encoding"] != "base64" {
		t.Errorf("encoding = %#v, want base64", file["encoding"])
	}
	decoded, err := base64.StdEncoding.DecodeString(file["data"].(string))
	if err != nil {
		t.Fatalf("data was not base64: %v", err)
	}
	if string(decoded) != body {
		t.Errorf("data decoded to %q, want %q", decoded, body)
	}
}

// TestCmdPutOmitsPlanTokenWhenNotGiven -- a plan_token of "" is not a token,
// and sending one would ask the server to authorise a write against nothing.
func TestCmdPutOmitsPlanTokenWhenNotGiven(t *testing.T) {
	src := cmdsTempFile(t, "a.css", "x")

	f := newFakeMCP(t, cmdsReplyJSON(`{"written":{"a.css":"e1"}}`))
	if _, err := cmdsRun(t, f, cmdPut, "p1", "a.css", src); err != nil {
		t.Fatalf("put failed: %v", err)
	}
	if _, present := cmdsOnlyCall(t, f).Args["plan_token"]; present {
		t.Error("plan_token sent though --plan was never given")
	}

	f2 := newFakeMCP(t, cmdsReplyJSON(`{"written":{"a.css":"e1"}}`))
	if _, err := cmdsRun(t, f2, cmdPut, "p1", "a.css", src, "--plan", "tok-9"); err != nil {
		t.Fatalf("put failed: %v", err)
	}
	if got := cmdsOnlyCall(t, f2).Args["plan_token"]; got != "tok-9" {
		t.Errorf("plan_token = %#v, want tok-9", got)
	}
}

// ---------------------------------------------------------------------------
// rm: the plan_token dance.
// ---------------------------------------------------------------------------

// TestCmdRmMintsAPathScopedPlanTokenNamingEveryPathBeforeDeleting.
//
// Deletes are the one operation dsx cannot undo, and the server refuses a
// project-scoped token for them. Three things have to hold together and each is
// asserted separately: finalize_plan comes FIRST, it names every path being
// deleted (not a subset, and with no scope override), and the token it returned
// is the one delete_files carries.
func TestCmdRmMintsAPathScopedPlanTokenNamingEveryPathBeforeDeleting(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "finalize_plan":
			return fakeReply{Text: `{"plan_token":"tok-123"}`}
		case "delete_files":
			return fakeReply{Text: `{"deleted":["a.css","dir/b.css"]}`}
		}
		t.Errorf("rm called an unexpected tool %q", name)
		return fakeReply{IsError: true, Text: "unexpected"}
	})

	if _, err := cmdsRun(t, f, cmdRm, "p1", "a.css", "dir/b.css"); err != nil {
		t.Fatalf("rm failed: %v", err)
	}

	calls := cmdsToolCalls(f)
	if len(calls) != 2 {
		t.Fatalf("want finalize_plan then delete_files, got %d calls: %#v", len(calls), calls)
	}
	if calls[0].Tool != "finalize_plan" {
		t.Fatalf("first call was %q; the token must be minted before anything is deleted", calls[0].Tool)
	}
	if calls[1].Tool != "delete_files" {
		t.Fatalf("second call = %q, want delete_files", calls[1].Tool)
	}

	// Path-scoped: every path named, and no scope key asking for a project token.
	cmdsWantArgs(t, calls[0].Args, map[string]any{
		"project_id": "p1",
		"deletes":    []any{"a.css", "dir/b.css"},
	})
	// The token that came back is the token that goes out.
	cmdsWantArgs(t, calls[1].Args, map[string]any{
		"project_id": "p1",
		"plan_token": "tok-123",
		"paths":      []any{"a.css", "dir/b.css"},
	})
}

// TestCmdRmDeletesNothingWhenFinalizePlanReturnsNoToken.
//
// The guard in planToken is the only thing standing between a malformed plan
// reply and a delete_files carrying plan_token "". Asserting the error alone
// would not catch a version that reported the failure *after* calling the tool.
func TestCmdRmDeletesNothingWhenFinalizePlanReturnsNoToken(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "finalize_plan" {
			return fakeReply{Text: `{"status":"ok"}`} // well-formed JSON, no token
		}
		return fakeReply{Text: `{"deleted":[]}`}
	})

	_, err := cmdsRun(t, f, cmdRm, "p1", "a.css")
	if err == nil {
		t.Fatal("rm succeeded though it never got a plan_token")
	}
	if got := classify(err).Kind; got != kindProtocol {
		t.Errorf("kind = %q, want %q", got, kindProtocol)
	}
	if n := f.countTool("delete_files"); n != 0 {
		t.Errorf("delete_files called %d times without a token; nothing may be deleted unauthorised", n)
	}
}

// TestCmdRmWithNoPathsTouchesNoNetwork -- a doomed invocation must not mint a
// plan token. Exit 2 tells an agent not to retry; a token minted on the way to
// that answer is a side effect of a command that did nothing.
func TestCmdRmWithNoPathsTouchesNoNetwork(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		t.Errorf("rm with no paths called %q", name)
		return fakeReply{IsError: true}
	})

	_, err := cmdsRun(t, f, cmdRm, "p1")
	if got := classify(err).Kind; got != kindUsage {
		t.Errorf("kind = %q, want %q", got, kindUsage)
	}
	if n := len(f.recorded()); n != 0 {
		t.Errorf("%d requests made for an invocation that could never work", n)
	}
}

// ---------------------------------------------------------------------------
// planToken -- shared by `dsx rm` and push's delete path.
// ---------------------------------------------------------------------------

// TestPlanTokenClassifiesAnUnusableReplyAsProtocolNotTransport.
//
// The classification is the contract, not the message. kindProtocol means the
// server said something dsx cannot use, and repeating the request will produce
// the same answer; kindTransport (exit 4) would invite an agent to retry a call
// that is guaranteed to fail again, and each retry mints plan state server-side.
func TestPlanTokenClassifiesAnUnusableReplyAsProtocolNotTransport(t *testing.T) {
	cases := []struct {
		name  string
		reply string
	}{
		{"no plan_token field", `{"status":"ok"}`},
		{"empty plan_token", `{"plan_token":""}`},
		{"not JSON at all", `plan finalised, you're good to go`},
		{"JSON but not an object", `["tok"]`},
		{"empty reply", ``},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeMCP(t, cmdsReplyJSON(tc.reply))
			_, err := planToken(context.Background(), f.client(),
				map[string]any{"project_id": "p1", "deletes": []string{"a.css"}})
			if err == nil {
				t.Fatal("planToken accepted a reply carrying no usable token")
			}
			de := classify(err)
			if de.Kind != kindProtocol {
				t.Errorf("kind = %q, want %q", de.Kind, kindProtocol)
			}
			if code := exitCodeFor(err); code == exitTransport {
				t.Errorf("exit code = %d (transport); a caller must not retry this", code)
			}
		})
	}
}

// TestPlanTokenReturnsTheTokenAndForwardsTheRequestVerbatim.
func TestPlanTokenReturnsTheTokenAndForwardsTheRequestVerbatim(t *testing.T) {
	f := newFakeMCP(t, cmdsReplyJSON(`{"plan_token":"tok-abc","expires_in":900}`))

	got, err := planToken(context.Background(), f.client(),
		map[string]any{"project_id": "p1", "writes": []string{"a.css", "b.css"}})
	if err != nil {
		t.Fatalf("planToken: %v", err)
	}
	if got != "tok-abc" {
		t.Errorf("token = %q, want tok-abc", got)
	}

	call := cmdsOnlyCall(t, f)
	if call.Tool != "finalize_plan" {
		t.Errorf("tool = %q, want finalize_plan", call.Tool)
	}
	cmdsWantArgs(t, call.Args, map[string]any{
		"project_id": "p1",
		"writes":     []any{"a.css", "b.css"},
	})
}

// TestPlanTokenKeepsATransportFailureRetryable -- the opposite guard to the one
// above. A 5xx on the way to a token is worth retrying, and flattening every
// finalize_plan failure to kindProtocol would lose that.
func TestPlanTokenKeepsATransportFailureRetryable(t *testing.T) {
	f := newFakeMCP(t, func(string, map[string]any) fakeReply {
		return fakeReply{HTTPStatus: 503, HTTPBody: "upstream unavailable"}
	})

	_, err := planToken(context.Background(), f.client(),
		map[string]any{"project_id": "p1", "deletes": []string{"a.css"}})
	if err == nil {
		t.Fatal("planToken reported success against a 503")
	}
	if got := classify(err).Kind; got != kindTransport {
		t.Errorf("kind = %q, want %q -- a 503 is not a protocol violation", got, kindTransport)
	}
}

// ---------------------------------------------------------------------------
// The --json contract.
// ---------------------------------------------------------------------------

// TestJSONOutputIsExactlyOneJSONDocument.
//
// Under --json stdout is promised to be one JSON document for every command. A
// guarantee with exceptions is not one an agent can use, so both lanes are
// pinned: a tool answering in JSON passes through untouched (no double
// wrapping), a tool answering in prose gets wrapped (no raw text handed to a
// parser).
func TestJSONOutputIsExactlyOneJSONDocument(t *testing.T) {
	const prose = "Design guidance:\n  use tokens, not hex.\nThat is all."
	const asJSON = `{"tools":["a"],"count":1}`

	cases := []struct {
		name string
		fn   cmdsFn
		argv []string
	}{
		{"ls", cmdLs, []string{"p1", "--json"}},
		{"new", cmdNew, []string{"n", "--json"}},
		{"prompt", cmdPrompt, []string{"--json"}},
		{"conv", cmdConv, []string{"p1", "--json"}},
		{"sharing", cmdSharing, []string{"p1", "--json"}},
		{"plan", cmdPlan, []string{"p1", "--json"}},
		{"preview", cmdPreview, []string{"p1", "i.html", "--json"}},
		{"member-rm", cmdMemberRm, []string{"p1", "u1", "--json"}},
		{"member-role", cmdMemberRole, []string{"p1", "u1", "viewer", "--json"}},
		{"raw", cmdRaw, []string{"any_tool", `{}`, "--json"}},
	}

	for _, tc := range cases {
		t.Run(tc.name+" wraps a prose reply", func(t *testing.T) {
			f := newFakeMCP(t, cmdsReplyJSON(prose))
			out, err := cmdsRun(t, f, tc.fn, tc.argv...)
			if err != nil {
				t.Fatalf("command failed: %v", err)
			}
			if !json.Valid([]byte(out)) {
				t.Fatalf("stdout is not valid JSON: %q", out)
			}
			var m map[string]string
			if err := json.Unmarshal([]byte(out), &m); err != nil {
				t.Fatalf("stdout did not parse as an object: %v (%q)", err, out)
			}
			if m["text"] != prose {
				t.Errorf("text = %q, want the prose verbatim %q", m["text"], prose)
			}
			// One document per line: the prose carried newlines, and a caller
			// reading stdout line-by-line must still get whole documents.
			if n := strings.Count(strings.TrimSuffix(out, "\n"), "\n"); n != 0 {
				t.Errorf("stdout spans %d extra lines; wrapped prose must escape its newlines", n)
			}
		})

		t.Run(tc.name+" passes a JSON reply through untouched", func(t *testing.T) {
			f := newFakeMCP(t, cmdsReplyJSON(asJSON))
			out, err := cmdsRun(t, f, tc.fn, tc.argv...)
			if err != nil {
				t.Fatalf("command failed: %v", err)
			}
			if !json.Valid([]byte(out)) {
				t.Fatalf("stdout is not valid JSON: %q", out)
			}
			if got := strings.TrimSuffix(out, "\n"); got != asJSON {
				t.Errorf("stdout = %q, want the reply verbatim %q (no re-wrapping)", got, asJSON)
			}
		})
	}
}

// TestCmdCatJSONPutsTheBodyInAJSONStringInsteadOfDumpingItOnStdout.
//
// A caller that asked for JSON is running a parser, and a CSS file is not one.
// The body here holds a quote and newlines, so a version that printed it raw
// would fail json.Valid rather than merely look untidy.
func TestCmdCatJSONPutsTheBodyInAJSONStringInsteadOfDumpingItOnStdout(t *testing.T) {
	body := "h1 {\n  content: \"a \\\" quote\";\n}\n"
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name != "read_file" {
			t.Errorf("cat called %q, want read_file", name)
		}
		return fakeReply{Text: envelopeFor("a.css", "etag-1", body)}
	})

	out, err := cmdsRun(t, f, cmdCat, "p1", "a.css", "--json")
	if err != nil {
		t.Fatalf("cat failed: %v", err)
	}
	if !json.Valid([]byte(out)) {
		t.Fatalf("stdout is not valid JSON: %q", out)
	}

	var m struct {
		Path    string `json:"path"`
		Etag    string `json:"etag"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("stdout did not parse: %v", err)
	}
	if m.Content != body {
		t.Errorf("content = %q, want %q", m.Content, body)
	}
	if m.Path != "a.css" || m.Etag != "etag-1" {
		t.Errorf("path/etag = %q/%q, want a.css/etag-1", m.Path, m.Etag)
	}
}

// TestCmdCatWithoutJSONWritesTheBodyVerbatimAndAddsNothing.
//
// cat is the one command whose stdout IS the file. Println instead of
// WriteString here would append a newline that the file never had, and a caller
// piping cat into a file would land a byte the server does not hold.
func TestCmdCatWithoutJSONWritesTheBodyVerbatimAndAddsNothing(t *testing.T) {
	body := "no trailing newline here"
	f := newFakeMCP(t, func(string, map[string]any) fakeReply {
		return fakeReply{Text: envelopeFor("a.css", "e1", body)}
	})

	out, err := cmdsRun(t, f, cmdCat, "p1", "a.css")
	if err != nil {
		t.Fatalf("cat failed: %v", err)
	}
	if out != body {
		t.Errorf("stdout = %q, want exactly %q", out, body)
	}
}

// TestCmdCatOutWritesTheFileAndKeepsTheBodyOffStdout.
func TestCmdCatOutWritesTheFileAndKeepsTheBodyOffStdout(t *testing.T) {
	body := "h1 { color: red; }\n"
	reply := func(string, map[string]any) fakeReply {
		return fakeReply{Text: envelopeFor("a.css", "e1", body)}
	}

	t.Run("without --json stdout stays silent", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "out.css")
		f := newFakeMCP(t, reply)

		out, err := cmdsRun(t, f, cmdCat, "p1", "a.css", "--out", dst)
		if err != nil {
			t.Fatalf("cat failed: %v", err)
		}
		if out != "" {
			t.Errorf("stdout = %q, want nothing: the bytes were asked for on disk", out)
		}
		got, err := os.ReadFile(dst)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != body {
			t.Errorf("file = %q, want %q", got, body)
		}
	})

	t.Run("with --json a receipt is printed but never the body", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "out.css")
		f := newFakeMCP(t, reply)

		out, err := cmdsRun(t, f, cmdCat, "p1", "a.css", "--out", dst, "--json")
		if err != nil {
			t.Fatalf("cat failed: %v", err)
		}
		if !json.Valid([]byte(out)) {
			t.Fatalf("stdout is not valid JSON: %q", out)
		}

		var m map[string]any
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatal(err)
		}
		// The receipt describes the write; it must not duplicate the payload.
		if _, present := m["content"]; present {
			t.Error("the body was printed as well as written; --out means the caller wants it on disk")
		}
		if m["bytes"] != float64(len(body)) {
			t.Errorf("bytes = %#v, want %d", m["bytes"], len(body))
		}
		if m["out"] != dst {
			t.Errorf("out = %#v, want %q", m["out"], dst)
		}

		got, err := os.ReadFile(dst)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != body {
			t.Errorf("file = %q, want %q", got, body)
		}
	})
}

// TestCmdCatOutReportsAFailedWriteInsteadOfFallingBackToStdout.
//
// If --out cannot be honoured the command failed. Printing the body to stdout
// as a consolation would look like success to a caller redirecting stdout
// elsewhere, and quietly put the file somewhere it never asked for.
func TestCmdCatOutReportsAFailedWriteInsteadOfFallingBackToStdout(t *testing.T) {
	f := newFakeMCP(t, func(string, map[string]any) fakeReply {
		return fakeReply{Text: envelopeFor("a.css", "e1", "h1 { color: red; }")}
	})

	// A path whose parent directory does not exist.
	dst := filepath.Join(t.TempDir(), "no-such-dir", "out.css")
	out, err := cmdsRun(t, f, cmdCat, "p1", "a.css", "--out", dst)
	if err == nil {
		t.Fatal("cat reported success though --out could not be written")
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
}

// ---------------------------------------------------------------------------
// tree.
// ---------------------------------------------------------------------------

// TestCmdTreeJSONListsEveryFileSortedAndOmitsDirectories.
func TestCmdTreeJSONListsEveryFileSortedAndOmitsDirectories(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch args["path"] {
		case nil: // the project root
			return fakeReply{Text: listingFor(
				fileEntry("z.css", "e-z", 3),
				dirEntry("src"),
			)}
		case "src":
			return fakeReply{Text: listingFor(
				fileEntry("src/b.css", "e-b", 2),
				fileEntry("src/a.css", "e-a", 1),
			)}
		}
		t.Errorf("unexpected listing of %#v", args["path"])
		return fakeReply{IsError: true}
	})

	out, err := cmdsRun(t, f, cmdTree, "p1", "--json")
	if err != nil {
		t.Fatalf("tree failed: %v", err)
	}

	var got []remoteEntry
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout did not parse as a listing: %v (%q)", err, out)
	}
	want := []remoteEntry{
		{Path: "src/a.css", Type: "file", Size: 1, Etag: "e-a"},
		{Path: "src/b.css", Type: "file", Size: 2, Etag: "e-b"},
		{Path: "z.css", Type: "file", Size: 3, Etag: "e-z"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tree =\n %#v\nwant\n %#v", got, want)
	}
}

// TestCmdTreeJSONOnAnEmptyProjectIsAnEmptyArrayNotNull -- a caller running a
// parser over `[]` iterates zero times; over `null` it has to special-case.
func TestCmdTreeJSONOnAnEmptyProjectIsAnEmptyArrayNotNull(t *testing.T) {
	f := newFakeMCP(t, cmdsReplyJSON(listingFor()))

	out, err := cmdsRun(t, f, cmdTree, "p1", "--json")
	if err != nil {
		t.Fatalf("tree failed: %v", err)
	}
	if got := strings.TrimSpace(out); got != "[]" {
		t.Errorf("stdout = %q, want []", got)
	}
}

// TestCmdTreeWithoutJSONSummarisesEveryFileOnce.
//
// The summary line is what a human reads to decide whether the tree is the one
// they expected, so the count and the byte total have to come from the files
// actually listed rather than from the last directory walked.
func TestCmdTreeWithoutJSONSummarisesEveryFileOnce(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch args["path"] {
		case nil:
			return fakeReply{Text: listingFor(fileEntry("z.css", "e-z", 1000), dirEntry("src"))}
		case "src":
			return fakeReply{Text: listingFor(fileEntry("src/a.css", "e-a", 24))}
		}
		t.Errorf("unexpected listing of %#v", args["path"])
		return fakeReply{IsError: true}
	})

	out, err := cmdsRun(t, f, cmdTree, "p1")
	if err != nil {
		t.Fatalf("tree failed: %v", err)
	}

	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want one per file plus a summary:\n%s", len(lines), out)
	}
	// Sorted, so the nested file comes first.
	if !strings.HasSuffix(lines[0], "src/a.css") || !strings.HasSuffix(lines[1], "z.css") {
		t.Errorf("rows are not sorted by path:\n%s", out)
	}
	if !strings.Contains(lines[0], "e-a") || !strings.Contains(lines[1], "e-z") {
		t.Errorf("etags are missing from the rows:\n%s", out)
	}
	// 1000 + 24 == 1024 == "1.0 KB": the total is summed, not taken from a row.
	if got := lines[2]; got != "2 files, 1.0 KB" {
		t.Errorf("summary = %q, want %q", got, "2 files, 1.0 KB")
	}
}

// TestCmdTreeReportsAListingFailureRatherThanPrintingAShortTree.
//
// A subdirectory that failed to list is not an empty subdirectory. Printing the
// files we did get, with exit 0, would hand a caller a tree that silently omits
// whatever lived under the failure -- and `dsx tree` is what a caller reaches
// for to decide what exists.
func TestCmdTreeReportsAListingFailureRatherThanPrintingAShortTree(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if args["path"] == nil {
			return fakeReply{Text: listingFor(fileEntry("z.css", "e-z", 1), dirEntry("src"))}
		}
		return fakeReply{IsError: true, Text: "permission denied"}
	})

	out, err := cmdsRun(t, f, cmdTree, "p1", "--json")
	if err == nil {
		t.Fatal("tree reported success though a directory never listed")
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing: a partial tree must not be printed", out)
	}
}

// TestCmdTreeClampsConcurrencyBelowOneToOne.
//
// Not cosmetic: walkTree sizes its semaphore from this number, and a zero-sized
// buffered channel is an unbuffered one, on which the first send blocks forever.
// Without the clamp `-j 0` hangs until the context dies rather than listing
// anything, so the deadline below is what makes the regression visible as a
// failure instead of a stall.
func TestCmdTreeClampsConcurrencyBelowOneToOne(t *testing.T) {
	f := newFakeMCP(t, cmdsReplyJSON(listingFor(fileEntry("a.css", "e1", 1))))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := captureStdout(t, func() error {
		return cmdTree(ctx, f.client(), []string{"p1", "-j", "0", "--json"})
	})
	if err != nil {
		t.Fatalf("tree -j 0 failed: %v", err)
	}

	var got []remoteEntry
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout did not parse: %v (%q)", err, out)
	}
	if len(got) != 1 || got[0].Path != "a.css" {
		t.Errorf("tree = %#v, want the one file", got)
	}
}

// ---------------------------------------------------------------------------
// tools.
// ---------------------------------------------------------------------------

// TestCmdToolsPrintsOneLinePerToolAndTruncatesAtTheFirstLine.
//
// The fake advertises a description carrying a newline, which is what the real
// tools/list does. One tool per line is the contract a caller greps; without
// firstLine a multi-line description silently becomes several rows.
func TestCmdToolsPrintsOneLinePerToolAndTruncatesAtTheFirstLine(t *testing.T) {
	f := newFakeMCP(t, cmdsReplyJSON(`{}`))

	out, err := cmdsRun(t, f, cmdTools)
	if err != nil {
		t.Fatalf("tools failed: %v", err)
	}

	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want one per advertised tool:\n%s", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "list_files") {
		t.Errorf("first line = %q, want it to start with the tool name", lines[0])
	}
	// "List files\nin a project" must not spill onto a row of its own.
	if strings.Contains(out, "in a project") {
		t.Errorf("the tail of a multi-line description reached stdout:\n%s", out)
	}
}

// TestCmdToolsJSONEmitsTheServersOwnListAsOneDocument.
func TestCmdToolsJSONEmitsTheServersOwnListAsOneDocument(t *testing.T) {
	for _, flag := range []string{"--json", "--schema"} {
		t.Run(flag, func(t *testing.T) {
			f := newFakeMCP(t, cmdsReplyJSON(`{}`))

			out, err := cmdsRun(t, f, cmdTools, flag)
			if err != nil {
				t.Fatalf("tools %s failed: %v", flag, err)
			}
			if !json.Valid([]byte(out)) {
				t.Fatalf("stdout is not valid JSON: %q", out)
			}
			var list struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			}
			if err := json.Unmarshal([]byte(out), &list); err != nil {
				t.Fatal(err)
			}
			if len(list.Tools) != 2 {
				t.Errorf("got %d tools, want the list passed through whole", len(list.Tools))
			}
		})
	}
}

// cmdTools' kindProtocol branch (a tools/list whose result is not a tool list)
// is not reachable here: the fake answers tools/list itself, ahead of the reply
// func, so no test can hand cmdTools a malformed list without a second harness.
// Left uncovered on purpose rather than grown a competing fake.

// ---------------------------------------------------------------------------
// conv-put.
// ---------------------------------------------------------------------------

// TestCmdConvPutSendsSyncedThroughIdxOnlyWhenGiven.
//
// Zero is a real value here -- "synced through message 0" -- so the sentinel has
// to be -1, not 0. A guard written as `if *through != 0` would drop exactly the
// case that means "nothing has been synced yet but I am telling you so".
func TestCmdConvPutSendsSyncedThroughIdxOnlyWhenGiven(t *testing.T) {
	msgs := cmdsTempFile(t, "m.json", `[{"role":"user","content":"hi"}]`)

	cases := []struct {
		name string
		argv []string
		want any // nil means the key must be absent
	}{
		{"unset", []string{"p1", "--messages", msgs}, nil},
		{"explicit zero", []string{"p1", "--messages", msgs, "--synced-through-idx", "0"}, float64(0)},
		{"explicit five", []string{"p1", "--messages", msgs, "--synced-through-idx", "5"}, float64(5)},
		{"negative is treated as unset", []string{"p1", "--messages", msgs, "--synced-through-idx", "-2"}, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeMCP(t, cmdsReplyJSON(`{"ok":true}`))
			if _, err := cmdsRun(t, f, cmdConvPut, tc.argv...); err != nil {
				t.Fatalf("conv-put failed: %v", err)
			}
			got, present := cmdsOnlyCall(t, f).Args["synced_through_idx"]
			switch {
			case tc.want == nil && present:
				t.Errorf("synced_through_idx sent as %#v though it was not asked for", got)
			case tc.want != nil && got != tc.want:
				t.Errorf("synced_through_idx = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestCmdConvPutForwardsTheMessagesArrayAndTheOptionalMetadata.
func TestCmdConvPutForwardsTheMessagesArrayAndTheOptionalMetadata(t *testing.T) {
	msgs := cmdsTempFile(t, "m.json", `[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"}]`)
	f := newFakeMCP(t, cmdsReplyJSON(`{"ok":true}`))

	if _, err := cmdsRun(t, f, cmdConvPut,
		"p1", "--messages", msgs, "--chat", "c1", "--title", "T", "--append"); err != nil {
		t.Fatalf("conv-put failed: %v", err)
	}

	call := cmdsOnlyCall(t, f)
	if call.Tool != "put_conversation" {
		t.Fatalf("tool = %q, want put_conversation", call.Tool)
	}
	cmdsWantArgs(t, call.Args, map[string]any{
		"project_id": "p1",
		"chat_id":    "c1",
		"title":      "T",
		"append":     true,
		"messages": []any{
			map[string]any{"role": "user", "content": "hi"},
			map[string]any{"role": "assistant", "content": "yo"},
		},
	})
}

// TestCmdConvPutRejectsAMessagesFileThatIsNotAnArrayBeforeCallingTheServer.
//
// put_conversation replaces the conversation by default. A file holding an
// object rather than an array must be caught locally, not sent and refused.
func TestCmdConvPutRejectsAMessagesFileThatIsNotAnArrayBeforeCallingTheServer(t *testing.T) {
	for _, content := range []string{`{"role":"user"}`, `not json`, ``} {
		bad := cmdsTempFile(t, "m.json", content)
		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			t.Errorf("conv-put called %q with a malformed messages file", name)
			return fakeReply{IsError: true}
		})

		if _, err := cmdsRun(t, f, cmdConvPut, "p1", "--messages", bad); err == nil {
			t.Errorf("conv-put accepted a messages file holding %q", content)
		}
		if n := len(f.recorded()); n != 0 {
			t.Errorf("%d requests made for a messages file holding %q", n, content)
		}
	}
}

// TestCmdConvPutSurfacesAMissingMessagesFileWithoutCallingTheServer.
func TestCmdConvPutSurfacesAMissingMessagesFileWithoutCallingTheServer(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		t.Errorf("conv-put called %q though its messages file does not exist", name)
		return fakeReply{IsError: true}
	})

	missing := filepath.Join(t.TempDir(), "nope.json")
	if _, err := cmdsRun(t, f, cmdConvPut, "p1", "--messages", missing); err == nil {
		t.Fatal("conv-put succeeded with a missing messages file")
	}
	if n := len(f.recorded()); n != 0 {
		t.Errorf("%d requests made though the messages file was unreadable", n)
	}
}

// ---------------------------------------------------------------------------
// Usage classification.
// ---------------------------------------------------------------------------

// TestUsageErrorsClassifyAsUsageAndTouchNoNetwork.
//
// Exit 2 is the signal an agent branches on to stop retrying. Both halves
// matter and both are asserted: the classification (so the process exits 2) and
// the absence of traffic (so a doomed invocation costs nothing and, for the
// mutating commands, cannot half-apply).
func TestUsageErrorsClassifyAsUsageAndTouchNoNetwork(t *testing.T) {
	cases := []struct {
		name string
		fn   cmdsFn
		argv []string
	}{
		{"new without a name", cmdNew, []string{}},
		{"ls without a project", cmdLs, []string{}},
		{"tree without a project", cmdTree, []string{}},
		{"cat without a path", cmdCat, []string{"p1"}},
		{"put without a path", cmdPut, []string{"p1"}},
		{"rm without a project", cmdRm, []string{}},
		{"rm without any path", cmdRm, []string{"p1"}},
		{"cp without a destination", cmdCp, []string{"p1", "a.css"}},
		{"plan without a project", cmdPlan, []string{}},
		{"preview without a path", cmdPreview, []string{"p1"}},
		{"support-js without a project", cmdSupportJS, []string{}},
		{"conv without a project", cmdConv, []string{}},
		{"conv-put without --messages", cmdConvPut, []string{"p1"}},
		{"conv-put without a project", cmdConvPut, []string{}},
		{"member-add without --role", cmdMemberAdd, []string{"p1", "--email", "a@b.c"}},
		{"member-add without --email or --uuid", cmdMemberAdd, []string{"p1", "--role", "editor"}},
		{"member-add without a project", cmdMemberAdd, []string{}},
		{"member-rm without a uuid", cmdMemberRm, []string{"p1"}},
		{"member-role without a role", cmdMemberRole, []string{"p1", "u1"}},
		{"sharing without a project", cmdSharing, []string{}},
		{"raw without a tool", cmdRaw, []string{}},
		{"raw with a JSON array for arguments", cmdRaw, []string{"t", `[1,2]`}},
		{"raw with a JSON string for arguments", cmdRaw, []string{"t", `"nope"`}},
		{"raw with malformed JSON", cmdRaw, []string{"t", `{`}},
		{"an unknown flag", cmdLs, []string{"p1", "--nope"}},
		{"a malformed -j", cmdTree, []string{"p1", "-j", "lots"}},
		{"--json=maybe", cmdLs, []string{"p1", "--json=maybe"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
				t.Errorf("a usage error still called %q", name)
				return fakeReply{IsError: true}
			})

			_, err := cmdsRun(t, f, tc.fn, tc.argv...)
			if err == nil {
				t.Fatal("the invocation was accepted")
			}
			if got := classify(err).Kind; got != kindUsage {
				t.Errorf("kind = %q, want %q", got, kindUsage)
			}
			if got := exitCodeFor(err); got != exitUsage {
				t.Errorf("exit code = %d, want %d", got, exitUsage)
			}
			if n := len(f.recorded()); n != 0 {
				t.Errorf("%d requests made for an invocation that could never work", n)
			}
		})
	}
}

// TestFlagsAreAcceptedAfterPositionalArguments -- Go's flag package stops at the
// first non-flag token, so `dsx ls p1 --json` would silently ignore --json and
// hand prose to a parser. parseArgs exists to prevent that; these commands are
// the ones that would carry the wart.
func TestFlagsAreAcceptedAfterPositionalArguments(t *testing.T) {
	f := newFakeMCP(t, cmdsReplyJSON("plain prose, not JSON"))

	out, err := cmdsRun(t, f, cmdLs, "p1", "src", "--json")
	if err != nil {
		t.Fatalf("ls failed: %v", err)
	}
	if !json.Valid([]byte(out)) {
		t.Fatalf("--json after a positional was ignored; stdout: %q", out)
	}
	cmdsWantArgs(t, cmdsOnlyCall(t, f).Args, map[string]any{"project_id": "p1", "path": "src"})
}

// ---------------------------------------------------------------------------
// Error passthrough.
// ---------------------------------------------------------------------------

// TestAToolErrorReachesTheCallerRatherThanBeingPrintedAsSuccess.
//
// The wrappers deliberately do not interpret replies, but a tool failure must
// still stop the command: printing an error body on stdout and exiting 0 is the
// one outcome a scripted caller cannot detect.
func TestAToolErrorReachesTheCallerRatherThanBeingPrintedAsSuccess(t *testing.T) {
	cases := []struct {
		name string
		fn   cmdsFn
		argv []string
	}{
		{"ls", cmdLs, []string{"p1"}},
		{"new", cmdNew, []string{"n"}},
		{"plan", cmdPlan, []string{"p1"}},
		{"raw", cmdRaw, []string{"any_tool"}},
		{"sharing", cmdSharing, []string{"p1"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeMCP(t, func(string, map[string]any) fakeReply {
				return fakeReply{IsError: true, Text: "path not found"}
			})

			out, err := cmdsRun(t, f, tc.fn, tc.argv...)
			if err == nil {
				t.Fatal("a tool error was reported as success")
			}
			if out != "" {
				t.Errorf("stdout = %q, want nothing printed for a failed call", out)
			}
			var te *toolError
			if !asToolError(err, &te) {
				t.Fatalf("error %v is not a *toolError; callers match on the type, not the text", err)
			}
			if te.Text != "path not found" {
				t.Errorf("text = %q, want the server's own message", te.Text)
			}
		})
	}
}

// TestCmdCatSurfacesAMalformedEnvelopeRatherThanPrintingIt -- cat's stdout is
// the file, so a reply that did not parse must never reach it.
func TestCmdCatSurfacesAMalformedEnvelopeRatherThanPrintingIt(t *testing.T) {
	f := newFakeMCP(t, cmdsReplyJSON("this is not an envelope at all"))

	out, err := cmdsRun(t, f, cmdCat, "p1", "a.css")
	if err == nil {
		t.Fatal("cat accepted a reply carrying no envelope")
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
}

// ---------------------------------------------------------------------------
// firstLine.
// ---------------------------------------------------------------------------

func TestFirstLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"no newline returns the whole string", "one line only", "one line only"},
		{"a leading newline yields an empty first line", "\nsecond", ""},
		{"stops at the first newline", "first\nsecond\nthird", "first"},
		{"a trailing newline is not part of the line", "first\n", "first"},
		{"only a newline", "\n", ""},
		// The cut is on \n alone, so a CRLF description keeps its CR. Pinned
		// because a caller aligning columns would see one stray byte of width.
		{"CRLF keeps the carriage return", "first\r\nsecond", "first\r"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstLine(tc.in); got != tc.want {
				t.Errorf("firstLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
