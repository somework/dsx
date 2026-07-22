package fmtutil

import "testing"

// Printable answers false to unicode.IsGraphic for '\n' as readily as for
// '\r', so running it over a whole reply collapses the reply into one
// '?'-riddled line. That is what the unrecognised-reply fallback in
// `project ls` has always done to anything with a line break in it, and it is
// the wrong shape for a document: the guard invariant 7 asks for is against
// text that rewrites what the terminal already showed, not against the line
// breaks dsx itself is printing.
func TestPrintableDocKeepsTheLinesAndStillDisarmsTheRest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, in, want string }{
		{"newlines survive", "a\nb\nc", "a\nb\nc"},
		{"tabs survive", "a\tb", "a\tb"},
		{"a carriage return is still disarmed", "safe\rEVIL", "safe?EVIL"},
		{"an escape is still disarmed", "safe\x1b[2KEVIL", "safe?[2KEVIL"},
		{"indented json is untouched", "{\n  \"a\": 1\n}", "{\n  \"a\": 1\n}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := PrintableDoc(tc.in); got != tc.want {
				t.Errorf("PrintableDoc(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The single-field guard keeps its old shape: a name is one line by
// construction, and a break inside one is itself the attack.
func TestPrintableStillCollapsesALineBreakInAField(t *testing.T) {
	t.Parallel()
	if got := Printable("a\nb"); got != "a?b" {
		t.Errorf("Printable(%q) = %q, want %q", "a\nb", got, "a?b")
	}
}
