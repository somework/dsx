package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
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
	if err := json.Unmarshal(normalizeSSE(payload), &out); err != nil {
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
func normalizeSSE(b []byte) []byte {
	trimmed := bytes.TrimSpace(b)
	if !bytes.HasPrefix(trimmed, []byte("event:")) && !bytes.HasPrefix(trimmed, []byte("data:")) {
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

// callTool invokes an MCP tool and returns its concatenated text content.
func (c *client) callTool(ctx context.Context, name string, args map[string]any) (string, error) {
	raw, err := c.rpc(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, readOnlyTools[name])
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	var res toolResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("%s: malformed tool result: %w", name, err)
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
