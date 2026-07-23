package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/mcp"
	"github.com/somework/dsx/internal/mcptest"
	"github.com/somework/dsx/internal/syncer"
)

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

func cmdsWantArgs(t *testing.T, got map[string]any, want map[string]any) {
	t.Helper()
	if !reflect.DeepEqual(any(got), cmdsNormalize(t, want)) {
		t.Errorf("arguments mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func cmdsToolCalls(f *fakeMCP) []mcptest.Call {
	var out []mcptest.Call
	for _, c := range f.Recorded() {
		if c.Method == "tools/call" {
			out = append(out, c)
		}
	}
	return out
}

func cmdsOnlyCall(t *testing.T, f *fakeMCP) mcptest.Call {
	t.Helper()
	calls := cmdsToolCalls(f)
	if len(calls) != 1 {
		t.Fatalf("want exactly 1 tool call, got %d: %#v", len(calls), calls)
	}
	return calls[0]
}

func cmdsReplyJSON(text string) func(string, map[string]any) fakeReply {
	return func(string, map[string]any) fakeReply { return fakeReply{Text: text} }
}

func cmdsTempFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func cmdsRun(t *testing.T, f *fakeMCP, name string, argv ...string) (string, error) {
	t.Helper()
	entry, ok := commandIndex[name]
	if !ok {
		t.Fatalf("no command named %q; this table names commands, not functions", name)
	}
	return captureStdout(t, func() error {
		return entry.Dispatch(context.Background(), fakeClient(f), argv)
	})
}

func TestCmdsCallTheRightToolWithExactlyTheRightArguments(t *testing.T) {
	cases := []struct {
		name     string
		cmd      string
		argv     []string
		wantTool string
		wantArgs map[string]any
	}{
		{
			name: "new without --ds omits design_system_id",
			cmd:  "project new", argv: []string{"My Project"},
			wantTool: "create_project",
			wantArgs: map[string]any{"name": "My Project"},
		},
		{
			name: "new with --ds",
			cmd:  "project new", argv: []string{"My Project", "--ds", "ds-1"},
			wantTool: "create_project",
			wantArgs: map[string]any{"name": "My Project", "design_system_id": "ds-1"},
		},
		{
			name: "ls without a path omits path",
			cmd:  "files ls", argv: []string{"p1"},
			wantTool: "list_files",
			wantArgs: map[string]any{"project_id": "p1"},
		},
		{
			name: "ls with a path",
			cmd:  "files ls", argv: []string{"p1", "src/components"},
			wantTool: "list_files",
			wantArgs: map[string]any{"project_id": "p1", "path": "src/components"},
		},
		{
			name: "cp within one project omits src_project_id",
			cmd:  "files cp", argv: []string{"p1", "a.css", "b.css"},
			wantTool: "copy_files",
			wantArgs: map[string]any{
				"project_id": "p1",
				"files":      []any{map[string]any{"src": "a.css", "dest": "b.css"}},
			},
		},
		{
			name: "cp across projects carries src_project_id in the file entry",
			cmd:  "files cp", argv: []string{"p1", "a.css", "b.css", "--from", "p0"},
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
			cmd:  "files cp", argv: []string{"p1", "a.css", "b.css", "--if-match", "e1", "--plan", "tok"},
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
			cmd:  "plan new", argv: []string{"p1"},
			wantTool: "finalize_plan",
			wantArgs: map[string]any{"project_id": "p1"},
		},
		{
			name: "plan splits comma lists and drops blanks",
			cmd:  "plan new", argv: []string{"p1", "--writes", "a.css, b.css,", "--deletes", "c.css"},
			wantTool: "finalize_plan",
			wantArgs: map[string]any{
				"project_id": "p1",
				"writes":     []any{"a.css", "b.css"},
				"deletes":    []any{"c.css"},
			},
		},
		{
			name: "plan --scope project",
			cmd:  "plan new", argv: []string{"p1", "--scope", "project"},
			wantTool: "finalize_plan",
			wantArgs: map[string]any{"project_id": "p1", "scope": "project"},
		},
		{
			// `render` is never sent: the server dropped it from the schema and
			// a probe showed render:true changing not one key of the reply, so
			// dsx no longer offers the flag at all.
			name: "preview sends no render",
			cmd:  "files preview", argv: []string{"p1", "index.html"},
			wantTool: "render_preview",
			wantArgs: map[string]any{"project_id": "p1", "path": "index.html"},
		},
		{
			// --validators stays: unlike render it is still declared, as
			// "Reserved … Ignored today" — a slot the server means to honour.
			name: "preview with validators",
			cmd:  "files preview", argv: []string{"p1", "index.html", "--validators", "a11y,css"},
			wantTool: "render_preview",
			wantArgs: map[string]any{
				"project_id": "p1", "path": "index.html",
				"validators": []any{"a11y", "css"},
			},
		},
		{
			name: "support-js sends only the project when nothing else is set",
			cmd:  "project support-js", argv: []string{"p1"},
			wantTool: "create_support_js",
			wantArgs: map[string]any{"project_id": "p1"},
		},
		{
			name: "support-js with every optional argument",
			cmd:  "project support-js", argv: []string{"p1", "--path", "s.js", "--if-match", "e1", "--plan", "tok"},
			wantTool: "create_support_js",
			wantArgs: map[string]any{
				"project_id": "p1", "path": "s.js", "if_match": "e1", "plan_token": "tok",
			},
		},
		{
			name: "conv without --chat omits chat_id",
			cmd:  "conv get", argv: []string{"p1"},
			wantTool: "get_conversation",
			wantArgs: map[string]any{"project_id": "p1"},
		},
		{
			name: "conv with --chat",
			cmd:  "conv get", argv: []string{"p1", "--chat", "c1"},
			wantTool: "get_conversation",
			wantArgs: map[string]any{"project_id": "p1", "chat_id": "c1"},
		},
		{
			name: "member add by email",
			cmd:  "member add", argv: []string{"p1", "--role", "editor", "--email", "a@b.c"},
			wantTool: "add_member",
			wantArgs: map[string]any{"project_id": "p1", "role": "editor", "email": "a@b.c"},
		},
		{
			name: "member add by uuid",
			cmd:  "member add", argv: []string{"p1", "--role", "viewer", "--uuid", "u1"},
			wantTool: "add_member",
			wantArgs: map[string]any{"project_id": "p1", "role": "viewer", "account_uuid": "u1"},
		},
		{
			name: "member rm",
			cmd:  "member rm", argv: []string{"p1", "u1"},
			wantTool: "remove_member",
			wantArgs: map[string]any{"project_id": "p1", "account_uuid": "u1"},
		},
		{
			name: "member role takes the role positionally",
			cmd:  "member role", argv: []string{"p1", "u1", "viewer"},
			wantTool: "update_member_role",
			wantArgs: map[string]any{"project_id": "p1", "account_uuid": "u1", "role": "viewer"},
		},
		{
			name: "sharing with no options sends only the project",
			cmd:  "project sharing", argv: []string{"p1"},
			wantTool: "update_sharing",
			wantArgs: map[string]any{"project_id": "p1"},
		},
		{
			name: "sharing with both options",
			cmd:  "project sharing", argv: []string{"p1", "--scope", "org", "--link-permission", "read"},
			wantTool: "update_sharing",
			wantArgs: map[string]any{"project_id": "p1", "scope": "org", "link_permission": "read"},
		},
		{
			name: "prompt with no flags sends an empty argument map",
			cmd:  "prompt", argv: []string{},
			wantTool: "get_claude_design_prompt",
			wantArgs: map[string]any{},
		},
		{
			name: "prompt with both flags",
			cmd:  "prompt", argv: []string{"--project", "p1", "--ds", "ds-1"},
			wantTool: "get_claude_design_prompt",
			wantArgs: map[string]any{"project_id": "p1", "design_system_id": "ds-1"},
		},
		{
			name: "raw passes the tool name and parsed object straight through",
			cmd:  "raw", argv: []string{"some_undocumented_tool", `{"a":1,"b":["x"]}`},
			wantTool: "some_undocumented_tool",
			wantArgs: map[string]any{"a": 1, "b": []any{"x"}},
		},
		{
			name: "raw with no argument string sends an empty object",
			cmd:  "raw", argv: []string{"some_undocumented_tool"},
			wantTool: "some_undocumented_tool",
			wantArgs: map[string]any{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeMCP(t, cmdsReplyJSON(`{"ok":true}`))
			if _, err := cmdsRun(t, f, tc.cmd, tc.argv...); err != nil {
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

// A project-scoped plan authorises every path, so naming paths alongside it is a
// contradiction the client can settle itself. Deciding it locally keeps the exit code
// honest (usage, not failure) and — the load-bearing half — means no over-broad token
// is ever minted for an invocation the user did not mean.
func TestPlanRefusesProjectScopeWithPaths(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"--scope project with --writes", []string{"p1", "--scope", "project", "--writes", "a.txt"}},
		{"--scope project with --deletes", []string{"p1", "--scope", "project", "--deletes", "a.txt"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
				t.Errorf("a contradictory plan still called %q", name)
				return fakeReply{IsError: true, Text: "unexpected"}
			})

			_, err := cmdsRun(t, f, "plan new", tc.argv...)
			if err == nil {
				t.Fatal("the invocation was accepted")
			}
			if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
				t.Errorf("kind = %q, want %q", got, dsxerr.KindUsage)
			}
			if calls := cmdsToolCalls(f); len(calls) != 0 {
				t.Errorf("%d tool calls made; nothing may reach the server, or an over-broad token could be minted: %#v", len(calls), calls)
			}
		})
	}
}

func TestCmdPutSendsIfMatchOnlyWhenAskedAndNeverAsAnEmptyString(t *testing.T) {
	body := "h1 { color: red; }"
	src := cmdsTempFile(t, "a.css", body)

	cases := []struct {
		name        string
		argv        []string
		wantIfMatch any
	}{
		{"unset", []string{"p1", "a.css", src}, nil},
		{`"0" asserts the path is new`, []string{"p1", "a.css", src, "--if-match", "0"}, "0"},
		{"a real etag", []string{"p1", "a.css", src, "--if-match", "etag-7"}, "etag-7"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeMCP(t, cmdsReplyJSON(`{"written":{"a.css":"e2"}}`))
			if _, err := cmdsRun(t, f, "files put", tc.argv...); err != nil {
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

func TestCmdPutBase64sTheFileAndDeclaresTheEncoding(t *testing.T) {
	body := "\x00\x01\xff\xfe binary-ish"
	src := cmdsTempFile(t, "blob.bin", body)

	f := newFakeMCP(t, cmdsReplyJSON(`{"written":{"blob.bin":"e1"}}`))
	if _, err := cmdsRun(t, f, "files put", "p1", "assets/blob.bin", src); err != nil {
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

func TestCmdPutOmitsPlanTokenWhenNotGiven(t *testing.T) {
	src := cmdsTempFile(t, "a.css", "x")

	f := newFakeMCP(t, cmdsReplyJSON(`{"written":{"a.css":"e1"}}`))
	if _, err := cmdsRun(t, f, "files put", "p1", "a.css", src); err != nil {
		t.Fatalf("put failed: %v", err)
	}
	if _, present := cmdsOnlyCall(t, f).Args["plan_token"]; present {
		t.Error("plan_token sent though --plan was never given")
	}

	f2 := newFakeMCP(t, cmdsReplyJSON(`{"written":{"a.css":"e1"}}`))
	if _, err := cmdsRun(t, f2, "files put", "p1", "a.css", src, "--plan", "tok-9"); err != nil {
		t.Fatalf("put failed: %v", err)
	}
	if got := cmdsOnlyCall(t, f2).Args["plan_token"]; got != "tok-9" {
		t.Errorf("plan_token = %#v, want tok-9", got)
	}
}

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

	if _, err := cmdsRun(t, f, "files rm", "p1", "a.css", "dir/b.css"); err != nil {
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

	cmdsWantArgs(t, calls[0].Args, map[string]any{
		"project_id": "p1",
		"deletes":    []any{"a.css", "dir/b.css"},
	})

	cmdsWantArgs(t, calls[1].Args, map[string]any{
		"project_id": "p1",
		"plan_token": "tok-123",
		"paths":      []any{"a.css", "dir/b.css"},
	})
}

func TestCmdRmDeletesNothingWhenFinalizePlanReturnsNoToken(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "finalize_plan" {
			return fakeReply{Text: `{"status":"ok"}`}
		}
		return fakeReply{Text: `{"deleted":[]}`}
	})

	_, err := cmdsRun(t, f, "files rm", "p1", "a.css")
	if err == nil {
		t.Fatal("rm succeeded though it never got a plan_token")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindProtocol {
		t.Errorf("kind = %q, want %q", got, dsxerr.KindProtocol)
	}
	if n := f.CountTool("delete_files"); n != 0 {
		t.Errorf("delete_files called %d times without a token; nothing may be deleted unauthorised", n)
	}
}

func TestCmdRmWithNoPathsTouchesNoNetwork(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		t.Errorf("rm with no paths called %q", name)
		return fakeReply{IsError: true}
	})

	_, err := cmdsRun(t, f, "files rm", "p1")
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind = %q, want %q", got, dsxerr.KindUsage)
	}
	if n := len(f.Recorded()); n != 0 {
		t.Errorf("%d requests made for an invocation that could never work", n)
	}
}

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
			_, err := syncer.PlanToken(context.Background(), fakeClient(f),
				map[string]any{"project_id": "p1", "deletes": []string{"a.css"}})
			if err == nil {
				t.Fatal("syncer.PlanToken accepted a reply carrying no usable token")
			}
			de := dsxerr.Classify(err)
			if de.Kind != dsxerr.KindProtocol {
				t.Errorf("kind = %q, want %q", de.Kind, dsxerr.KindProtocol)
			}
			if code := dsxerr.ExitCodeFor(err); code == dsxerr.ExitTransport {
				t.Errorf("exit code = %d (transport); a caller must not retry this", code)
			}
		})
	}
}

func TestPlanTokenReturnsTheTokenAndForwardsTheRequestVerbatim(t *testing.T) {
	f := newFakeMCP(t, cmdsReplyJSON(`{"plan_token":"tok-abc","expires_in":900}`))

	got, err := syncer.PlanToken(context.Background(), fakeClient(f),
		map[string]any{"project_id": "p1", "writes": []string{"a.css", "b.css"}})
	if err != nil {
		t.Fatalf("syncer.PlanToken: %v", err)
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

func TestPlanTokenKeepsATransportFailureRetryable(t *testing.T) {
	f := newFakeMCP(t, func(string, map[string]any) fakeReply {
		return fakeReply{HTTPStatus: 503, HTTPBody: "upstream unavailable"}
	})

	_, err := syncer.PlanToken(context.Background(), fakeClient(f),
		map[string]any{"project_id": "p1", "deletes": []string{"a.css"}})
	if err == nil {
		t.Fatal("syncer.PlanToken reported success against a 503")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindTransport {
		t.Errorf("kind = %q, want %q -- a 503 is not a protocol violation", got, dsxerr.KindTransport)
	}
}

func TestJSONOutputIsExactlyOneJSONDocument(t *testing.T) {
	const prose = "Design guidance:\n  use tokens, not hex.\nThat is all."
	const asJSON = `{"tools":["a"],"count":1}`

	cases := []struct {
		name string
		cmd  string
		argv []string
	}{
		{"files ls", "files ls", []string{"p1", "--json"}},
		{"project new", "project new", []string{"n", "--json"}},
		{"prompt", "prompt", []string{"--json"}},
		{"conv get", "conv get", []string{"p1", "--json"}},
		{"project sharing", "project sharing", []string{"p1", "--json"}},
		{"plan new", "plan new", []string{"p1", "--json"}},
		{"files preview", "files preview", []string{"p1", "i.html", "--json"}},
		{"member rm", "member rm", []string{"p1", "u1", "--json"}},
		{"member role", "member role", []string{"p1", "u1", "viewer", "--json"}},
		{"raw", "raw", []string{"any_tool", `{}`, "--json"}},
	}

	for _, tc := range cases {
		t.Run(tc.name+" wraps a prose reply", func(t *testing.T) {
			f := newFakeMCP(t, cmdsReplyJSON(prose))
			out, err := cmdsRun(t, f, tc.cmd, tc.argv...)
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

			if n := strings.Count(strings.TrimSuffix(out, "\n"), "\n"); n != 0 {
				t.Errorf("stdout spans %d extra lines; wrapped prose must escape its newlines", n)
			}
		})

		t.Run(tc.name+" passes a JSON reply through untouched", func(t *testing.T) {
			f := newFakeMCP(t, cmdsReplyJSON(asJSON))
			out, err := cmdsRun(t, f, tc.cmd, tc.argv...)
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

func TestCmdCatJSONPutsTheBodyInAJSONStringInsteadOfDumpingItOnStdout(t *testing.T) {
	body := "h1 {\n  content: \"a \\\" quote\";\n}\n"
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name != "read_file" {
			t.Errorf("cat called %q, want read_file", name)
		}
		return fakeReply{Text: envelopeFor("a.css", "etag-1", body)}
	})

	out, err := cmdsRun(t, f, "files cat", "p1", "a.css", "--json")
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

func TestCmdCatWithoutJSONWritesTheBodyVerbatimAndAddsNothing(t *testing.T) {
	body := "no trailing newline here"
	f := newFakeMCP(t, func(string, map[string]any) fakeReply {
		return fakeReply{Text: envelopeFor("a.css", "e1", body)}
	})

	out, err := cmdsRun(t, f, "files cat", "p1", "a.css")
	if err != nil {
		t.Fatalf("cat failed: %v", err)
	}
	if out != body {
		t.Errorf("stdout = %q, want exactly %q", out, body)
	}
}

func TestCmdCatOutWritesTheFileAndKeepsTheBodyOffStdout(t *testing.T) {
	body := "h1 { color: red; }\n"
	reply := func(string, map[string]any) fakeReply {
		return fakeReply{Text: envelopeFor("a.css", "e1", body)}
	}

	t.Run("without --json stdout stays silent", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "out.css")
		f := newFakeMCP(t, reply)

		out, err := cmdsRun(t, f, "files cat", "p1", "a.css", "--out", dst)
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

		out, err := cmdsRun(t, f, "files cat", "p1", "a.css", "--out", dst, "--json")
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

func TestCmdCatOutReportsAFailedWriteInsteadOfFallingBackToStdout(t *testing.T) {
	f := newFakeMCP(t, func(string, map[string]any) fakeReply {
		return fakeReply{Text: envelopeFor("a.css", "e1", "h1 { color: red; }")}
	})

	dst := filepath.Join(t.TempDir(), "no-such-dir", "out.css")
	out, err := cmdsRun(t, f, "files cat", "p1", "a.css", "--out", dst)
	if err == nil {
		t.Fatal("cat reported success though --out could not be written")
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
}

func TestCmdTreeJSONListsEveryFileSortedAndOmitsDirectories(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch args["path"] {
		case nil:
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

	out, err := cmdsRun(t, f, "files tree", "p1", "--json")
	if err != nil {
		t.Fatalf("tree failed: %v", err)
	}

	var got []syncer.RemoteEntry
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout did not parse as a listing: %v (%q)", err, out)
	}
	want := []syncer.RemoteEntry{
		{Path: "src/a.css", Type: "file", Size: 1, Etag: "e-a"},
		{Path: "src/b.css", Type: "file", Size: 2, Etag: "e-b"},
		{Path: "z.css", Type: "file", Size: 3, Etag: "e-z"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tree =\n %#v\nwant\n %#v", got, want)
	}
}

func TestCmdTreeJSONOnAnEmptyProjectIsAnEmptyArrayNotNull(t *testing.T) {
	f := newFakeMCP(t, cmdsReplyJSON(listingFor()))

	out, err := cmdsRun(t, f, "files tree", "p1", "--json")
	if err != nil {
		t.Fatalf("tree failed: %v", err)
	}
	if got := strings.TrimSpace(out); got != "[]" {
		t.Errorf("stdout = %q, want []", got)
	}
}

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

	out, err := cmdsRun(t, f, "files tree", "p1")
	if err != nil {
		t.Fatalf("tree failed: %v", err)
	}

	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want one per file plus a summary:\n%s", len(lines), out)
	}

	if !strings.HasSuffix(lines[0], "src/a.css") || !strings.HasSuffix(lines[1], "z.css") {
		t.Errorf("rows are not sorted by path:\n%s", out)
	}
	if !strings.Contains(lines[0], "e-a") || !strings.Contains(lines[1], "e-z") {
		t.Errorf("etags are missing from the rows:\n%s", out)
	}

	if got := lines[2]; got != "2 files, 1.0 KB" {
		t.Errorf("summary = %q, want %q", got, "2 files, 1.0 KB")
	}
}

func TestCmdTreeReportsAListingFailureRatherThanPrintingAShortTree(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if args["path"] == nil {
			return fakeReply{Text: listingFor(fileEntry("z.css", "e-z", 1), dirEntry("src"))}
		}
		return fakeReply{IsError: true, Text: "permission denied"}
	})

	out, err := cmdsRun(t, f, "files tree", "p1", "--json")
	if err == nil {
		t.Fatal("tree reported success though a directory never listed")
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing: a partial tree must not be printed", out)
	}
}

func TestCmdToolsPrintsOneLinePerToolAndTruncatesAtTheFirstLine(t *testing.T) {
	f := newFakeMCP(t, cmdsReplyJSON(`{}`))

	out, err := cmdsRun(t, f, "tools")
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

	if strings.Contains(out, "in a project") {
		t.Errorf("the tail of a multi-line description reached stdout:\n%s", out)
	}
}

func TestCmdToolsJSONEmitsTheServersOwnListAsOneDocument(t *testing.T) {
	for _, flag := range []string{"--json", "--schema"} {
		t.Run(flag, func(t *testing.T) {
			f := newFakeMCP(t, cmdsReplyJSON(`{}`))

			out, err := cmdsRun(t, f, "tools", flag)
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

func TestCmdConvPutSendsSyncedThroughIdxOnlyWhenGiven(t *testing.T) {
	msgs := cmdsTempFile(t, "m.json", `[{"role":"user","content":"hi"}]`)

	cases := []struct {
		name string
		argv []string
		want any
	}{
		{"unset", []string{"p1", "--messages", msgs}, nil},
		{"explicit zero", []string{"p1", "--messages", msgs, "--synced-through-idx", "0"}, float64(0)},
		{"explicit five", []string{"p1", "--messages", msgs, "--synced-through-idx", "5"}, float64(5)},
		{"negative is treated as unset", []string{"p1", "--messages", msgs, "--synced-through-idx", "-2"}, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeMCP(t, cmdsReplyJSON(`{"ok":true}`))
			if _, err := cmdsRun(t, f, "conv put", tc.argv...); err != nil {
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

func TestCmdConvPutForwardsTheMessagesArrayAndTheOptionalMetadata(t *testing.T) {
	msgs := cmdsTempFile(t, "m.json", `[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"}]`)
	f := newFakeMCP(t, cmdsReplyJSON(`{"ok":true}`))

	if _, err := cmdsRun(t, f, "conv put",
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

func TestCmdConvPutRejectsAMessagesFileThatIsNotAnArrayBeforeCallingTheServer(t *testing.T) {
	for _, content := range []string{`{"role":"user"}`, `not json`, ``} {
		bad := cmdsTempFile(t, "m.json", content)
		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			t.Errorf("conv-put called %q with a malformed messages file", name)
			return fakeReply{IsError: true}
		})

		_, err := cmdsRun(t, f, "conv put", "p1", "--messages", bad)
		if err == nil {
			t.Errorf("conv-put accepted a messages file holding %q", content)
		} else if k := dsxerr.Classify(err).Kind; k != dsxerr.KindUsage {
			t.Errorf("kind = %q for a messages file holding %q, want %q", k, content, dsxerr.KindUsage)
		}
		if n := len(f.Recorded()); n != 0 {
			t.Errorf("%d requests made for a messages file holding %q", n, content)
		}
	}
}

func TestCmdConvPutRejectsMessagesThatAreNotJSONObjects(t *testing.T) {
	for _, content := range []string{`[1,2,3]`, `["hi"]`, `[[]]`, `[{"role":"user"},3]`, `[null]`, `[{"a":1},null]`} {
		bad := cmdsTempFile(t, "m.json", content)
		f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			t.Errorf("conv-put called %q with a message element that is not an object", name)
			return fakeReply{IsError: true}
		})

		_, err := cmdsRun(t, f, "conv put", "p1", "--messages", bad)
		if err == nil {
			t.Errorf("conv-put accepted a messages file holding %q", content)
		} else if k := dsxerr.Classify(err).Kind; k != dsxerr.KindUsage {
			t.Errorf("kind = %q for a messages file holding %q, want %q", k, content, dsxerr.KindUsage)
		}
		if n := len(f.Recorded()); n != 0 {
			t.Errorf("%d requests made for a messages file holding %q", n, content)
		}
	}
}

func TestCmdConvPutSurfacesAMissingMessagesFileWithoutCallingTheServer(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		t.Errorf("conv-put called %q though its messages file does not exist", name)
		return fakeReply{IsError: true}
	})

	missing := filepath.Join(t.TempDir(), "nope.json")
	_, err := cmdsRun(t, f, "conv put", "p1", "--messages", missing)
	if err == nil {
		t.Fatal("conv-put succeeded with a missing messages file")
	}
	if k := dsxerr.Classify(err).Kind; k != dsxerr.KindUsage {
		t.Errorf("kind = %q for a missing messages file, want %q", k, dsxerr.KindUsage)
	}
	if n := len(f.Recorded()); n != 0 {
		t.Errorf("%d requests made though the messages file was unreadable", n)
	}
}

func TestUsageErrorsClassifyAsUsageAndTouchNoNetwork(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		argv []string
	}{
		{"project new without a name", "project new", []string{}},
		{"files ls without a project", "files ls", []string{}},
		{"files tree without a project", "files tree", []string{}},
		{"files cat without a path", "files cat", []string{"p1"}},
		{"files put without a path", "files put", []string{"p1"}},
		{"files rm without a project", "files rm", []string{}},
		{"files rm without any path", "files rm", []string{"p1"}},
		{"files cp without a destination", "files cp", []string{"p1", "a.css"}},
		{"plan new without a project", "plan new", []string{}},
		{"files preview without a path", "files preview", []string{"p1"}},
		{"support-js without a project", "project support-js", []string{}},
		{"conv get without a project", "conv get", []string{}},
		{"conv put without --messages", "conv put", []string{"p1"}},
		{"conv put without a project", "conv put", []string{}},
		{"member add without --role", "member add", []string{"p1", "--email", "a@b.c"}},
		{"member add without --email or --uuid", "member add", []string{"p1", "--role", "editor"}},
		{"member add with both --email and --uuid", "member add", []string{"p1", "--role", "editor", "--email", "a@b.c", "--uuid", "u1"}},
		{"member add without a project", "member add", []string{}},
		{"member rm without a uuid", "member rm", []string{"p1"}},
		{"member role without a role", "member role", []string{"p1", "u1"}},
		{"sharing without a project", "project sharing", []string{}},
		{"raw without a tool", "raw", []string{}},
		{"raw with a JSON array for arguments", "raw", []string{"t", `[1,2]`}},
		{"raw with a JSON string for arguments", "raw", []string{"t", `"nope"`}},
		{"raw with malformed JSON", "raw", []string{"t", `{`}},
		{"an unknown flag", "files ls", []string{"p1", "--nope"}},
		{"a malformed -j", "files tree", []string{"p1", "-j", "lots"}},
		{"--json=maybe", "files ls", []string{"p1", "--json=maybe"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
				t.Errorf("a usage error still called %q", name)
				return fakeReply{IsError: true}
			})

			_, err := cmdsRun(t, f, tc.cmd, tc.argv...)
			if err == nil {
				t.Fatal("the invocation was accepted")
			}
			if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
				t.Errorf("kind = %q, want %q", got, dsxerr.KindUsage)
			}
			if got := dsxerr.ExitCodeFor(err); got != dsxerr.ExitUsage {
				t.Errorf("exit code = %d, want %d", got, dsxerr.ExitUsage)
			}
			if n := len(f.Recorded()); n != 0 {
				t.Errorf("%d requests made for an invocation that could never work", n)
			}
		})
	}
}

func TestFlagsAreAcceptedAfterPositionalArguments(t *testing.T) {
	f := newFakeMCP(t, cmdsReplyJSON("plain prose, not JSON"))

	out, err := cmdsRun(t, f, "files ls", "p1", "src", "--json")
	if err != nil {
		t.Fatalf("ls failed: %v", err)
	}
	if !json.Valid([]byte(out)) {
		t.Fatalf("--json after a positional was ignored; stdout: %q", out)
	}
	cmdsWantArgs(t, cmdsOnlyCall(t, f).Args, map[string]any{"project_id": "p1", "path": "src"})
}

func TestAToolErrorReachesTheCallerRatherThanBeingPrintedAsSuccess(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		argv []string
	}{
		{"files ls", "files ls", []string{"p1"}},
		{"project new", "project new", []string{"n"}},
		{"plan new", "plan new", []string{"p1"}},
		{"raw", "raw", []string{"any_tool"}},
		{"project sharing", "project sharing", []string{"p1"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeMCP(t, func(string, map[string]any) fakeReply {
				return fakeReply{IsError: true, Text: "path not found"}
			})

			out, err := cmdsRun(t, f, tc.cmd, tc.argv...)
			if err == nil {
				t.Fatal("a tool error was reported as success")
			}
			if out != "" {
				t.Errorf("stdout = %q, want nothing printed for a failed call", out)
			}
			var te *mcp.ToolError
			if !errors.As(err, &te) {
				t.Fatalf("error %v is not a *mcp.ToolError; callers match on the type, not the text", err)
			}
			if te.Text != "path not found" {
				t.Errorf("text = %q, want the server's own message", te.Text)
			}
		})
	}
}

func TestCmdCatSurfacesAMalformedEnvelopeRatherThanPrintingIt(t *testing.T) {
	f := newFakeMCP(t, cmdsReplyJSON("this is not an envelope at all"))

	out, err := cmdsRun(t, f, "files cat", "p1", "a.css")
	if err == nil {
		t.Fatal("cat accepted a reply carrying no envelope")
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
}

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

		{"CRLF keeps the carriage return", "first\r\nsecond", "first\r"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cmd.FirstLine(tc.in); got != tc.want {
				t.Errorf("firstLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
