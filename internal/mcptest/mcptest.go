package mcptest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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

type Call struct {
	Method string
	Tool   string
	Args   map[string]any
}

// Server answers JSON-RPC the way the design endpoint does.
type Server struct {
	t   *testing.T
	srv *httptest.Server

	mu    sync.Mutex
	calls []Call

	// tool answers a tools/call. Returning isError models a server-side tool
	// failure; returning an httpStatus models a transport-level one.
	tool func(name string, args map[string]any) Reply
}

// Reply is one answer. Exactly one of its lanes is used: an HTTP status
// short-circuits before the JSON-RPC layer, mirroring how the real endpoint
// reports 401, 403 and 429.
type Reply struct {
	Text       string
	IsError    bool
	HTTPStatus int
	HTTPBody   string
	RawBody    string // bypasses the envelope entirely (SSE framing, garbage)
}

func New(t *testing.T, tool func(name string, args map[string]any) Reply) *Server {
	t.Helper()
	f := &Server{t: t, tool: tool}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *Server) serve(w http.ResponseWriter, r *http.Request) {
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
	f.calls = append(f.calls, Call{Method: req.Method, Tool: p.Name, Args: p.Arguments})
	f.mu.Unlock()

	if req.Method == "tools/list" {
		WriteJSON(w, map[string]any{
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
		WriteJSON(w, map[string]any{
			"jsonrpc": "2.0", "id": req.ID,
			"result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": rep.Text}},
				"isError": rep.IsError,
			},
		})
	}
}

func WriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (f *Server) Recorded() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

func (f *Server) CountTool(name string) int {
	n := 0
	for _, c := range f.Recorded() {
		if c.Tool == name {
			n++
		}
	}
	return n
}

// EnvelopeFor renders a read_file reply exactly as the server frames one: a
// newline after the open tag and one before the close tag, neither belonging to
// the file, and the body escaped for & < > only.
func EnvelopeFor(path, etag, body string) string {
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(body)
	return fmt.Sprintf("<untrusted-project-content path=%q etag=%q>\n%s\n</untrusted-project-content>", path, etag, esc)
}

// URL is the address of the fake. Callers build their own client against it --
// mcptest deliberately does not import mcp, so that mcp's own tests, which are
// internal and reach unexported fields, can import mcptest without a cycle.
func (f *Server) URL() string { return f.srv.URL }
