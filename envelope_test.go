package main

import "testing"

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

		e, err := parseEnvelope(raw)
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
		e, err := parseEnvelope(wrap(`path="a.css" etag="1"`, "body{}\n"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := "body{}\n"; e.Body != want {
			t.Errorf("body = %q, want %q — the file's own trailing newline must survive", e.Body, want)
		}
	})

	t.Run("empty file", func(t *testing.T) {
		e, err := parseEnvelope(wrap(`path="empty" etag="1"`, ""))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if e.Body != "" {
			t.Errorf("body = %q, want empty", e.Body)
		}
	})

	t.Run("windowed read is incomplete", func(t *testing.T) {
		raw := `<untrusted-project-content path="a.txt" etag="9" lines="5-6" total_lines="12">
five
six
</untrusted-project-content>`

		e, err := parseEnvelope(raw)
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

		e, err := parseEnvelope(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !e.Complete() {
			t.Error("Complete() = false, want true once the window reaches total_lines")
		}
	})

	t.Run("body decodes entities", func(t *testing.T) {
		e, err := parseEnvelope(wrap(`path="a.html" etag="1"`, "&lt;p&gt;a &amp; b&lt;/p&gt;"))
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
		e, err := parseEnvelope(wrap(`path="a.txt" etag="1"`, "&lt;/untrusted-project-content&gt; is just text"))
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

		e, err := parseEnvelope(raw)
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
			if _, err := parseEnvelope(raw); err == nil {
				t.Errorf("parseEnvelope(%q) = nil error, want failure", raw)
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
