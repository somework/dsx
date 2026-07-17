package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHelpAndCompletionHonourJSONLikeEveryOtherCommand(t *testing.T) {
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

	out, err := captureStdout(t, func() error { return cmdCompletion([]string{"bash"}) })
	if err != nil {
		t.Fatal(err)
	}
	if json.Valid([]byte(out)) || !strings.Contains(out, "complete -F _dsx dsx") {
		t.Errorf("prose completion is no longer a shell script: %q", out[:min(len(out), 120)])
	}
}
