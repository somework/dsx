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

func (c *client) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
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

		raw, retryable, err := c.attempt(ctx, body)
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

func (c *client) attempt(ctx context.Context, body []byte) (raw json.RawMessage, retryable bool, err error) {
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
		return nil, true, err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, err
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, false, fmt.Errorf("401 unauthorized — token rejected; run any `claude` command to refresh, then retry")
	case resp.StatusCode == http.StatusForbidden && bytes.Contains(payload, []byte("needs_project_grant")):
		var g struct {
			ProjectID string `json:"project_id"`
		}
		_ = json.Unmarshal(payload, &g)
		return nil, false, &grantError{ProjectID: g.ProjectID}
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return nil, true, fmt.Errorf("http %d: %s", resp.StatusCode, truncate(string(payload), 200))
	case resp.StatusCode != http.StatusOK:
		return nil, false, fmt.Errorf("http %d: %s", resp.StatusCode, truncate(string(payload), 400))
	}

	var out rpcResponse
	if err := json.Unmarshal(normalizeSSE(payload), &out); err != nil {
		return nil, false, fmt.Errorf("malformed response: %w", err)
	}
	if out.Error != nil {
		return nil, false, fmt.Errorf("rpc %d: %s", out.Error.Code, out.Error.Message)
	}
	return out.Result, false, nil
}

// normalizeSSE unwraps a text/event-stream framing if the server chose one.
// Plain JSON passes through untouched.
func normalizeSSE(b []byte) []byte {
	if !bytes.HasPrefix(bytes.TrimSpace(b), []byte("event:")) &&
		!bytes.HasPrefix(bytes.TrimSpace(b), []byte("data:")) {
		return b
	}
	var buf bytes.Buffer
	for _, line := range strings.Split(string(b), "\n") {
		if after, ok := strings.CutPrefix(line, "data: "); ok {
			buf.WriteString(after)
		}
	}
	return buf.Bytes()
}

// callTool invokes an MCP tool and returns its concatenated text content.
func (c *client) callTool(ctx context.Context, name string, args map[string]any) (string, error) {
	raw, err := c.rpc(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
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
