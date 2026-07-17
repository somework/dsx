package main

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// Round two's CLI half. The sync half moved to internal/syncer with the code
// it guards; the preamble there explains what round two was.

// ---------------------------------------------------------------------------
// the write path
// ---------------------------------------------------------------------------

func TestPutClassifiesAConflictEvenWithACallerSuppliedPlanToken(t *testing.T) {
	// emitWrite short-circuited to emit() when the caller passed --plan, and
	// emit() never classifies. Same tool, same reply, opposite exit code: 3
	// without --plan, 1 with it. The live test that "pinned" this called
	// conflictFromToolError directly and never went through cmdPut, so it passed.
	body := `{"conflicts":[{"path":"a.css","etag":"999"}],"message":"write_files: refused — … Nothing was written."}`
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: body, IsError: true}
	})

	dir := t.TempDir()
	mkfile(t, dir, "a.css", "x")
	err := cmdPut(t.Context(), fakeClient(f), []string{
		"p1", "a.css", filepath.Join(dir, "a.css"), "--plan", "tok", "--if-match", "stale",
	})
	if err == nil {
		t.Fatal("the server refused the write and put reported success")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindConflict {
		t.Fatalf("put --plan classified a conflict as %q (exit %d); without --plan it is %q (exit %d). "+
			"Same tool, same reply, opposite answer.", got, got.ExitCode(), dsxerr.KindConflict, dsxerr.ExitConflict)
	}
}

func TestSupportJSSelfAuthorisesUsingTheServersDocumentedDefaultPath(t *testing.T) {
	// `dsx support-js p1` — the documented form — skipped the grant recovery on
	// the theory that there was nothing to name in a plan. The server's own
	// schema says otherwise: path "defaults to support.js at the project root".
	var planned []string
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "create_support_js":
			if _, ok := args["plan_token"]; !ok {
				return fakeReply{
					HTTPStatus: 403,
					HTTPBody:   `{"error":"needs_project_grant","project_id":"p1"}`,
				}
			}
			return fakeReply{Text: `{"path":"support.js"}`}
		case "finalize_plan":
			if w, ok := args["writes"].([]any); ok {
				for _, p := range w {
					planned = append(planned, p.(string))
				}
			}
			return fakeReply{Text: `{"plan_token":"tok"}`}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	if err := cmdSupportJS(t.Context(), fakeClient(f), []string{"p1"}); err != nil {
		t.Fatalf("support-js with no --path did not recover from needs_project_grant: %v", err)
	}
	if !slices.Contains(planned, "support.js") {
		t.Errorf("finalize_plan authorised %v, want the server's documented default support.js", planned)
	}
}

// ---------------------------------------------------------------------------
// transport
// ---------------------------------------------------------------------------

func TestHelpAndCompletionHonourJSONLikeEveryOtherCommand(t *testing.T) {
	// README promises --json on every command with no carve-out. help and
	// completion were dispatched before any FlagSet and printed prose at exit 0,
	// so a caller that pipes stdout into a parser got a broken pipe of text.
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"help", func() error { return cmdHelp([]string{"--json"}) }},
		{"completion", func() error { return cmdCompletion([]string{"bash", "--json"}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, tc.run)
			if err != nil {
				t.Fatal(err)
			}
			if !json.Valid([]byte(out)) {
				t.Fatalf("%s --json is not JSON: %q", tc.name, out[:min(len(out), 120)])
			}
		})
	}

	// Prose still works: `eval "$(dsx completion bash)"` is the point of it.
	out, err := captureStdout(t, func() error { return cmdCompletion([]string{"bash"}) })
	if err != nil {
		t.Fatal(err)
	}
	if json.Valid([]byte(out)) || !strings.Contains(out, "complete -F _dsx dsx") {
		t.Errorf("prose completion is no longer a shell script: %q", out[:min(len(out), 120)])
	}
}
