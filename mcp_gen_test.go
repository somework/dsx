package main

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
)

// Transport tests for mcp.go.
//
// The fake here is not evidence about the server -- see fake_test.go. What it
// can prove is dsx's own conduct in front of a server: how many times a request
// reaches the wire, and what classification a caller ends up branching on.
// Both are things a mistake in mcp.go breaks silently.

// mcpGenRawServer returns a client pointed at a bare handler, for the cases
// fakeMCP cannot express: an exact Date header, a suppressed one, a raw status.
func mcpGenRawServer(t *testing.T, h http.HandlerFunc) *client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := newClient("test-token")
	c.endpoint = srv.URL
	return c
}

// mcpGenManifest is the shape of reference/mcp-tools.json, which is the
// server's own tools/list output recorded verbatim.
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
	b, err := os.ReadFile(filepath.Join("reference", "mcp-tools.json"))
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

// ---------------------------------------------------------------------------
// readOnlyTools must not drift (invariant 6)
// ---------------------------------------------------------------------------

// reference/mcp-tools.json is the server's verbatim reply, so its readOnlyHint
// is the one statement about retry safety dsx did not have to guess. Drift in
// either direction is a defect, and they are not symmetric: a name missing from
// readOnlyTools only costs a retry dsx could have had, while an extra name
// re-executes a mutation after a transport fault.
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

// A second, independent guard: the reference file only knows the tools that
// existed when it was recorded. A tool added later and marked read-only by
// hand, whose name says otherwise, should still be caught.
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

// ---------------------------------------------------------------------------
// Retry: who may be replayed, and who may not
// ---------------------------------------------------------------------------

// A 500 says nothing about whether the server ran the call, so a read-only tool
// is the only kind dsx may put back on the wire.
func TestReadOnlyToolIsRetriedAfterA500(t *testing.T) {
	t.Parallel()
	var seen atomic.Int32
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if seen.Add(1) == 1 {
			return fakeReply{HTTPStatus: http.StatusInternalServerError, HTTPBody: "upstream boom"}
		}
		return fakeReply{Text: envelopeFor("a.txt", "e1", "hello")}
	})

	got, err := f.client().callTool(context.Background(), "read_file",
		map[string]any{"project_id": "p", "path": "a.txt"})
	if err != nil {
		t.Fatalf("read_file should have survived one 500: %v", err)
	}
	if n := f.countTool("read_file"); n != 2 {
		t.Errorf("read_file reached the server %d times, want 2 (one 500, one retry)", n)
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("retry returned %q, which does not carry the body", got)
	}
}

// THE test in this file. write_files may already have been applied when the 500
// surfaced, so exactly one attempt may reach the server.
//
// Falsification: flipping readOnlyTools["write_files"] to true makes rpc treat
// the 500 as retryable and the count becomes maxAttempts. If this test ever
// stops failing under that edit, it has stopped testing anything.
func TestMutatingToolIsNotRetriedAfterA500(t *testing.T) {
	t.Parallel()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{HTTPStatus: http.StatusInternalServerError, HTTPBody: "upstream boom"}
	})

	_, err := f.client().callTool(context.Background(), "write_files",
		map[string]any{"project_id": "p", "files": []any{}})
	if err == nil {
		t.Fatal("write_files returned nil error after a 500")
	}
	if n := f.countTool("write_files"); n != 1 {
		t.Fatalf("write_files reached the server %d times, want exactly 1; "+
			"a replayed write re-applies a mutation the server may already have made", n)
	}
	if k := dsxerr.Classify(err).Kind; k != dsxerr.KindTransport {
		t.Errorf("dsxerr.Classify(err).Kind = %q, want %q", k, dsxerr.KindTransport)
	}
}

// 429 is the one status that is safe for any method: the server rejected the
// call before running it. Retrying is what keeps a large push from dying on
// rate limiting.
func TestMutatingToolIsRetriedAfterA429(t *testing.T) {
	t.Parallel()
	var seen atomic.Int32
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if seen.Add(1) == 1 {
			return fakeReply{HTTPStatus: http.StatusTooManyRequests, HTTPBody: "slow down"}
		}
		return fakeReply{Text: `{"etags":{"a.txt":"e2"}}`}
	})

	got, err := f.client().callTool(context.Background(), "write_files",
		map[string]any{"project_id": "p", "files": []any{}})
	if err != nil {
		t.Fatalf("write_files should have been retried past a 429: %v", err)
	}
	if n := f.countTool("write_files"); n != 2 {
		t.Errorf("write_files reached the server %d times, want 2; a 429 never ran, so it is safe to replay", n)
	}
	if got != `{"etags":{"a.txt":"e2"}}` {
		t.Errorf("reply after retry = %q", got)
	}
}

// The wrap at the end of rpc ("after N attempts: %w") is the last thing between
// the transport label and the caller. With %v instead of %w the label is lost
// and a run that should exit 4 (retryable) exits 1 (generic failure) -- an
// agent branching on the kind would stop retrying a fault that was retryable.
func TestRetryExhaustionKeepsTheTransportClassification(t *testing.T) {
	t.Parallel()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{HTTPStatus: http.StatusBadGateway, HTTPBody: "no upstream"}
	})

	_, err := f.client().callTool(context.Background(), "list_files", map[string]any{"project_id": "p"})
	if err == nil {
		t.Fatal("list_files returned nil error after every attempt 502'd")
	}
	if n := f.countTool("list_files"); n != maxAttempts {
		t.Errorf("list_files reached the server %d times, want maxAttempts=%d", n, maxAttempts)
	}
	if k := dsxerr.Classify(err).Kind; k != dsxerr.KindTransport {
		t.Errorf("dsxerr.Classify(err).Kind = %q, want %q; the label did not survive the retry wrap", k, dsxerr.KindTransport)
	}
	if code := dsxerr.ExitCodeFor(err); code != dsxerr.ExitTransport {
		t.Errorf("dsxerr.ExitCodeFor(err) = %d, want %d", code, dsxerr.ExitTransport)
	}
}

// A 400 is deterministic: replaying it burns attempts and, worse, classifying
// it as transport tells a caller to retry something that can never succeed.
func TestBadRequestIsProtocolAndIsNotRetried(t *testing.T) {
	t.Parallel()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{HTTPStatus: http.StatusBadRequest, HTTPBody: "bad arguments"}
	})

	_, err := f.client().callTool(context.Background(), "list_files", map[string]any{"project_id": "p"})
	if err == nil {
		t.Fatal("a 400 produced no error")
	}
	if n := f.countTool("list_files"); n != 1 {
		t.Errorf("list_files reached the server %d times, want 1; a 400 will say the same thing every time", n)
	}
	if k := dsxerr.Classify(err).Kind; k != dsxerr.KindProtocol {
		t.Errorf("dsxerr.Classify(err).Kind = %q, want %q", k, dsxerr.KindProtocol)
	}
}

// ---------------------------------------------------------------------------
// Error classification a caller acts on
// ---------------------------------------------------------------------------

// Auth is the one failure with a specific remedy, and mcp.go is the only place
// that knows a 401 happened. Retrying it would just spend the backoff.
func TestUnauthorizedIsAuthAndSaysToRunClaude(t *testing.T) {
	t.Parallel()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{HTTPStatus: http.StatusUnauthorized, HTTPBody: `{"error":"unauthorized"}`}
	})

	_, err := f.client().callTool(context.Background(), "list_files", map[string]any{"project_id": "p"})
	if err == nil {
		t.Fatal("a 401 produced no error")
	}
	if n := f.countTool("list_files"); n != 1 {
		t.Errorf("list_files reached the server %d times, want 1; a rejected token stays rejected", n)
	}
	de := dsxerr.Classify(err)
	if de.Kind != dsxerr.KindAuth {
		t.Fatalf("dsxerr.Classify(err).Kind = %q, want %q", de.Kind, dsxerr.KindAuth)
	}
	if code := dsxerr.ExitCodeFor(err); code != dsxerr.ExitAuth {
		t.Errorf("dsxerr.ExitCodeFor(err) = %d, want %d", code, dsxerr.ExitAuth)
	}
	// Errors say what to do next. Without the remedy the user has a dead token
	// and no way to learn that re-running claude fixes it.
	if !strings.Contains(de.Msg, "claude") {
		t.Errorf("401 message = %q, want it to name the `claude` command as the fix", de.Msg)
	}
}

// The grant refusal is recoverable and push.go recovers from it by name --
// `errors.As(err, &ge)` at push.go:185, through callTool's %w wrap. If it
// arrived as a plain dsxerr.Error or a toolError, push would surface the 403 to the
// user instead of self-authorising with a plan_token.
func TestNeedsProjectGrantSurfacesAsGrantErrorAndIsNotRetried(t *testing.T) {
	t.Parallel()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{
			HTTPStatus: http.StatusForbidden,
			HTTPBody:   `{"error":"needs_project_grant","project_id":"proj-42"}`,
		}
	})

	_, err := f.client().callTool(context.Background(), "write_files",
		map[string]any{"project_id": "proj-42", "files": []any{}})
	if err == nil {
		t.Fatal("the grant refusal produced no error")
	}

	var ge *grantError
	if !errors.As(err, &ge) {
		t.Fatalf("errors.As did not find a *grantError in %v; push cannot self-authorise", err)
	}
	if ge.ProjectID != "proj-42" {
		t.Errorf("grantError.ProjectID = %q, want %q; finalize_plan needs the id", ge.ProjectID, "proj-42")
	}

	// It is a transport-layer refusal, not a tool result, and conflating the two
	// would send push down the wrong recovery path.
	var te *toolError
	if errors.As(err, &te) {
		t.Errorf("the grant refusal also matched *toolError (%v)", te)
	}
	if n := f.countTool("write_files"); n != 1 {
		t.Errorf("write_files reached the server %d times, want 1; the 403 is deterministic and write_files mutates", n)
	}
}

// isError is the server refusing the call, not the transport failing. Callers
// match on the type rather than on message text, and the text must arrive whole
// because it is the only description of what went wrong.
func TestToolErrorSurvivesTheCallToolWrap(t *testing.T) {
	t.Parallel()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{IsError: true, Text: "  file not found: nope.txt  "}
	})

	_, err := f.client().callTool(context.Background(), "read_file",
		map[string]any{"project_id": "p", "path": "nope.txt"})
	if err == nil {
		t.Fatal("isError:true produced no error")
	}

	var te *toolError
	if !errors.As(err, &te) {
		t.Fatalf("errors.As did not find a *toolError in %v", err)
	}
	if te.Tool != "read_file" {
		t.Errorf("toolError.Tool = %q, want read_file", te.Tool)
	}
	if te.Text != "file not found: nope.txt" {
		t.Errorf("toolError.Text = %q, want the server's text trimmed and otherwise intact", te.Text)
	}
	// A tool error is the server's considered answer; replaying it changes nothing.
	if n := f.countTool("read_file"); n != 1 {
		t.Errorf("read_file reached the server %d times, want 1", n)
	}
}

func TestRPCErrorObjectIsProtocolAndIsNotRetried(t *testing.T) {
	t.Parallel()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{RawBody: `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"invalid params"}}`}
	})

	_, err := f.client().callTool(context.Background(), "list_files", map[string]any{"project_id": "p"})
	if err == nil {
		t.Fatal("an rpc error object produced no error")
	}
	if k := dsxerr.Classify(err).Kind; k != dsxerr.KindProtocol {
		t.Errorf("dsxerr.Classify(err).Kind = %q, want %q", k, dsxerr.KindProtocol)
	}
	// list_files is read-only, so nothing but the classification stops a replay
	// of a call the server has already judged malformed.
	if n := f.countTool("list_files"); n != 1 {
		t.Errorf("list_files reached the server %d times, want 1", n)
	}
	var te *toolError
	if errors.As(err, &te) {
		t.Errorf("an rpc-level error matched *toolError; it is not a tool result")
	}
}

func TestMalformedBodyIsProtocolNotTransport(t *testing.T) {
	t.Parallel()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{RawBody: `{"jsonrpc":"2.0","id":1,"result":`}
	})

	_, err := f.client().callTool(context.Background(), "list_files", map[string]any{"project_id": "p"})
	if err == nil {
		t.Fatal("a truncated body produced no error")
	}
	if k := dsxerr.Classify(err).Kind; k != dsxerr.KindProtocol {
		t.Errorf("dsxerr.Classify(err).Kind = %q, want %q; unparseable is not the same as retryable", k, dsxerr.KindProtocol)
	}
	if n := f.countTool("list_files"); n != 1 {
		t.Errorf("list_files reached the server %d times, want 1", n)
	}
}

// A server that splits text across content parts must not lose the tail:
// read_file's envelope is parsed as one string, and a truncated envelope is a
// corrupt file. Invariant 1's size check would catch it, but only after the
// decode had already gone wrong.
func TestCallToolConcatenatesEveryTextPart(t *testing.T) {
	t.Parallel()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{RawBody: `{"jsonrpc":"2.0","id":1,"result":{"content":[` +
			`{"type":"text","text":"head"},{"type":"text","text":"-tail"}],"isError":false}}`}
	})

	got, err := f.client().callTool(context.Background(), "read_file",
		map[string]any{"project_id": "p", "path": "a.txt"})
	if err != nil {
		t.Fatalf("callTool: %v", err)
	}
	if got != "head-tail" {
		t.Errorf("callTool = %q, want %q; a dropped part silently truncates a file", got, "head-tail")
	}
}

// rpc succeeded but the result is not a tool result at all. The property worth
// pinning is the negative one: callTool must not hand back ("", nil). An empty
// string that looks like success is a read_file that writes an empty file and a
// write_files whose etags are quietly never recorded.
func TestCallToolRefusesAResultShapeItCannotParse(t *testing.T) {
	t.Parallel()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{RawBody: `{"jsonrpc":"2.0","id":1,"result":"not a tool result"}`}
	})

	got, err := f.client().callTool(context.Background(), "read_file",
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

// DSX_ENDPOINT is how the live suite and anyone debugging points dsx somewhere
// other than production. If it were ignored, a test run aimed at a scratch
// endpoint would silently touch the real service instead.
func TestNewClientPrefersTheEndpointFromTheEnvironment(t *testing.T) {
	// Not parallel: t.Setenv mutates process-wide state. Setting it empty first
	// makes the default case deterministic even if the developer running the
	// suite has DSX_ENDPOINT exported.
	t.Setenv("DSX_ENDPOINT", "")
	if got := newClient("tok").endpoint; got != defaultEndpoint {
		t.Errorf("endpoint = %q, want the default %q when the env is empty", got, defaultEndpoint)
	}
	t.Setenv("DSX_ENDPOINT", "http://127.0.0.1:1/mcp")
	if got := newClient("tok").endpoint; got != "http://127.0.0.1:1/mcp" {
		t.Errorf("endpoint = %q, want DSX_ENDPOINT to win", got)
	}
}

// ---------------------------------------------------------------------------
// Backoff
// ---------------------------------------------------------------------------

// The backoff sleeps for seconds. A plain time.Sleep there would make Ctrl-C
// take up to the full remaining backoff to be noticed, and the derived contexts
// used by the concurrent tree walk would keep working after the walk gave up.
func TestBackoffHonoursContextCancellationInsteadOfSleepingThroughIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		// Cancel while the first attempt is in flight, so the loop meets a done
		// context exactly when it reaches its first backoff.
		cancel()
		return fakeReply{HTTPStatus: http.StatusInternalServerError}
	})

	started := time.Now()
	_, err := f.client().callTool(ctx, "read_file", map[string]any{"project_id": "p", "path": "a.txt"})
	elapsed := time.Since(started)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want it to carry context.Canceled", err)
	}
	// The first backoff is 2<<0... i.e. 500ms plus jitter, and there are three
	// of them. Returning inside that window is the whole assertion.
	if elapsed >= 400*time.Millisecond {
		t.Errorf("cancelled call took %v, want well under the 500ms first backoff", elapsed)
	}
}

// ---------------------------------------------------------------------------
// The Date header (doctor's clock check reads it)
// ---------------------------------------------------------------------------

func TestServerDateLandsInLastServerDate(t *testing.T) {
	t.Parallel()
	want := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	c := mcpGenRawServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", want.Format(http.TimeFormat))
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tools": []any{}}})
	})

	if _, err := c.rpc(context.Background(), "tools/list", map[string]any{}, true); err != nil {
		t.Fatalf("rpc: %v", err)
	}
	if got := c.lastServerDate.Load(); got != want.UnixNano() {
		t.Errorf("lastServerDate = %d (%v), want %d (%v)",
			got, time.Unix(0, got).UTC(), want.UnixNano(), want)
	}
}

// Zero is the sentinel doctor turns into "skew unknown". Storing time.Now() as
// a stand-in would make the clock check compare the local clock against itself
// and report ok on precisely the skewed machine the check exists to find.
func TestReplyWithoutADateLeavesLastServerDateZero(t *testing.T) {
	t.Parallel()
	c := mcpGenRawServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header()["Date"] = nil // suppress net/http's automatic Date
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tools": []any{}}})
	})

	if _, err := c.rpc(context.Background(), "tools/list", map[string]any{}, true); err != nil {
		t.Fatalf("rpc: %v", err)
	}
	if got := c.lastServerDate.Load(); got != 0 {
		t.Fatalf("lastServerDate = %d, want 0 for a reply carrying no Date", got)
	}
	// The sentinel only matters through what doctor does with it.
	if ch := clockCheck(c.lastServerDate.Load(), time.Now()); ch.Status != checkWarn {
		t.Errorf("clock check = %+v, want a warn rather than an invented verdict", ch)
	}
}

// An unparseable Date must not be stored either -- a garbage value is worse
// than none, because doctor would report a decades-wide skew as a hard failure.
func TestUnparseableDateLeavesLastServerDateZero(t *testing.T) {
	t.Parallel()
	c := mcpGenRawServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", "not a date")
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tools": []any{}}})
	})

	if _, err := c.rpc(context.Background(), "tools/list", map[string]any{}, true); err != nil {
		t.Fatalf("rpc: %v", err)
	}
	if got := c.lastServerDate.Load(); got != 0 {
		t.Errorf("lastServerDate = %d, want 0 for an unparseable Date", got)
	}
}

// ---------------------------------------------------------------------------
// normalizeSSE: the branches mcp_test.go leaves uncovered
// ---------------------------------------------------------------------------

// A frame that does not parse must be skipped, not treated as the answer and
// not allowed to end the search: the response may well be behind it.
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

// When nothing carries result or error there is no right answer; returning the
// last frame at least hands the caller something the protocol layer can report
// as malformed, rather than a silent empty success.
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

// An SSE-looking body with no data: lines yields no frames at all. Returning
// the body unchanged keeps the decision with the caller, which reports it as
// malformed; returning empty would look like a well-formed nothing.
func TestNormalizeSSEReturnsTheBodyWhenNoDataLinesArePresent(t *testing.T) {
	t.Parallel()
	in := []byte("event: ping\n\nevent: ping\n\n")
	if got := normalizeSSE(in, "text/event-stream"); !bytes.Equal(got, in) {
		t.Errorf("normalizeSSE = %q, want the body unchanged", got)
	}
}

// A stream may open with data: and never send an event: line.
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

// ---------------------------------------------------------------------------
// truncate
// ---------------------------------------------------------------------------
