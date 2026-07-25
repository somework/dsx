package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/mcptest"
)

func mcpGenRawServer(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New("test-token")
	c.endpoint = srv.URL
	return c
}

type mcpGenManifest struct {
	Tools []struct {
		Name        string `json:"name"`
		Annotations struct {
			ReadOnlyHint bool `json:"readOnlyHint"`
		} `json:"annotations"`
	} `json:"tools"`
}

func mcpGenLoadManifest(t *testing.T) mcpGenManifest {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "reference", "mcp-tools.json"))
	if err != nil {
		t.Fatalf("reading the recorded tools/list: %v", err)
	}
	var m mcpGenManifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parsing the recorded tools/list: %v", err)
	}
	if len(m.Tools) == 0 {
		t.Fatal("the recorded tools/list carried no tools; the cross-check below would pass vacuously")
	}
	return m
}

func TestReadOnlyToolsMatchesTheServersOwnReadOnlyHint(t *testing.T) {
	t.Parallel()
	m := mcpGenLoadManifest(t)

	listed := make(map[string]bool, len(m.Tools))
	for _, tool := range m.Tools {
		listed[tool.Name] = true
		if got, want := readOnlyTools[tool.Name], tool.Annotations.ReadOnlyHint; got != want {
			t.Errorf("readOnlyTools[%q] = %v, but the server annotates readOnlyHint=%v",
				tool.Name, got, want)
		}
	}
	for name := range readOnlyTools {
		if !listed[name] {
			t.Errorf("readOnlyTools names %q, which the server's tools/list does not mention; "+
				"either the name is a typo or the reference is stale", name)
		}
	}
}

func TestNoToolWhoseNameImpliesMutationIsMarkedReadOnly(t *testing.T) {
	t.Parallel()
	mutatingVerbs := []string{
		"write", "delete", "create", "update", "add", "remove", "copy", "put", "finalize",
	}
	for name := range readOnlyTools {
		for _, verb := range mutatingVerbs {
			if strings.Contains(name, verb) {
				t.Errorf("readOnlyTools contains %q, whose name implies mutation (%q); "+
					"a transport fault would re-execute it", name, verb)
			}
		}
	}
}

func TestReadOnlyToolIsRetriedAfterA500(t *testing.T) {
	t.Parallel()
	var seen atomic.Int32
	f := mcptest.New(t, func(name string, args map[string]any) mcptest.Reply {
		if seen.Add(1) == 1 {
			return mcptest.Reply{HTTPStatus: http.StatusInternalServerError, HTTPBody: "upstream boom"}
		}
		return mcptest.Reply{Text: mcptest.EnvelopeFor("a.txt", "e1", "hello")}
	})

	got, err := New("test-token", WithEndpoint(f.URL())).CallTool(context.Background(), "read_file",
		map[string]any{"project_id": "p", "path": "a.txt"})
	if err != nil {
		t.Fatalf("read_file should have survived one 500: %v", err)
	}
	if n := f.CountTool("read_file"); n != 2 {
		t.Errorf("read_file reached the server %d times, want 2 (one 500, one retry)", n)
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("retry returned %q, which does not carry the body", got)
	}
}

func TestMutatingToolIsNotRetriedAfterA500(t *testing.T) {
	t.Parallel()
	f := mcptest.New(t, func(name string, args map[string]any) mcptest.Reply {
		return mcptest.Reply{HTTPStatus: http.StatusInternalServerError, HTTPBody: "upstream boom"}
	})

	_, err := New("test-token", WithEndpoint(f.URL())).CallTool(context.Background(), "write_files",
		map[string]any{"project_id": "p", "files": []any{}})
	if err == nil {
		t.Fatal("write_files returned nil error after a 500")
	}
	if n := f.CountTool("write_files"); n != 1 {
		t.Fatalf("write_files reached the server %d times, want exactly 1; "+
			"a replayed write re-applies a mutation the server may already have made", n)
	}
	if k := dsxerr.Classify(err).Kind; k != dsxerr.KindTransport {
		t.Errorf("dsxerr.Classify(err).Kind = %q, want %q", k, dsxerr.KindTransport)
	}
}

func TestMutatingToolIsRetriedAfterA429(t *testing.T) {
	t.Parallel()
	var seen atomic.Int32
	f := mcptest.New(t, func(name string, args map[string]any) mcptest.Reply {
		if seen.Add(1) == 1 {
			return mcptest.Reply{HTTPStatus: http.StatusTooManyRequests, HTTPBody: "slow down"}
		}
		return mcptest.Reply{Text: `{"etags":{"a.txt":"e2"}}`}
	})

	got, err := New("test-token", WithEndpoint(f.URL())).CallTool(context.Background(), "write_files",
		map[string]any{"project_id": "p", "files": []any{}})
	if err != nil {
		t.Fatalf("write_files should have been retried past a 429: %v", err)
	}
	if n := f.CountTool("write_files"); n != 2 {
		t.Errorf("write_files reached the server %d times, want 2; a 429 never ran, so it is safe to replay", n)
	}
	if got != `{"etags":{"a.txt":"e2"}}` {
		t.Errorf("reply after retry = %q", got)
	}
}

func TestRetryExhaustionKeepsTheTransportClassification(t *testing.T) {
	t.Parallel()
	f := mcptest.New(t, func(name string, args map[string]any) mcptest.Reply {
		return mcptest.Reply{HTTPStatus: http.StatusBadGateway, HTTPBody: "no upstream"}
	})

	_, err := New("test-token", WithEndpoint(f.URL())).CallTool(context.Background(), "list_files", map[string]any{"project_id": "p"})
	if err == nil {
		t.Fatal("list_files returned nil error after every attempt 502'd")
	}
	if n := f.CountTool("list_files"); n != maxAttempts {
		t.Errorf("list_files reached the server %d times, want maxAttempts=%d", n, maxAttempts)
	}
	if k := dsxerr.Classify(err).Kind; k != dsxerr.KindTransport {
		t.Errorf("dsxerr.Classify(err).Kind = %q, want %q; the label did not survive the retry wrap", k, dsxerr.KindTransport)
	}
	if code := dsxerr.ExitCodeFor(err); code != dsxerr.ExitTransport {
		t.Errorf("dsxerr.ExitCodeFor(err) = %d, want %d", code, dsxerr.ExitTransport)
	}
}

func TestBadRequestIsProtocolAndIsNotRetried(t *testing.T) {
	t.Parallel()
	f := mcptest.New(t, func(name string, args map[string]any) mcptest.Reply {
		return mcptest.Reply{HTTPStatus: http.StatusBadRequest, HTTPBody: "bad arguments"}
	})

	_, err := New("test-token", WithEndpoint(f.URL())).CallTool(context.Background(), "list_files", map[string]any{"project_id": "p"})
	if err == nil {
		t.Fatal("a 400 produced no error")
	}
	if n := f.CountTool("list_files"); n != 1 {
		t.Errorf("list_files reached the server %d times, want 1; a 400 will say the same thing every time", n)
	}
	if k := dsxerr.Classify(err).Kind; k != dsxerr.KindProtocol {
		t.Errorf("dsxerr.Classify(err).Kind = %q, want %q", k, dsxerr.KindProtocol)
	}
}

func TestUnauthorizedIsAuthAndSaysToRunClaude(t *testing.T) {
	t.Parallel()
	f := mcptest.New(t, func(name string, args map[string]any) mcptest.Reply {
		return mcptest.Reply{HTTPStatus: http.StatusUnauthorized, HTTPBody: `{"error":"unauthorized"}`}
	})

	_, err := New("test-token", WithEndpoint(f.URL())).CallTool(context.Background(), "list_files", map[string]any{"project_id": "p"})
	if err == nil {
		t.Fatal("a 401 produced no error")
	}
	if n := f.CountTool("list_files"); n != 1 {
		t.Errorf("list_files reached the server %d times, want 1; a rejected token stays rejected", n)
	}
	de := dsxerr.Classify(err)
	if de.Kind != dsxerr.KindAuth {
		t.Fatalf("dsxerr.Classify(err).Kind = %q, want %q", de.Kind, dsxerr.KindAuth)
	}
	if code := dsxerr.ExitCodeFor(err); code != dsxerr.ExitAuth {
		t.Errorf("dsxerr.ExitCodeFor(err) = %d, want %d", code, dsxerr.ExitAuth)
	}

	if !strings.Contains(de.Msg, "claude") {
		t.Errorf("401 message = %q, want it to name the `claude` command as the fix", de.Msg)
	}
	if !strings.Contains(de.Msg, "DSX_TOKEN") {
		t.Errorf("401 message = %q, want it to name the DSX_TOKEN escape hatch", de.Msg)
	}
}

// TestForbiddenWithoutGrantIsAuthAndNamesConsent covers the 403 that is NOT
// needs_project_grant: an account whose token is valid but which has never
// authorised Claude Design. It is an authorisation refusal, so it is KindAuth
// (exit 5) like the 401, not the KindProtocol/exit-1 a bare non-200 would
// otherwise get — and the message names `/design consent`, the one out-of-band
// step dsx cannot take for the user. Matched on the 403 status alone, because
// the body of a consent refusal is unmeasured; needs_project_grant is peeled
// off in the case above, so what reaches here is every OTHER 403.
func TestForbiddenWithoutGrantIsAuthAndNamesConsent(t *testing.T) {
	t.Parallel()
	// The body carries control bytes on purpose: a hostile 403 must not splice
	// a CR or an ANSI escape into the message dsx prints to a terminal
	// (invariant 7). fmtutil.Printable is the guard, and its absence is what
	// the sanitisation assertions below catch.
	f := mcptest.New(t, func(name string, args map[string]any) mcptest.Reply {
		return mcptest.Reply{HTTPStatus: http.StatusForbidden, HTTPBody: "{\"error\":\"forbidden\"}\r\n\x1b[31mgotcha"}
	})

	_, err := New("test-token", WithEndpoint(f.URL())).CallTool(context.Background(), "list_files", map[string]any{"project_id": "p"})
	if err == nil {
		t.Fatal("a bare 403 produced no error")
	}
	if n := f.CountTool("list_files"); n != 1 {
		t.Errorf("list_files reached the server %d times, want 1; a forbidden account stays forbidden", n)
	}

	var ge *GrantError
	if errors.As(err, &ge) {
		t.Fatalf("a non-grant 403 matched *GrantError (%v); it must not route through finalize_plan", ge)
	}

	de := dsxerr.Classify(err)
	if de.Kind != dsxerr.KindAuth {
		t.Fatalf("dsxerr.Classify(err).Kind = %q, want %q; a 403 is an authorisation refusal, not a protocol error", de.Kind, dsxerr.KindAuth)
	}
	if code := dsxerr.ExitCodeFor(err); code != dsxerr.ExitAuth {
		t.Errorf("dsxerr.ExitCodeFor(err) = %d, want %d", code, dsxerr.ExitAuth)
	}
	if !strings.Contains(de.Msg, "/design consent") {
		t.Errorf("403 message = %q, want it to name `/design consent` as the account-level fix", de.Msg)
	}
	if !strings.Contains(de.Msg, "forbidden") {
		t.Errorf("403 message = %q, want it to echo the server body so the real refusal is visible", de.Msg)
	}
	if strings.ContainsAny(de.Msg, "\r\n\x1b") {
		t.Errorf("403 message = %q, want the server body sanitised — a CR or ANSI escape must not reach the terminal", de.Msg)
	}
}

func TestNeedsProjectGrantSurfacesAsGrantErrorAndIsNotRetried(t *testing.T) {
	t.Parallel()
	f := mcptest.New(t, func(name string, args map[string]any) mcptest.Reply {
		return mcptest.Reply{
			HTTPStatus: http.StatusForbidden,
			HTTPBody:   `{"error":"needs_project_grant","project_id":"proj-42"}`,
		}
	})

	_, err := New("test-token", WithEndpoint(f.URL())).CallTool(context.Background(), "write_files",
		map[string]any{"project_id": "proj-42", "files": []any{}})
	if err == nil {
		t.Fatal("the grant refusal produced no error")
	}

	var ge *GrantError
	if !errors.As(err, &ge) {
		t.Fatalf("errors.As did not find a *GrantError in %v; push cannot self-authorise", err)
	}
	if ge.ProjectID != "proj-42" {
		t.Errorf("GrantError.ProjectID = %q, want %q; finalize_plan needs the id", ge.ProjectID, "proj-42")
	}

	var te *ToolError
	if errors.As(err, &te) {
		t.Errorf("the grant refusal also matched *ToolError (%v)", te)
	}
	if n := f.CountTool("write_files"); n != 1 {
		t.Errorf("write_files reached the server %d times, want 1; the 403 is deterministic and write_files mutates", n)
	}
	if !strings.Contains(err.Error(), "dsx plan") {
		t.Errorf("grant refusal = %q, want it to name `dsx plan` as the remedy, not just the condition", err)
	}
}

func TestToolErrorSurvivesTheCallToolWrap(t *testing.T) {
	t.Parallel()
	f := mcptest.New(t, func(name string, args map[string]any) mcptest.Reply {
		return mcptest.Reply{IsError: true, Text: "  file not found: nope.txt  "}
	})

	_, err := New("test-token", WithEndpoint(f.URL())).CallTool(context.Background(), "read_file",
		map[string]any{"project_id": "p", "path": "nope.txt"})
	if err == nil {
		t.Fatal("isError:true produced no error")
	}

	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("errors.As did not find a *ToolError in %v", err)
	}
	if te.Tool != "read_file" {
		t.Errorf("ToolError.Tool = %q, want read_file", te.Tool)
	}
	if te.Text != "file not found: nope.txt" {
		t.Errorf("ToolError.Text = %q, want the server's text trimmed and otherwise intact", te.Text)
	}

	if n := f.CountTool("read_file"); n != 1 {
		t.Errorf("read_file reached the server %d times, want 1", n)
	}
}

func TestRPCErrorObjectIsProtocolAndIsNotRetried(t *testing.T) {
	t.Parallel()
	f := mcptest.New(t, func(name string, args map[string]any) mcptest.Reply {
		return mcptest.Reply{RawBody: `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"invalid params"}}`}
	})

	_, err := New("test-token", WithEndpoint(f.URL())).CallTool(context.Background(), "list_files", map[string]any{"project_id": "p"})
	if err == nil {
		t.Fatal("an rpc error object produced no error")
	}
	if k := dsxerr.Classify(err).Kind; k != dsxerr.KindProtocol {
		t.Errorf("dsxerr.Classify(err).Kind = %q, want %q", k, dsxerr.KindProtocol)
	}

	if n := f.CountTool("list_files"); n != 1 {
		t.Errorf("list_files reached the server %d times, want 1", n)
	}
	var te *ToolError
	if errors.As(err, &te) {
		t.Errorf("an rpc-level error matched *ToolError; it is not a tool result")
	}
}

func TestMalformedBodyIsProtocolNotTransport(t *testing.T) {
	t.Parallel()
	f := mcptest.New(t, func(name string, args map[string]any) mcptest.Reply {
		return mcptest.Reply{RawBody: `{"jsonrpc":"2.0","id":1,"result":`}
	})

	_, err := New("test-token", WithEndpoint(f.URL())).CallTool(context.Background(), "list_files", map[string]any{"project_id": "p"})
	if err == nil {
		t.Fatal("a truncated body produced no error")
	}
	if k := dsxerr.Classify(err).Kind; k != dsxerr.KindProtocol {
		t.Errorf("dsxerr.Classify(err).Kind = %q, want %q; unparseable is not the same as retryable", k, dsxerr.KindProtocol)
	}
	if n := f.CountTool("list_files"); n != 1 {
		t.Errorf("list_files reached the server %d times, want 1", n)
	}
}

func TestCallToolConcatenatesEveryTextPart(t *testing.T) {
	t.Parallel()
	f := mcptest.New(t, func(name string, args map[string]any) mcptest.Reply {
		return mcptest.Reply{RawBody: `{"jsonrpc":"2.0","id":1,"result":{"content":[` +
			`{"type":"text","text":"head"},{"type":"text","text":"-tail"}],"isError":false}}`}
	})

	got, err := New("test-token", WithEndpoint(f.URL())).CallTool(context.Background(), "read_file",
		map[string]any{"project_id": "p", "path": "a.txt"})
	if err != nil {
		t.Fatalf("callTool: %v", err)
	}
	if got != "head-tail" {
		t.Errorf("callTool = %q, want %q; a dropped part silently truncates a file", got, "head-tail")
	}
}

func TestCallToolRefusesAResultShapeItCannotParse(t *testing.T) {
	t.Parallel()
	f := mcptest.New(t, func(name string, args map[string]any) mcptest.Reply {
		return mcptest.Reply{RawBody: `{"jsonrpc":"2.0","id":1,"result":"not a tool result"}`}
	})

	got, err := New("test-token", WithEndpoint(f.URL())).CallTool(context.Background(), "read_file",
		map[string]any{"project_id": "p", "path": "a.txt"})
	if err == nil {
		t.Fatalf("callTool returned (%q, nil) for a result it cannot parse", got)
	}
	if got != "" {
		t.Errorf("callTool returned text %q alongside an error", got)
	}
	if !strings.Contains(err.Error(), "read_file") {
		t.Errorf("err = %v, want it to name the tool", err)
	}
}

func TestNewClientPrefersTheEndpointFromTheEnvironment(t *testing.T) {
	t.Setenv("DSX_ENDPOINT", "")
	if got := New("tok").endpoint; got != defaultEndpoint {
		t.Errorf("endpoint = %q, want the default %q when the env is empty", got, defaultEndpoint)
	}
	t.Setenv("DSX_ENDPOINT", "http://127.0.0.1:1/mcp")
	if got := New("tok").endpoint; got != "http://127.0.0.1:1/mcp" {
		t.Errorf("endpoint = %q, want DSX_ENDPOINT to win", got)
	}
}

func TestBackoffHonoursContextCancellationInsteadOfSleepingThroughIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	f := mcptest.New(t, func(name string, args map[string]any) mcptest.Reply {
		cancel()
		return mcptest.Reply{HTTPStatus: http.StatusInternalServerError}
	})

	started := time.Now()
	_, err := New("test-token", WithEndpoint(f.URL())).CallTool(ctx, "read_file", map[string]any{"project_id": "p", "path": "a.txt"})
	elapsed := time.Since(started)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want it to carry context.Canceled", err)
	}

	if elapsed >= 400*time.Millisecond {
		t.Errorf("cancelled call took %v, want well under the 500ms first backoff", elapsed)
	}
}

func TestServerDateLandsInLastServerDate(t *testing.T) {
	t.Parallel()
	want := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	c := mcpGenRawServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", want.Format(http.TimeFormat))
		mcptest.WriteJSON(w, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tools": []any{}}})
	})

	if _, err := c.rpc(context.Background(), "tools/list", map[string]any{}, true); err != nil {
		t.Fatalf("rpc: %v", err)
	}
	if got := c.lastServerDate.Load(); got != want.UnixNano() {
		t.Errorf("lastServerDate = %d (%v), want %d (%v)",
			got, time.Unix(0, got).UTC(), want.UnixNano(), want)
	}
}

func TestUnparseableDateLeavesLastServerDateZero(t *testing.T) {
	t.Parallel()
	c := mcpGenRawServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", "not a date")
		mcptest.WriteJSON(w, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tools": []any{}}})
	})

	if _, err := c.rpc(context.Background(), "tools/list", map[string]any{}, true); err != nil {
		t.Fatalf("rpc: %v", err)
	}
	if got := c.lastServerDate.Load(); got != 0 {
		t.Errorf("lastServerDate = %d, want 0 for an unparseable Date", got)
	}
}

func TestNormalizeSSESkipsAnUnparseableFrameAheadOfTheResponse(t *testing.T) {
	t.Parallel()
	in := "event: message\n" +
		"data: <html>gateway noise</html>\n\n" +
		"event: message\n" +
		`data: {"jsonrpc":"2.0","id":1,"result":{"ok":true}}` + "\n\n"

	var out rpcResponse
	if err := json.Unmarshal(normalizeSSE([]byte(in), "text/event-stream"), &out); err != nil {
		t.Fatalf("the response did not survive an unparseable frame ahead of it: %v", err)
	}
	if string(out.Result) != `{"ok":true}` {
		t.Errorf("result = %s, want {\"ok\":true}", out.Result)
	}
}

func TestNormalizeSSEFallsBackToTheLastFrameWhenNoneCarriesResultOrError(t *testing.T) {
	t.Parallel()
	last := `{"jsonrpc":"2.0","method":"notifications/message","params":{"n":2}}`
	in := "event: message\n" +
		`data: {"jsonrpc":"2.0","method":"notifications/message","params":{"n":1}}` + "\n\n" +
		"event: message\ndata: " + last + "\n\n"

	if got := string(normalizeSSE([]byte(in), "text/event-stream")); got != last {
		t.Errorf("normalizeSSE = %q, want the last frame %q", got, last)
	}
}

func TestNormalizeSSEReturnsTheBodyWhenNoDataLinesArePresent(t *testing.T) {
	t.Parallel()
	in := []byte("event: ping\n\nevent: ping\n\n")
	if got := normalizeSSE(in, "text/event-stream"); !bytes.Equal(got, in) {
		t.Errorf("normalizeSSE = %q, want the body unchanged", got)
	}
}

func TestNormalizeSSEHandlesAStreamThatOpensWithData(t *testing.T) {
	t.Parallel()
	in := `data: {"jsonrpc":"2.0","id":1,"result":{"ok":true}}` + "\n\n"
	var out rpcResponse
	if err := json.Unmarshal(normalizeSSE([]byte(in), "text/event-stream"), &out); err != nil {
		t.Fatalf("event-less stream failed: %v", err)
	}
	if string(out.Result) != `{"ok":true}` {
		t.Errorf("result = %s", out.Result)
	}
}
