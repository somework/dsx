// Tests that arrived from the root's chronological review files.
//
// They are here because they reach ParseEnvelope, normalizeSSE and
// stripTruncationNotice -- the transport's internals, which stay unexported.
// Their comments travel verbatim: each records a defect that shipped, and in
// this suite that record is the load-bearing part.
//
// Their old files are organised by WHEN a defect was found, not by subject, so
// splitting them was the one place a package boundary forced a test to move for
// reasons that have nothing to do with what it proves. The rest of each file
// stayed where it was.

package mcp

import (
	"strings"
	"testing"
)

// From review_test.go.
func TestNormalizeSSEAcceptsEveryFrameTheGrammarAllows(t *testing.T) {
	// The entry guard demanded the body open with "event:" or "data:", but the
	// SSE grammar the parse loop itself cites allows a comment, an id: or a
	// retry: first — and servers send `: ping` as a keepalive. Such a stream
	// was returned raw and died as "malformed response", non-retryable.
	want := `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`
	cases := []struct {
		name string
		body string
	}{
		{"comment first", ": ping\n\nevent: message\ndata: " + want + "\n\n"},
		{"id first", "id: 1\nevent: message\ndata: " + want + "\n\n"},
		{"retry first", "retry: 3000\n\ndata: " + want + "\n\n"},
		{"plain data", "data: " + want + "\n\n"},
		{"crlf", "event: message\r\ndata: " + want + "\r\n\r\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(normalizeSSE([]byte(tc.body), "text/event-stream"))
			if got != want {
				t.Errorf("normalizeSSE = %q, want %q", got, want)
			}
		})
	}

	// Plain JSON must still pass through untouched, whatever the header says.
	plain := []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)
	if got := string(normalizeSSE(plain, "application/json")); got != string(plain) {
		t.Errorf("plain JSON was mangled: %q", got)
	}
}

// From review_test.go.
func TestTruncationStripCannotReachIntoAFilesOwnContent(t *testing.T) {
	// The strip regexp was unanchored, and FindStringIndex returns the LEFTMOST
	// match. The server's notice contains exactly one ']' — its final character
	// — so `[^\]]*` would span from a *user's* line that merely looks like a
	// notice, straight through the server's real trailer, and body[:loc[0]]
	// deleted everything in between.
	//
	// The file that triggers it is not exotic: PROTOCOL.md itself documents the
	// notice, and any file over 256 KiB describing read_file's windowing hits it.
	// `dsx pull` refuses it (invariant 1 catches the length mismatch), so the
	// file becomes unpullable forever; `dsx cat` wrote the damage out.
	content := "# how read_file windows a big file\n" +
		"a windowed reply ends with a line like:\n" +
		"…[+54400 bytes truncated at the cap — continue\n" + // no ']' anywhere after
		"tail line one\n" +
		"tail line two\n"
	raw := `<untrusted-project-content path="notes.md" etag="1" lines="1-5" total_lines="9">` +
		"\n" + content + liveWindowNotice + "\n</untrusted-project-content>"

	e, err := ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if e.Body != content {
		t.Fatalf("the strip ate the file's own content:\n want %q\n  got %q", content, e.Body)
	}
}

// From review2_test.go.
func TestNormalizeSSEUnwrapsAStreamWhateverTheHeaderSays(t *testing.T) {
	// Deciding on the Content-Type alone traded one failure class for another:
	// it repaired three inputs and broke four that the old body sniff handled —
	// an SSE body with an absent, wrong, or text/plain header. Every measured
	// reply from this endpoint is application/json, so a server that started
	// framing without relabelling would have died as "malformed response".
	//
	// Both signals are now accepted. A JSON-RPC reply always opens with '{', so
	// the body sniff has no false positives.
	want := `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`
	bodies := map[string]string{
		"comment first": ": ping\n\nevent: message\ndata: " + want + "\n\n",
		"id first":      "id: 1\nevent: message\ndata: " + want + "\n\n",
		"retry first":   "retry: 3000\n\ndata: " + want + "\n\n",
		"plain data":    "data: " + want + "\n\n",
	}
	headers := []string{"text/event-stream", "text/event-stream; charset=utf-8", "application/json", "text/plain", ""}

	for name, body := range bodies {
		for _, h := range headers {
			t.Run(name+" via "+h, func(t *testing.T) {
				if got := string(normalizeSSE([]byte(body), h)); got != want {
					t.Errorf("normalizeSSE(_, %q) = %q, want the response frame", h, got)
				}
			})
		}
	}

	// Plain JSON is never mistaken for a stream, whatever the header claims.
	plain := `{"jsonrpc":"2.0","id":1,"result":{}}`
	for _, h := range headers {
		if got := string(normalizeSSE([]byte(plain), h)); got != plain {
			t.Errorf("plain JSON mangled under %q: %q", h, got)
		}
	}
}

// From review3_test.go.
func TestTruncationStripRefusesAFramingItCannotAccountFor(t *testing.T) {
	// stripTruncationNotice cuts at the last newline, which is right for the
	// framing measured on 2026-07-17: content ends at a complete line, then a
	// blank line, then the notice. The "fail loud if the server changes" promise
	// covered the notice's WORDING but not its FRAMING — if the server keeps the
	// notice and drops the blank separator, the cut eats the content's own final
	// newline and readFull welds two windows mid-line, silently and one byte
	// short per boundary.
	//
	// "The body ends at a complete line" is the server's own claim, so asserting
	// it costs nothing and turns that into a refusal.
	raw := `<untrusted-project-content path="big.txt" etag="1" lines="1-2" total_lines="9">` +
		"\nl1\nl2" + // no trailing newline: the separator is gone
		liveWindowNotice + "\n</untrusted-project-content>"

	_, err := ParseEnvelope(raw)
	if err == nil {
		t.Fatal("a windowed body whose content does not end at a complete line was accepted; " +
			"readFull would weld the next window onto the end of line 2")
	}
	if !strings.Contains(err.Error(), "complete line") {
		t.Errorf("the error should name what it could not account for: %v", err)
	}
}

// From review3_test.go.
func TestTruncationStripRefusesANoticeOnlyBody(t *testing.T) {
	raw := `<untrusted-project-content path="big.txt" etag="1" lines="0-0" total_lines="9">` +
		"\n…[+9 bytes truncated at read_file's cap; continue with offset=1]" +
		"\n</untrusted-project-content>"
	if _, err := ParseEnvelope(raw); err == nil {
		t.Fatal("a window carrying nothing but the notice was accepted as an empty body")
	}
}
