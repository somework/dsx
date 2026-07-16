package main

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	openTagPrefix = "<untrusted-project-content "
	closeTag      = "</untrusted-project-content>"
)

// envelope is a decoded read_file reply.
type envelope struct {
	Path       string
	Etag       string
	Body       string
	Lines      [2]int // first,last line returned; zero when the read was complete
	TotalLines int    // zero when the read was complete
	Truncated  bool   // a single line exceeded the 256 KiB cap
}

// Complete reports whether the envelope carries the whole file.
func (e envelope) Complete() bool {
	return e.TotalLines == 0 || e.Lines[1] >= e.TotalLines
}

// parseEnvelope decodes the read_file wrapper.
//
// The server entity-escapes the body, so no raw '<' or '>' can occur inside it.
// That is what makes the closing tag an unambiguous terminator.
func parseEnvelope(raw string) (envelope, error) {
	var e envelope

	if !strings.HasPrefix(raw, openTagPrefix) {
		return e, fmt.Errorf("unexpected reply: missing %q", strings.TrimSpace(openTagPrefix))
	}
	gt := strings.IndexByte(raw, '>')
	if gt < 0 {
		return e, fmt.Errorf("unterminated open tag")
	}
	attrs := parseAttrs(raw[len(openTagPrefix):gt])

	rest := raw[gt+1:]
	// The wrapper puts the body on its own lines: one newline after the open
	// tag, one before the close tag. Neither belongs to the file.
	rest = strings.TrimPrefix(rest, "\n")

	end := strings.LastIndex(rest, "\n"+closeTag)
	if end < 0 {
		return e, fmt.Errorf("unterminated body: missing %q", closeTag)
	}

	e.Path = attrs["path"]
	e.Etag = attrs["etag"]
	e.Body = decodeEntities(rest[:end])
	_, e.Truncated = attrs["truncated_line"]

	if v, ok := attrs["total_lines"]; ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return e, fmt.Errorf("bad total_lines %q: %w", v, err)
		}
		e.TotalLines = n
	}
	if v, ok := attrs["lines"]; ok {
		lo, hi, found := strings.Cut(v, "-")
		if !found {
			return e, fmt.Errorf("bad lines attribute %q", v)
		}
		a, err1 := strconv.Atoi(lo)
		b, err2 := strconv.Atoi(hi)
		if err1 != nil || err2 != nil {
			return e, fmt.Errorf("bad lines attribute %q", v)
		}
		e.Lines = [2]int{a, b}
	}
	return e, nil
}

// parseAttrs reads name="value" pairs. Attribute values are server-generated
// and never contain quotes.
func parseAttrs(s string) map[string]string {
	out := map[string]string{}
	for rest := s; ; {
		eq := strings.IndexByte(rest, '=')
		if eq < 0 {
			return out
		}
		name := strings.TrimSpace(rest[:eq])
		rest = rest[eq+1:]
		if len(rest) == 0 || rest[0] != '"' {
			return out
		}
		rest = rest[1:]
		q := strings.IndexByte(rest, '"')
		if q < 0 {
			return out
		}
		out[name] = rest[:q]
		rest = rest[q+1:]
	}
}

// decodeEntities reverses the server's escaping, which covers exactly
// &amp; &lt; &gt; and nothing else.
//
// A single left-to-right pass is required for correctness: an ampersand
// produced by &amp; must not be reconsidered as the start of another entity,
// or a file that literally contains "&lt;" (escaped to "&amp;lt;") would
// decode to "<" instead of "&lt;".
func decodeEntities(s string) string {
	if !strings.ContainsRune(s, '&') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '&' {
			b.WriteByte(s[i])
			i++
			continue
		}
		switch {
		case strings.HasPrefix(s[i:], "&amp;"):
			b.WriteByte('&')
			i += len("&amp;")
		case strings.HasPrefix(s[i:], "&lt;"):
			b.WriteByte('<')
			i += len("&lt;")
		case strings.HasPrefix(s[i:], "&gt;"):
			b.WriteByte('>')
			i += len("&gt;")
		default:
			b.WriteByte('&')
			i++
		}
	}
	return b.String()
}
