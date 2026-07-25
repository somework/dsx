package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/fmtutil"
)

const (
	defaultEndpoint = "https://api.anthropic.com/v1/design/mcp"
	protocolVersion = "2025-06-18"
	maxAttempts     = 4
)

type Client struct {
	endpoint    string
	token       string
	http        *http.Client
	retryNotice io.Writer
	seq         atomic.Int64

	// The parsed endpoint, for allowServeHost. Nil if the endpoint does not
	// parse, which allowServeHost reads as "trust nothing but the real
	// preview host" rather than as permission.
	endpointURL *url.URL

	// A second client for the preview lane, deliberately not c.http: it must
	// carry no Authorization (the preview host neither needs nor should see
	// the OAuth token) and must follow no redirect, so a moved preview host
	// cannot walk dsx off the two hosts allowServeHost admits.
	serveHTTP *http.Client

	lastServerDate atomic.Int64
}

type Option func(*Client)

func WithEndpoint(url string) Option {
	return func(c *Client) { c.endpoint = url }
}

func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// WithRetryNotice names where a retry announces itself. It sits above the
// transfer counter because it covers every command, and because it is what
// distinguishes "working, but repeating" from "hung" — the distinction that
// decides whether the reader reaches for Ctrl-C, which invariant 3 makes a
// full failure rather than a short success.
func WithRetryNotice(w io.Writer) Option {
	return func(c *Client) { c.retryNotice = w }
}

func New(token string, opts ...Option) *Client {
	ep := defaultEndpoint
	if v := os.Getenv("DSX_ENDPOINT"); v != "" {
		ep = v
	}
	c := &Client{
		endpoint: ep,
		token:    token,
		http: &http.Client{
			Timeout: 120 * time.Second,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 32,
				ForceAttemptHTTP2:   true,
			},
		},
	}
	for _, o := range opts {
		o(c)
	}
	// After the options, so WithEndpoint is accounted for.
	c.endpointURL, _ = url.Parse(c.endpoint)
	c.serveHTTP = &http.Client{
		Timeout: 120 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errServeRedirect
		},
	}
	return c
}

// errServeRedirect stops the preview client at the first redirect. It carries
// no URL: http wraps a CheckRedirect error in a *url.Error, whose Error()
// prints the URL it was following — and that URL is the credential.
var errServeRedirect = errors.New("the preview host redirected; dsx does not follow it")

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

type ToolError struct {
	Tool string
	Text string
}

func (e *ToolError) Error() string { return e.Tool + ": " + e.Text }

type ServerConflict struct {
	Conflicts []struct {
		Path string `json:"path"`
		Etag string `json:"etag"`
	} `json:"conflicts"`
	Message string `json:"message"`
}

func ConflictFromToolError(err error) ([]string, bool) {
	var te *ToolError
	if !errors.As(err, &te) {
		return nil, false
	}
	var sc ServerConflict
	if json.Unmarshal([]byte(te.Text), &sc) != nil || len(sc.Conflicts) == 0 {
		return nil, false
	}
	paths := make([]string, 0, len(sc.Conflicts))
	for _, c := range sc.Conflicts {
		paths = append(paths, c.Path)
	}
	return paths, true
}

type GrantError struct{ ProjectID string }

func (e *GrantError) Error() string {
	return "needs_project_grant for project " + e.ProjectID +
		"; mint a token with `dsx plan new " + e.ProjectID + " --writes <paths>` and pass plan_token"
}

var readOnlyTools = map[string]bool{
	"list_projects":            true,
	"list_files":               true,
	"list_design_systems":      true,
	"list_members":             true,
	"get_project":              true,
	"get_conversation":         true,
	"get_claude_design_prompt": true,
	"read_file":                true,
	"list_comments":            true,
	"read_design_skill":        true,
}

func (c *Client) rpc(ctx context.Context, method string, params any, idempotent bool) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      c.seq.Add(1),
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := range maxAttempts {
		if attempt > 0 {
			if c.retryNotice != nil {
				fmt.Fprintf(c.retryNotice, "dsx: retrying (%d/%d) after a transport fault\n",
					attempt+1, maxAttempts)
			}
			delay := time.Duration(1<<attempt)*250*time.Millisecond +
				time.Duration(rand.IntN(200))*time.Millisecond
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		raw, retryable, err := c.attempt(ctx, body, idempotent)
		if err == nil {
			return raw, nil
		}
		lastErr = err
		if !retryable {
			return nil, err
		}
	}
	return nil, fmt.Errorf("after %d attempts: %w", maxAttempts, lastErr)
}

func (c *Client) attempt(ctx context.Context, body []byte, idempotent bool) (raw json.RawMessage, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, idempotent, &dsxerr.Error{Kind: dsxerr.KindTransport, Msg: "request failed", Err: err}
	}
	defer resp.Body.Close()

	if d, parseErr := http.ParseTime(resp.Header.Get("Date")); parseErr == nil {
		c.lastServerDate.Store(d.UnixNano())
	}

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, idempotent, &dsxerr.Error{Kind: dsxerr.KindTransport, Msg: "reading the reply failed", Err: err}
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, false, &dsxerr.Error{Kind: dsxerr.KindAuth,
			Msg: "401 unauthorized — token rejected; run any `claude` command to refresh (or set DSX_TOKEN), then retry"}
	case resp.StatusCode == http.StatusForbidden && bytes.Contains(payload, []byte("needs_project_grant")):
		var g struct {
			ProjectID string `json:"project_id"`
		}
		_ = json.Unmarshal(payload, &g)
		return nil, false, &GrantError{ProjectID: g.ProjectID}
	case resp.StatusCode == http.StatusForbidden:
		// Not needs_project_grant — that is peeled off above and is the one
		// 403 dsx recovers on its own. What is left is an authorisation
		// refusal of the whole request: the token is valid (a bad one is 401),
		// but this account may never have authorised Claude Design. That gate
		// is granted once, out of band, by `/design consent` in Claude Code —
		// the one step dsx cannot take for the user — so this is KindAuth like
		// the 401, not the KindProtocol a bare non-200 would get. Matched on
		// the status alone: the body of a consent refusal is unmeasured, so
		// nothing here reads it beyond echoing the server's own words.
		return nil, false, &dsxerr.Error{Kind: dsxerr.KindAuth,
			Msg: fmt.Sprintf("403 forbidden — token accepted but the request was refused; "+
				"if this account has not used Claude Design, run `/design consent` in Claude Code to grant it access. Server said: %s",
				fmtutil.Truncate(string(payload), 200))}
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, true, &dsxerr.Error{Kind: dsxerr.KindTransport,
			Msg: fmt.Sprintf("http %d: %s", resp.StatusCode, fmtutil.Truncate(string(payload), 200))}
	case resp.StatusCode >= 500:
		return nil, idempotent, &dsxerr.Error{Kind: dsxerr.KindTransport,
			Msg: fmt.Sprintf("http %d: %s", resp.StatusCode, fmtutil.Truncate(string(payload), 200))}
	case resp.StatusCode != http.StatusOK:
		return nil, false, &dsxerr.Error{Kind: dsxerr.KindProtocol,
			Msg: fmt.Sprintf("http %d: %s", resp.StatusCode, fmtutil.Truncate(string(payload), 400))}
	}

	var out rpcResponse
	if err := json.Unmarshal(normalizeSSE(payload, resp.Header.Get("Content-Type")), &out); err != nil {
		return nil, false, &dsxerr.Error{Kind: dsxerr.KindProtocol, Msg: "malformed response", Err: err}
	}
	if out.Error != nil {
		return nil, false, &dsxerr.Error{Kind: dsxerr.KindProtocol,
			Msg: fmt.Sprintf("rpc %d: %s", out.Error.Code, out.Error.Message)}
	}
	return out.Result, false, nil
}

func normalizeSSE(b []byte, contentType string) []byte {
	if !isEventStream(contentType, b) {
		return b
	}

	var (
		frames [][]byte
		lines  []string
	)
	flush := func() {
		if len(lines) > 0 {
			frames = append(frames, []byte(strings.Join(lines, "\n")))
			lines = nil
		}
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			flush()
			continue
		}
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			lines = append(lines, strings.TrimPrefix(after, " "))
		}
	}
	flush()

	for _, f := range frames {
		var probe struct {
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(f, &probe) != nil {
			continue
		}
		if len(probe.Result) > 0 || len(probe.Error) > 0 {
			return f
		}
	}
	if len(frames) > 0 {
		return frames[len(frames)-1]
	}
	return b
}

func isEventStream(contentType string, body []byte) bool {
	mediaType, _, _ := strings.Cut(contentType, ";")
	if strings.EqualFold(strings.TrimSpace(mediaType), "text/event-stream") {
		return true
	}
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	for _, field := range [][]byte{
		[]byte("data:"), []byte("event:"), []byte("id:"), []byte("retry:"), []byte(":"),
	} {
		if bytes.HasPrefix(trimmed, field) {
			return true
		}
	}
	return false
}

func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	raw, err := c.rpc(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, readOnlyTools[name])
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	var res toolResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", &dsxerr.Error{Kind: dsxerr.KindProtocol, Msg: name + ": malformed tool result", Err: err}
	}

	var sb strings.Builder
	for _, part := range res.Content {
		sb.WriteString(part.Text)
	}
	if res.IsError {
		return "", &ToolError{Tool: name, Text: strings.TrimSpace(sb.String())}
	}
	return sb.String(), nil
}

func (c *Client) Endpoint() string { return c.endpoint }

func (c *Client) LastServerDate() int64 { return c.lastServerDate.Load() }

func (c *Client) ToolsList(ctx context.Context) (json.RawMessage, error) {
	return c.rpc(ctx, "tools/list", map[string]any{}, true)
}

func ReadOnlyToolNames() []string {
	out := make([]string, 0, len(readOnlyTools))
	for name := range readOnlyTools {
		out = append(out, name)
	}
	return out
}

func (c *Client) RPC(ctx context.Context, method string, params any, idempotent bool) (json.RawMessage, error) {
	return c.rpc(ctx, method, params, idempotent)
}
