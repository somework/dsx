package mcp

import (
	"context"
	"fmt"
	"regexp"
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

// ParseEnvelope decodes the read_file wrapper.
//
// The server entity-escapes the body, so no raw '<' or '>' can occur inside it.
// That is what makes the closing tag an unambiguous terminator.
func ParseEnvelope(raw string) (envelope, error) {
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

	e.Body = decodeEntities(rest[:end])

	// A windowed reply carries, inside the body, a line in which the server
	// explains that it stopped early:
	//
	//	…[+54400 bytes truncated at read_file's 256 KiB cap — the body ends at
	//	a complete line; continue with offset=3856]
	//
	// That is the server talking, not the file. readFull concatenates window
	// bodies, so a notice left in place is spliced into the middle of the
	// user's file, and nothing downstream can tell it from content. It did
	// exactly that until a live read of a 316 KB file caught it.
	//
	// Measured (2026-07-17): present on every window that stops short of
	// total_lines, absent from the final one, and the content it follows always
	// ends at a complete line -- which is why concatenating windows needs no
	// separator once the notice is gone.
	if !e.Complete() && !e.Truncated {
		body, ok := stripTruncationNotice(e.Body)
		if !ok {
			return e, fmt.Errorf(
				"%s: a windowed read (lines %d-%d of %d) does not end with the truncation notice dsx knows how to remove; "+
					"refusing to reassemble it rather than splice server prose into the file",
				e.Path, e.Lines[0], e.Lines[1], e.TotalLines)
		}
		// The server's own words: "the body ends at a complete line". readFull
		// joins windows with no separator on the strength of that, so check it
		// rather than trust it. Without this, a server that kept the notice but
		// dropped the blank line before it would have the cut eat the content's
		// own final newline -- welding two windows mid-line, one byte short per
		// boundary, in silence.
		if !strings.HasSuffix(body, "\n") {
			return e, fmt.Errorf(
				"%s: a windowed read (lines %d-%d of %d) does not end at a complete line, which is what "+
					"lets dsx join windows without a separator; refusing to reassemble it",
				e.Path, e.Lines[0], e.Lines[1], e.TotalLines)
		}
		e.Body = body
	}
	return e, nil
}

// truncationNotice matches the server's trailer, and nothing else.
//
// It is anchored at both ends and tested against the body's LAST LINE ONLY.
// Both of those matter. An unanchored search would find the LEFTMOST match, and
// the notice carries exactly one ']' -- its final character -- so `[^\]]*`
// would happily span from a line of the user's own content that merely looks
// like a notice, straight through the server's real trailer, taking everything
// between them with it. A file describing read_file's own windowing is enough
// to trigger that; PROTOCOL.md is such a file.
//
// It is also deliberately narrow. If the server rewords the notice this stops
// matching and ParseEnvelope refuses the read: dsx failing loudly on files over
// 256 KiB is recoverable, dsx quietly rewriting one is not.
var truncationNotice = regexp.MustCompile(`^…\[\+\d+ bytes truncated[^\]]*\]$`)

// stripTruncationNotice removes the server's trailer and the newline before it.
//
// The trailer is always the final line, and the content it follows always ends
// at a complete line of its own -- both measured. So cutting at the last newline
// yields exactly the content, and the strip can never reach further back than
// one line however strange the file is.
func stripTruncationNotice(body string) (string, bool) {
	nl := strings.LastIndexByte(body, '\n')
	if nl < 0 {
		return body, false
	}
	if !truncationNotice.MatchString(body[nl+1:]) {
		return body, false
	}
	return body[:nl], true
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

// readFull retrieves a complete file, walking windows when the server's
// 256 KiB per-read cap truncates it.
func (c *Client) ReadFull(ctx context.Context, projectID, path string) (body string, etag string, err error) {
	var (
		sb     strings.Builder
		offset = 0
	)
	for {
		args := map[string]any{"project_id": projectID, "path": path}
		if offset > 0 {
			args["offset"] = offset
		}
		text, err := c.CallTool(ctx, "read_file", args)
		if err != nil {
			return "", "", err
		}
		env, err := ParseEnvelope(text)
		if err != nil {
			return "", "", fmt.Errorf("%s: %w", path, err)
		}
		if env.Truncated {
			return "", "", fmt.Errorf("%s: a single line exceeds the 256 KiB read cap; the server cannot return it whole", path)
		}
		if etag != "" && env.Etag != etag {
			return "", "", fmt.Errorf("%s: changed on the server mid-read (etag %s -> %s); retry", path, etag, env.Etag)
		}
		etag = env.Etag
		sb.WriteString(env.Body)

		if env.Complete() {
			return sb.String(), etag, nil
		}
		if env.Lines[1] <= offset-1 {
			return "", "", fmt.Errorf("%s: read made no progress at offset %d", path, offset)
		}
		offset = env.Lines[1] + 1
	}
}
