package mcp

import (
	"strings"
	"testing"
)

func TestNormalizeSSEAcceptsEveryFrameTheGrammarAllows(t *testing.T) {
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

	plain := []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)
	if got := string(normalizeSSE(plain, "application/json")); got != string(plain) {
		t.Errorf("plain JSON was mangled: %q", got)
	}
}

func TestTruncationStripCannotReachIntoAFilesOwnContent(t *testing.T) {
	content := "# how read_file windows a big file\n" +
		"a windowed reply ends with a line like:\n" +
		"…[+54400 bytes truncated at the cap — continue\n" +
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

func TestNormalizeSSEUnwrapsAStreamWhateverTheHeaderSays(t *testing.T) {
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

	plain := `{"jsonrpc":"2.0","id":1,"result":{}}`
	for _, h := range headers {
		if got := string(normalizeSSE([]byte(plain), h)); got != plain {
			t.Errorf("plain JSON mangled under %q: %q", h, got)
		}
	}
}

func TestTruncationStripRefusesAFramingItCannotAccountFor(t *testing.T) {
	raw := `<untrusted-project-content path="big.txt" etag="1" lines="1-2" total_lines="9">` +
		"\nl1\nl2" +
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

func TestTruncationStripRefusesANoticeOnlyBody(t *testing.T) {
	raw := `<untrusted-project-content path="big.txt" etag="1" lines="0-0" total_lines="9">` +
		"\n…[+9 bytes truncated at read_file's cap; continue with offset=1]" +
		"\n</untrusted-project-content>"
	if _, err := ParseEnvelope(raw); err == nil {
		t.Fatal("a window carrying nothing but the notice was accepted as an empty body")
	}
}
