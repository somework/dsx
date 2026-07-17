package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	defaultEndpoint = "https://api.anthropic.com/v1/design/mcp"
	protocolVersion = "2025-06-18"
	maxAttempts     = 4
)

type client struct {
	endpoint string
	token    string
	http     *http.Client
	seq      atomic.Int64

	// lastServerDate holds the Date header of the most recent reply, in unix
	// nanoseconds, or 0 when no reply has carried one.
	//
	// Only `dsx doctor` reads it. dsx decides a token is expired by comparing
	// expiresAt against the local clock, so a machine whose clock is far enough
	// off calls a live token dead -- a failure that otherwise looks exactly
	// like a real expiry, and sends the user to re-run `claude` forever.
	lastServerDate atomic.Int64
}

func newClient(token string) *client {
	ep := defaultEndpoint
	if v := os.Getenv("DSX_ENDPOINT"); v != "" {
		ep = v
	}
	return &client{
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
}

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

// toolError marks a server-reported tool failure, as opposed to a transport
// fault. Callers that expect a specific failure (a missing path, a conflict)
// can match on it instead of on message text.
type toolError struct {
	Tool string
	Text string
}

func (e *toolError) Error() string { return e.Tool + ": " + e.Text }

// serverConflict is write_files' answer to a stale if_match, measured on
// 2026-07-17:
//
//	{"conflicts":[{"path":"a.css","etag":"1784268009093847","current_content":"…"}],
//	 "message":"write_files: refused — the user (or another writer) changed one or
//	            more of these files since your if_match etag. Nothing was written. …"}
//
// It arrives as a tool error, so nothing about it is structured until it is
// parsed. That mattered: the race this reply reports is the exact one every
// if_match exists to catch, and dsx used to hand it back as a generic failure —
// exit 1 — so an agent watching for exit 3 sailed past the one case that needs
// a human.
//
// "Nothing was written" is the server's own word, and it is why this is safe to
// report as a plain conflict rather than as a partial write.
type serverConflict struct {
	Conflicts []struct {
		Path string `json:"path"`
		Etag string `json:"etag"`
	} `json:"conflicts"`
	Message string `json:"message"`
}

// conflictFromToolError recovers the conflicting paths from a write refusal, or
// reports false for any other failure.
func conflictFromToolError(err error) ([]string, bool) {
	var te *toolError
	if !errors.As(err, &te) {
		return nil, false
	}
	var sc serverConflict
	if json.Unmarshal([]byte(te.Text), &sc) != nil || len(sc.Conflicts) == 0 {
		return nil, false
	}
	paths := make([]string, 0, len(sc.Conflicts))
	for _, c := range sc.Conflicts {
		paths = append(paths, c.Path)
	}
	return paths, true
}

// grantError is the server's 403 demanding a standing write grant for a
// project. It is recoverable: a plan_token from finalize_plan authorises the
// same write without one.
type grantError struct{ ProjectID string }

func (e *grantError) Error() string {
	return "needs_project_grant for project " + e.ProjectID
}

// readOnlyTools never change server state, so a transport fault can be retried
// freely. Everything else may already have been applied by the time the fault
// surfaced -- a retried delete_files or write_files would re-execute it.
var readOnlyTools = map[string]bool{
	"list_projects":            true,
	"list_files":               true,
	"list_design_systems":      true,
	"list_members":             true,
	"get_project":              true,
	"get_conversation":         true,
	"get_claude_design_prompt": true,
	"read_file":                true,
}

func (c *client) rpc(ctx context.Context, method string, params any, idempotent bool) (json.RawMessage, error) {
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
			// Exponential backoff with jitter; the endpoint rate-limits.
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

func (c *client) attempt(ctx context.Context, body []byte, idempotent bool) (raw json.RawMessage, retryable bool, err error) {
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
		// The request may already have reached the server and been applied.
		return nil, idempotent, &dsxError{Kind: kindTransport, Msg: "request failed", Err: err}
	}
	defer resp.Body.Close()

	if d, parseErr := http.ParseTime(resp.Header.Get("Date")); parseErr == nil {
		c.lastServerDate.Store(d.UnixNano())
	}

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, idempotent, &dsxError{Kind: kindTransport, Msg: "reading the reply failed", Err: err}
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, false, &dsxError{Kind: kindAuth,
			Msg: "401 unauthorized — token rejected; run any `claude` command to refresh, then retry"}
	case resp.StatusCode == http.StatusForbidden && bytes.Contains(payload, []byte("needs_project_grant")):
		var g struct {
			ProjectID string `json:"project_id"`
		}
		_ = json.Unmarshal(payload, &g)
		return nil, false, &grantError{ProjectID: g.ProjectID}
	case resp.StatusCode == http.StatusTooManyRequests:
		// Rejected before it ran; safe to retry whatever the method.
		return nil, true, &dsxError{Kind: kindTransport,
			Msg: fmt.Sprintf("http %d: %s", resp.StatusCode, truncate(string(payload), 200))}
	case resp.StatusCode >= 500:
		return nil, idempotent, &dsxError{Kind: kindTransport,
			Msg: fmt.Sprintf("http %d: %s", resp.StatusCode, truncate(string(payload), 200))}
	case resp.StatusCode != http.StatusOK:
		return nil, false, &dsxError{Kind: kindProtocol,
			Msg: fmt.Sprintf("http %d: %s", resp.StatusCode, truncate(string(payload), 400))}
	}

	var out rpcResponse
	if err := json.Unmarshal(normalizeSSE(payload, resp.Header.Get("Content-Type")), &out); err != nil {
		return nil, false, &dsxError{Kind: kindProtocol, Msg: "malformed response", Err: err}
	}
	if out.Error != nil {
		return nil, false, &dsxError{Kind: kindProtocol,
			Msg: fmt.Sprintf("rpc %d: %s", out.Error.Code, out.Error.Message)}
	}
	return out.Result, false, nil
}

// normalizeSSE unwraps a text/event-stream framing if the server chose one.
// Plain JSON passes through untouched.
//
// Frames must not simply be concatenated: a stream may carry notifications
// ahead of the response, and gluing two JSON documents together yields neither.
// Only the frame bearing result or error answers our call.
//
// Both the Content-Type and the body are consulted, and either is enough.
//
// The body sniff alone was too strict: it demanded event: or data: first, while
// the grammar this function's own loop implements permits a comment, an id: or
// a retry: -- and servers send `: ping` as a keepalive. But the header alone
// traded one failure for another: every reply ever measured from this endpoint
// is application/json, so a server that started framing without relabelling
// would have died as an unretryable "malformed response".
//
// Accepting either costs nothing. A JSON-RPC reply always opens with '{', so
// the body sniff has an empty false-positive set.
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
			// Per the SSE grammar, several data: lines in one event join on \n.
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

// isEventStream reports an SSE reply, by its Content-Type or by its body.
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

// callTool invokes an MCP tool and returns its concatenated text content.
func (c *client) callTool(ctx context.Context, name string, args map[string]any) (string, error) {
	raw, err := c.rpc(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, readOnlyTools[name])
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	var res toolResult
	if err := json.Unmarshal(raw, &res); err != nil {
		// kindProtocol, matching the malformed-body path above: both are "the
		// server sent a shape we do not model", and errKind is the token an
		// agent matches on, so the two must not answer differently.
		return "", &dsxError{Kind: kindProtocol, Msg: name + ": malformed tool result", Err: err}
	}

	var sb strings.Builder
	for _, part := range res.Content {
		sb.WriteString(part.Text)
	}
	if res.IsError {
		return "", &toolError{Tool: name, Text: strings.TrimSpace(sb.String())}
	}
	return sb.String(), nil
}

// truncate bounds server text for display, cutting on a rune boundary.
//
// The bound is in bytes because the callers are bounding a payload, but the
// cut cannot land inside a rune: the endpoint's own prose is full of multi-byte
// characters (it writes — and …), and half of one is invalid UTF-8 in an error
// message that is about to be marshalled into --json.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
