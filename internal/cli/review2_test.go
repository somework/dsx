package cli

import (
	"encoding/json"
	"path/filepath"
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
