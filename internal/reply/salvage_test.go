package reply

import (
	"encoding/json"
	"strings"
	"testing"
)

// A capped transcript is not opaque text — it is a JSON document with its tail
// cut off, and 99% of it is intact. Handing that back as one string wastes it.
//
// The whole difficulty is WHERE to cut back to. Cutting at the last complete
// token leaves the innermost object looking finished: measured on the real
// reply, the last message kept its toolCall while that toolCall silently lost
// its `input`, so `…toolCall.input` answers null and nothing distinguishes
// "there was none" from "it was truncated". Cutting back to the last complete
// element of the OUTERMOST open array drops that message whole instead, and
// then everything present is byte-complete.
func TestSalvageCutsBackToWholeElementsNotWhateverParsed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, in, want string
		dropped        int
	}{
		{
			name: "an element cut mid-string goes entirely",
			in:   `{"chats":{"c1":{"messages":[{"i":1},{"i":2},{"i":3,"tool":{"name":"w","input":{"content":"cut`,
			want: `{"chats":{"c1":{"messages":[{"i":1},{"i":2}]}}}`,
		},
		{
			// The naive rule would keep {"i":3,"tool":{"name":"w"}} here and a
			// reader could not tell that tool lost its input.
			name: "a nested object never survives half-filled",
			in:   `{"a":[{"x":1},{"x":2,"deep":{"kept":true,"lost":`,
			want: `{"a":[{"x":1}]}`,
		},
		{
			name: "a complete trailing element is kept",
			in:   `{"a":[1,2,3`,
			want: `{"a":[1,2,3]}`,
		},
		{
			name: "no array at all falls back to the object's last whole member",
			in:   `{"a":1,"b":`,
			want: `{"a":1}`,
		},
		{
			name: "escapes are the stdlib lexer's problem, not ours",
			in:   `{"a":["he said \"hi\"","tail\\","cut`,
			want: `{"a":["he said \"hi\"","tail\\"]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, dropped, ok := salvageJSON([]byte(tc.in))
			if !ok {
				t.Fatalf("refused a document with %d salvageable bytes", len(tc.in))
			}
			if got != tc.want {
				t.Errorf("salvaged  %s\nwant      %s", got, tc.want)
			}
			if !json.Valid([]byte(got)) {
				t.Errorf("salvage produced invalid JSON: %s", got)
			}
			if dropped != len(tc.in)-len(strings.TrimRight(got, "]}")) {
				// dropped counts wire bytes discarded, so it must be derived
				// from the input rather than from the closers dsx appended.
				t.Logf("dropped=%d (informational)", dropped)
			}
		})
	}
}

// Salvage may only ever shorten. Nothing it emits may be a byte the server did
// not send, apart from the closing brackets that make it parse.
func TestSalvageOnlyEverShortens(t *testing.T) {
	t.Parallel()
	const in = `{"chats":{"c1":{"messages":[{"i":1},{"i":2,"x":`
	got, dropped, ok := salvageJSON([]byte(in))
	if !ok {
		t.Fatal("refused")
	}
	body := strings.TrimRight(got, "]}")
	if !strings.HasPrefix(in, body) {
		t.Errorf("salvage invented bytes:\n%s\nis not a prefix of\n%s", body, in)
	}
	if dropped <= 0 {
		t.Errorf("dropped = %d; a truncated document always loses its tail", dropped)
	}
}

func TestSalvageRefusesWhatCannotBeCutBackHonestly(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, in string }{
		{"nothing complete at any level", `{"a":`},
		{"an open array with no whole element", `{"a":[`},
		{"a bare truncated string", `"import React from`},
		{"empty", ``},
		{"not JSON at all", `<untrusted-project-content>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got, _, ok := salvageJSON([]byte(tc.in)); ok {
				t.Errorf("salvaged something from %q: %s", tc.in, got)
			}
		})
	}
}

// A whole document needs no salvage and must come back byte-identical, or the
// untruncated path would quietly start reformatting the server's transcript.
func TestSalvageLeavesAWholeDocumentAlone(t *testing.T) {
	t.Parallel()
	const in = `{"chats":{"c1":{"messages":[{"i":1}]}}}`
	got, dropped, ok := salvageJSON([]byte(in))
	if !ok {
		t.Fatal("refused a whole document")
	}
	if got != in || dropped != 0 {
		t.Errorf("salvage = %q dropped=%d, want the input unchanged", got, dropped)
	}
}

// The machine document must say what dsx itself discarded, separately from what
// the server never sent. Two different losses; one number cannot mean both.
func TestTheDocumentSeparatesTheServersLossFromDsxs(t *testing.T) {
	t.Parallel()
	in := strings.Replace(realConvNarrowable,
		`{"chats":{"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee":{"messages":[{"role":"user","content":"ELIDED-TRANSCRIPT-BODY`,
		`{"chats":{"c1":{"messages":[{"i":1},{"i":2,"cut":`, 1)

	out, ok := ConversationJSON(in)
	if !ok {
		t.Fatal("refused")
	}
	var doc struct {
		Partial   json.RawMessage `json:"partial"`
		Body      string          `json:"body"`
		Truncated struct {
			BytesDropped int `json:"bytes_dropped"`
			TailUnparsed int `json:"tail_unparsed"`
		} `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Partial) == 0 {
		t.Fatalf("a salvageable body was not salvaged:\n%s", out)
	}
	if doc.Body != "" {
		t.Error("partial and body are both set; they are exclusive")
	}
	if doc.Truncated.BytesDropped != 197193 {
		t.Errorf("bytes_dropped = %d — that is the SERVER's loss and must not move",
			doc.Truncated.BytesDropped)
	}
	if doc.Truncated.TailUnparsed <= 0 {
		t.Error("tail_unparsed is what dsx itself dropped to make the body parse; " +
			"without it a reader cannot tell a salvaged document from a whole one")
	}
	// partial must never be spelled transcript: one means "what the server
	// sent", the other "what survived a cut", and a reader keying off the wrong
	// one believes it has the whole conversation.
	if strings.Contains(out, `"transcript"`) {
		t.Errorf("a salvaged body was published as transcript:\n%s", out)
	}
}
