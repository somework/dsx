package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// A fake endpoint, and what it is and is not for.
//
// It exercises dsx's own handling: retry and idempotency, batching, envelope
// decoding, error classification, ledger bookkeeping. It does NOT establish
// protocol facts, and no test here should be read as evidence about the server.
// A mock can only repeat what we already believe -- it could never have
// discovered that write_files replies with a map rather than a list, that
// needs_project_grant arrives as HTTP 403 rather than a tool error, or that
// binary detection is by content rather than by extension. Each of those was
// guessed wrong first and corrected only by probing the live service, and a
// green mock would have hidden all three.
//
// Every reply shape below is copied from a recorded live response; PROTOCOL.md
// is the record. Protocol truth is live_test.go's job.

type recordedCall struct {
	Method string
	Tool   string
	Args   map[string]any
}

// fakeMCP answers JSON-RPC the way the design endpoint does.
type fakeMCP struct {
	t   *testing.T
	srv *httptest.Server

	mu    sync.Mutex
	calls []recordedCall

	// tool answers a tools/call. Returning isError models a server-side tool
	// failure; returning an httpStatus models a transport-level one.
	tool func(name string, args map[string]any) fakeReply
}

// fakeReply is one answer. Exactly one of its lanes is used: an HTTP status
// short-circuits before the JSON-RPC layer, mirroring how the real endpoint
// reports 401, 403 and 429.
type fakeReply struct {
	Text       string
	IsError    bool
	HTTPStatus int
	HTTPBody   string
	RawBody    string // bypasses the envelope entirely (SSE framing, garbage)
}

func newFakeMCP(t *testing.T, tool func(name string, args map[string]any) fakeReply) *fakeMCP {
	t.Helper()
	f := &fakeMCP{t: t, tool: tool}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeMCP) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		ID     any             `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	_ = json.Unmarshal(req.Params, &p)

	f.mu.Lock()
	f.calls = append(f.calls, recordedCall{Method: req.Method, Tool: p.Name, Args: p.Arguments})
	f.mu.Unlock()

	if req.Method == "tools/list" {
		writeJSON(w, map[string]any{
			"jsonrpc": "2.0", "id": req.ID,
			"result": map[string]any{"tools": []map[string]any{
				{"name": "list_files", "description": "List files\nin a project"},
				{"name": "read_file", "description": "Read a file"},
			}},
		})
		return
	}

	rep := f.tool(p.Name, p.Arguments)
	switch {
	case rep.HTTPStatus != 0:
		w.WriteHeader(rep.HTTPStatus)
		_, _ = io.WriteString(w, rep.HTTPBody)
	case rep.RawBody != "":
		_, _ = io.WriteString(w, rep.RawBody)
	default:
		writeJSON(w, map[string]any{
			"jsonrpc": "2.0", "id": req.ID,
			"result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": rep.Text}},
				"isError": rep.IsError,
			},
		})
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// client returns a dsx client pointed at the fake.
func (f *fakeMCP) client() *client {
	c := newClient("test-token")
	c.endpoint = f.srv.URL
	return c
}

func (f *fakeMCP) recorded() []recordedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedCall(nil), f.calls...)
}

func (f *fakeMCP) countTool(name string) int {
	n := 0
	for _, c := range f.recorded() {
		if c.Tool == name {
			n++
		}
	}
	return n
}

// envelopeFor renders a read_file reply exactly as the server frames one: a
// newline after the open tag and one before the close tag, neither belonging to
// the file, and the body escaped for & < > only.
func envelopeFor(path, etag, body string) string {
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(body)
	return fmt.Sprintf("<untrusted-project-content path=%q etag=%q>\n%s\n</untrusted-project-content>", path, etag, esc)
}

// listingFor renders a list_files reply: project-relative paths, one directory
// deep, with directories carrying no etag.
func listingFor(entries ...remoteEntry) string {
	b, err := json.Marshal(entries)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func fileEntry(path, etag string, size int64) remoteEntry {
	return remoteEntry{Path: path, Type: "file", Size: size, Etag: etag}
}

func dirEntry(path string) remoteEntry {
	return remoteEntry{Path: path, Type: "directory"}
}

// captureStdout runs fn with os.Stdout redirected and returns what it printed.
// Commands print through fmt.Println directly, so this is the only way to
// assert on their output without restructuring every one of them.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fnErr := fn()

	os.Stdout = saved
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	return out, fnErr
}
