package cmd

import (
	"strings"
	"testing"
)

// Every tool reply used to reach a person exactly as it left the wire, so
// `dsx ds ls` answered a human with a one-line JSON array and `dsx files put`
// with {"etags":{…},"written":1}. --json existed and was not being asked for.
//
// PrintReply is the one place that decides. Three rules, and the third is the
// one that keeps dsx honest: a renderer may refuse. Every shape dsx renders
// was measured against the real server, not promised by it — the tools declare
// no output schema at all — and three protocol facts were guessed wrong
// before, so a reply that does not match falls through to itself rather than
// to a table built from a guess.
func TestPrintReplyLeavesJSONToTheMachine(t *testing.T) {
	t.Parallel()
	raw := `{"b":2,"a":1}`
	got := renderReply(raw, true, func(string) (string, bool) {
		t.Error("a renderer reached the --json path")
		return "never", true
	})
	if got != raw {
		t.Errorf("--json = %q, want the server's own bytes %q", got, raw)
	}
}

func TestPrintReplyPrefersTheRendererForAPerson(t *testing.T) {
	t.Parallel()
	got := renderReply(`{"a":1}`, false, func(string) (string, bool) { return "one design system", true })
	if got != "one design system" {
		t.Errorf("rendered = %q, want the renderer's line", got)
	}
}

func TestAnUnrecognisedReplyIsIndentedNotGuessedAt(t *testing.T) {
	t.Parallel()
	got := renderReply(`[{"id":"x","name":"n"}]`, false, func(string) (string, bool) { return "", false })
	want := "[\n  {\n    \"id\": \"x\",\n    \"name\": \"n\"\n  }\n]"
	if got != want {
		t.Errorf("fallback = %q, want it indented:\n%q", got, want)
	}
}

// get_claude_design_prompt answers in prose. Reindenting is not available and
// mangling it would be damage, so anything that is not JSON passes through.
func TestProseIsPassedThroughUntouched(t *testing.T) {
	t.Parallel()
	prose := "You are an expert designer.\n\nRule one.\n"
	if got := renderReply(prose, false, nil); got != strings.TrimSpace(prose) {
		t.Errorf("prose = %q, want it whole", got)
	}
}

// Invariant 7: what dsx prints for a person is sanitised, and the fallback is
// the path a hostile reply takes. It has to keep the line breaks while doing
// it — see fmtutil.PrintableDoc.
func TestTheFallbackDisarmsServerTextWithoutFlatteningIt(t *testing.T) {
	t.Parallel()
	got := renderReply("line one\rEVIL\nline two", false, nil)
	if strings.Contains(got, "\r") {
		t.Errorf("a carriage return reached the terminal: %q", got)
	}
	if !strings.Contains(got, "\n") {
		t.Errorf("the reply was flattened into one line: %q", got)
	}
}
