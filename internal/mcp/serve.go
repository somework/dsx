package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/somework/dsx/internal/fmtutil"
)

// serveHostSuffix is the host render_preview names against the real endpoint:
// https://<project-uuid>.claudeusercontent.com/v1/design/projects/<id>/serve/<path>.
const serveHostSuffix = ".claudeusercontent.com"

// serveUserAgent is sent instead of Go's default. Measured 2026-07-23: the
// preview host is behind Cloudflare, which answers `Python-urllib/*` with 403
// "error code: 1010" and lets an absent, empty, curl, requests or Go-default
// User-Agent through. Naming dsx is honest and takes the value out of the
// hands of whatever the runtime happens to send.
const serveUserAgent = "dsx"

// allowServeHost decides whether a serve_url may be fetched at all.
//
// render_preview's reply is server-controlled, so an endpoint pointed
// elsewhere by DSX_ENDPOINT can name any host it likes and dsx would GET it
// and write the answer to the user's disk. Exactly two hosts are acceptable.
// The real preview host, matched by suffix on the hostname (never the string,
// or an attacker.com/.claudeusercontent.com path would pass). And the
// endpoint's own host, which is already the host dsx sends its bearer token
// to — fetching bytes from it grants nothing dsx has not already granted, and
// it is what lets a fake endpoint serve its own preview lane.
func allowServeHost(serve, endpoint *url.URL) bool {
	if serve.User != nil {
		return false
	}
	host := strings.ToLower(serve.Hostname())
	if host == "" {
		return false
	}
	if serve.Scheme == "https" && strings.HasSuffix(host, serveHostSuffix) {
		return true
	}
	return endpoint != nil &&
		serve.Scheme == endpoint.Scheme &&
		host == strings.ToLower(endpoint.Hostname()) &&
		serve.Port() == endpoint.Port()
}

// IsBinaryRefusal reports whether an error is read_file declining a path
// because its bytes are not valid UTF-8. It is the discovery point for a
// binary: nothing in list_files says which paths are one, and the extension
// says nothing either — the server decides by content.
func IsBinaryRefusal(err error) bool {
	var te *ToolError
	if !errors.As(err, &te) {
		return false
	}
	return strings.Contains(te.Text, "is a binary file")
}

// ReadBinary returns the stored bytes of one file through the preview lane —
// the only server→disk route for a file read_file refuses, since "binary" here
// means "not valid UTF-8" and read_file serves the text lane alone.
//
// The serve_url this mints is a credential: a *.claudeusercontent.com link
// carrying a token scoped to the whole PROJECT, not to the path, and good for
// about an hour with no Authorization header of its own. It is born and dies
// inside this function on purpose — it reaches no caller, no report, no error
// text and no file, so there is exactly one place to audit.
//
// maxBytes is the size list_files reports. It bounds the read so a runaway or
// substituted body cannot be swallowed whole; the caller still has to assert
// exact equality (invariant 1), which is also what rejects the ~16 KiB preview
// harness the server prepends to a .html.
func (c *Client) ReadBinary(ctx context.Context, projectID, path string, maxBytes int64) ([]byte, error) {
	text, err := c.CallTool(ctx, "render_preview", map[string]any{
		"project_id": projectID, "path": path,
	})
	if err != nil {
		return nil, err
	}
	var reply struct {
		ServeURL string `json:"serve_url"`
	}
	if err := json.Unmarshal([]byte(text), &reply); err != nil || reply.ServeURL == "" {
		return nil, fmt.Errorf("%s: render_preview returned no serve_url", path)
	}
	u, err := url.Parse(reply.ServeURL)
	if err != nil {
		return nil, fmt.Errorf("%s: render_preview returned an unparseable serve_url", path)
	}
	if !allowServeHost(u, c.endpointURL) {
		// The hostname alone, never the URL: the token rides in the query.
		return nil, fmt.Errorf(
			"%s: render_preview named preview host %q, which is neither the endpoint's own host nor *%s; refusing to fetch",
			path, fmtutil.Printable(u.Hostname()), serveHostSuffix)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%s: preview request: %w", path, err)
	}
	req.Header.Set("User-Agent", serveUserAgent)

	resp, err := c.serveHTTP.Do(req)
	if err != nil {
		// Never wrap: http returns a *url.Error, whose Error() prints the URL
		// it was fetching — and that URL is the credential. The one thing
		// worth telling apart is a redirect, which is a protocol change rather
		// than a network fault.
		if errors.Is(err, errServeRedirect) {
			return nil, fmt.Errorf("%s: %w", path, errServeRedirect)
		}
		if ce := ctx.Err(); ce != nil {
			return nil, fmt.Errorf("%s: preview fetch: %w", path, ce)
		}
		return nil, fmt.Errorf("%s: preview fetch failed", path)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		short, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("%s: preview host answered %d %s",
			path, resp.StatusCode, fmtutil.Truncate(fmtutil.Printable(strings.TrimSpace(string(short))), 120))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s: reading the preview body: %w", path, err)
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("%s: preview served more than the %d bytes list_files reports", path, maxBytes)
	}
	return body, nil
}
