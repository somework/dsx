package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// Round two's CLI half. The sync half moved to internal/syncer with the code
// it guards; the preamble there explains what round two was.
//
// TestPutClassifiesAConflictEvenWithACallerSuppliedPlanToken moved to
// internal/cmd/files with cmdPut, which it drives directly.

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
