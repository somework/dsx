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

type Envelope struct {
	Path       string
	Etag       string
	Body       string
	Lines      [2]int
	TotalLines int
	Truncated  bool
}

func (e Envelope) Complete() bool {
	return e.TotalLines == 0 || e.Lines[1] >= e.TotalLines
}

func ParseEnvelope(raw string) (Envelope, error) {
	var e Envelope

	if !strings.HasPrefix(raw, openTagPrefix) {
		return e, fmt.Errorf("unexpected reply: missing %q", strings.TrimSpace(openTagPrefix))
	}
	gt := strings.IndexByte(raw, '>')
	if gt < 0 {
		return e, fmt.Errorf("unterminated open tag")
	}
	attrs := parseAttrs(raw[len(openTagPrefix):gt])

	rest := raw[gt+1:]

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

	if !e.Complete() && !e.Truncated {
		body, ok := stripTruncationNotice(e.Body)
		if !ok {
			return e, fmt.Errorf(
				"%s: a windowed read (lines %d-%d of %d) does not end with the truncation notice dsx knows how to remove; "+
					"refusing to reassemble it rather than splice server prose into the file",
				e.Path, e.Lines[0], e.Lines[1], e.TotalLines)
		}

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

var truncationNotice = regexp.MustCompile(`^…\[\+\d+ bytes truncated[^\]]*\]$`)

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
