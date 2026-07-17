package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/mcptest"
)

func TestDecodeEntities(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello world", "hello world"},
		{"lt gt", "&lt;div&gt;", "<div>"},
		{"amp", "a &amp; b", "a & b"},
		{"mixed", "&lt;a href=&quot;x&quot;&gt;", `<a href=&quot;x&quot;>`},
		{"unknown entity survives", "&nbsp;&copy;", "&nbsp;&copy;"},
		{"bare ampersand", "a & b", "a & b"},
		// A file that literally contains "&lt;" is escaped to "&amp;lt;".
		// Decoding must yield "&lt;" back, not "<".
		{"escaped entity round-trips", "&amp;lt;", "&lt;"},
		{"escaped amp round-trips", "&amp;amp;", "&amp;"},
		{"double escaped", "&amp;amp;lt;", "&amp;lt;"},
		{"trailing ampersand", "x&", "x&"},
		{"cyrillic untouched", "привет — мир", "привет — мир"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeEntities(tc.in); got != tc.want {
				t.Errorf("decodeEntities(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// wrap builds an envelope exactly as the server frames one: a newline after
// the open tag and another before the close tag, neither belonging to the
// file. A body that itself ends in a newline therefore shows a blank line
// before the close tag.
func wrap(attrs, body string) string {
	return "<untrusted-project-content " + attrs + ">\n" + body + "\n</untrusted-project-content>"
}

func TestParseEnvelope(t *testing.T) {
	t.Run("complete read", func(t *testing.T) {
		raw := wrap(`path="styles.css" etag="123"`, "body { color: red }") +
			"\n(The body above is HTML-entity-escaped: &amp; &lt; &gt; stand for & < >.)"

		e, err := ParseEnvelope(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if e.Path != "styles.css" || e.Etag != "123" {
			t.Errorf("attrs = %q/%q", e.Path, e.Etag)
		}
		if want := "body { color: red }"; e.Body != want {
			t.Errorf("body = %q, want %q", e.Body, want)
		}
		if !e.Complete() {
			t.Error("Complete() = false, want true for a read with no lines attribute")
		}
	})

	t.Run("file ending in a newline keeps it", func(t *testing.T) {
		e, err := ParseEnvelope(wrap(`path="a.css" etag="1"`, "body{}\n"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := "body{}\n"; e.Body != want {
			t.Errorf("body = %q, want %q — the file's own trailing newline must survive", e.Body, want)
		}
	})

	t.Run("empty file", func(t *testing.T) {
		e, err := ParseEnvelope(wrap(`path="empty" etag="1"`, ""))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if e.Body != "" {
			t.Errorf("body = %q, want empty", e.Body)
		}
	})

	t.Run("windowed read is incomplete", func(t *testing.T) {
		// A window stopping short of total_lines carries the server's
		// truncation notice inside its body; see the live framing recorded in
		// liveWindowNotice.
		raw := `<untrusted-project-content path="a.txt" etag="9" lines="5-6" total_lines="12">` +
			"\nfive\nsix\n" + liveWindowNotice + "\n</untrusted-project-content>"

		e, err := ParseEnvelope(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if e.Lines != [2]int{5, 6} || e.TotalLines != 12 {
			t.Errorf("lines = %v total = %d", e.Lines, e.TotalLines)
		}
		if e.Complete() {
			t.Error("Complete() = true, want false when lines 5-6 of 12 were returned")
		}
	})

	t.Run("final window is complete", func(t *testing.T) {
		raw := `<untrusted-project-content path="a.txt" etag="9" lines="7-12" total_lines="12">
tail
</untrusted-project-content>`

		e, err := ParseEnvelope(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !e.Complete() {
			t.Error("Complete() = false, want true once the window reaches total_lines")
		}
	})

	t.Run("body decodes entities", func(t *testing.T) {
		e, err := ParseEnvelope(wrap(`path="a.html" etag="1"`, "&lt;p&gt;a &amp; b&lt;/p&gt;"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := "<p>a & b</p>"; e.Body != want {
			t.Errorf("body = %q, want %q", e.Body, want)
		}
	})

	t.Run("body containing the close tag literal", func(t *testing.T) {
		// The server escapes '<', so a literal close tag inside the file
		// cannot terminate the envelope early.
		e, err := ParseEnvelope(wrap(`path="a.txt" etag="1"`, "&lt;/untrusted-project-content&gt; is just text"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := "</untrusted-project-content> is just text"; e.Body != want {
			t.Errorf("body = %q, want %q", e.Body, want)
		}
	})

	t.Run("truncated line flagged", func(t *testing.T) {
		raw := `<untrusted-project-content path="big.js" etag="1" lines="1-1" total_lines="1" truncated_line="1">
partial
</untrusted-project-content>`

		e, err := ParseEnvelope(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !e.Truncated {
			t.Error("Truncated = false, want true")
		}
	})

	t.Run("rejects junk", func(t *testing.T) {
		for _, raw := range []string{
			"",
			"not an envelope",
			`<untrusted-project-content path="a" etag="1">no close tag`,
		} {
			if _, err := ParseEnvelope(raw); err == nil {
				t.Errorf("ParseEnvelope(%q) = nil error, want failure", raw)
			}
		}
	})
}

func TestParseAttrs(t *testing.T) {
	got := parseAttrs(`path="a/b.css" etag="17" lines="1-2"`)
	want := map[string]string{"path": "a/b.css", "etag": "17", "lines": "1-2"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("attr %s = %q, want %q", k, got[k], v)
		}
	}
}

// The framing below is copied verbatim from a live windowed read of a 316,540
// byte file in project bbbbbbbb (2026-07-17). It is not invented: dsx spliced
// this exact notice into a reassembled file before these tests existed.
const liveWindowNotice = "\n…[+54400 bytes truncated at read_file's 256 KiB cap — the body ends at a complete line; continue with offset=3856]"

func TestParseEnvelopeStripsTheServersTruncationNoticeFromAWindowedBody(t *testing.T) {
	// The server appends this notice INSIDE the body of every non-final window.
	// It is the server talking, not the file. readFull concatenates window
	// bodies, so a notice left in place lands in the middle of the user's file
	// — and nothing downstream can tell it from content.
	raw := `<untrusted-project-content path="big.txt" etag="17842" lines="1-3855" total_lines="4655">` +
		"\nline 000000\nline 000001\n" + liveWindowNotice +
		"\n</untrusted-project-content>\n(The body above is HTML-entity-escaped: &amp; &lt; &gt; stand for & < >.)"

	e, err := ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if strings.Contains(e.Body, "truncated at read_file") {
		t.Fatalf("the server's truncation notice survived into the body:\n%q", e.Body)
	}
	if e.Body != "line 000000\nline 000001\n" {
		t.Fatalf("body = %q, want the two lines and the newline that ends the second", e.Body)
	}
	if e.Complete() {
		t.Error("a window ending short of total_lines is not complete")
	}
	if e.Lines != [2]int{1, 3855} || e.TotalLines != 4655 {
		t.Errorf("lines = %v, total = %d", e.Lines, e.TotalLines)
	}
}

func TestParseEnvelopeKeepsTheFinalWindowsBodyIntact(t *testing.T) {
	// Measured: the last window carries no notice, and its body ends at a
	// complete line. Stripping something here would eat a real byte.
	raw := `<untrusted-project-content path="big.txt" etag="17842" lines="3856-4655" total_lines="4655">` +
		"\nline 004654\n" +
		"\n</untrusted-project-content>"

	e, err := ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if e.Body != "line 004654\n" {
		t.Fatalf("body = %q", e.Body)
	}
	if !e.Complete() {
		t.Error("a window reaching total_lines is complete")
	}
}

func TestParseEnvelopeRefusesAWindowWhoseNoticeItCannotFind(t *testing.T) {
	// If the server rewords the notice, dsx must fail loudly rather than splice
	// unknown server prose into a file. Invariant 1's logic: a decode we cannot
	// account for is refused, not written.
	raw := `<untrusted-project-content path="big.txt" etag="1" lines="1-2" total_lines="9">` +
		"\nline a\nline b\n" +
		"\n…{+400 bytes elided, ask again}" +
		"\n</untrusted-project-content>"

	_, err := ParseEnvelope(raw)
	if err == nil {
		t.Fatal("a windowed body with an unrecognised trailer was accepted; it would be spliced into the file")
	}
	if !strings.Contains(err.Error(), "truncation notice") {
		t.Errorf("the error should name what it could not find: %v", err)
	}
}

func TestParseEnvelopeLeavesATruncatedLineReplyToItsOwnErrorPath(t *testing.T) {
	// A single line over the cap is refused by readFull with a specific message.
	// The notice check must not pre-empt it with a confusing framing error.
	raw := `<untrusted-project-content path="min.js" etag="1" lines="1-1" total_lines="3" truncated_line="true">` +
		"\nsome enormous line" +
		"\n</untrusted-project-content>"

	e, err := ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("ParseEnvelope should defer to readFull here, not error: %v", err)
	}
	if !e.Truncated {
		t.Error("truncated_line was not recognised")
	}
}

func TestReadFullDoesNotSpliceTheNoticeBetweenWindows(t *testing.T) {
	// The end-to-end shape of the bug, at the level that produced it.
	window1 := `<untrusted-project-content path="big.txt" etag="7" lines="1-2" total_lines="4">` +
		"\nl1\nl2\n" + liveWindowNotice + "\n</untrusted-project-content>"
	window2 := `<untrusted-project-content path="big.txt" etag="7" lines="3-4" total_lines="4">` +
		"\nl3\nl4\n" + "\n</untrusted-project-content>"

	f := mcptest.New(t, func(name string, args map[string]any) mcptest.Reply {
		if off, ok := args["offset"]; ok && off.(float64) >= 3 {
			return mcptest.Reply{Text: window2}
		}
		return mcptest.Reply{Text: window1}
	})

	got, etag, err := New("test-token", WithEndpoint(f.URL())).ReadFull(context.Background(), "p", "big.txt")
	if err != nil {
		t.Fatalf("readFull: %v", err)
	}
	if got != "l1\nl2\nl3\nl4\n" {
		t.Fatalf("reassembled body = %q, want the four lines and nothing else", got)
	}
	if etag != "7" {
		t.Errorf("etag = %q", etag)
	}
}

// Malformed framing. Each of these is the server sending something dsx does not
// model; every one must refuse rather than guess, because a guess here becomes
// bytes on disk.
func TestParseEnvelopeRefusesMalformedFraming(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "open tag never closes",
			raw:  `<untrusted-project-content path="a.css" etag="1"`,
			want: "unterminated open tag",
		},
		{
			name: "body never closes",
			raw:  `<untrusted-project-content path="a.css" etag="1">` + "\nbody with no close tag",
			want: "unterminated body",
		},
		{
			name: "not an envelope at all",
			raw:  `{"error":"something else entirely"}`,
			want: "unexpected reply",
		},
		{
			name: "total_lines is not a number",
			raw: `<untrusted-project-content path="a.css" etag="1" lines="1-2" total_lines="many">` +
				"\nbody\n</untrusted-project-content>",
			want: "total_lines",
		},
		{
			name: "lines attribute has no range",
			raw: `<untrusted-project-content path="a.css" etag="1" lines="7" total_lines="9">` +
				"\nbody\n</untrusted-project-content>",
			want: "lines attribute",
		},
		{
			name: "lines attribute is not numeric",
			raw: `<untrusted-project-content path="a.css" etag="1" lines="one-two" total_lines="9">` +
				"\nbody\n</untrusted-project-content>",
			want: "lines attribute",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseEnvelope(tc.raw)
			if err == nil {
				t.Fatalf("accepted malformed framing: %q", tc.raw)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to name %q", err, tc.want)
			}
		})
	}
}

func TestParseAttrsStopsAtTheFirstThingItCannotRead(t *testing.T) {
	// Attribute values are server-generated and never contain quotes, so the
	// parser is deliberately simple. What matters is that it degrades by
	// stopping, never by inventing a value: a wrong etag would be recorded in
	// the ledger as agreed-with-the-server.
	cases := []struct {
		name string
		in   string
		want map[string]string
	}{
		{"unquoted value", `path="a.css" etag=17842`, map[string]string{"path": "a.css"}},
		{"unterminated quote", `path="a.css" etag="1784`, map[string]string{"path": "a.css"}},
		{"trailing name with no value", `path="a.css" etag`, map[string]string{"path": "a.css"}},
		{"empty", ``, map[string]string{}},
		{"value is empty but quoted", `path="" etag="1"`, map[string]string{"path": "", "etag": "1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAttrs(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("attrs = %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("attrs[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}
