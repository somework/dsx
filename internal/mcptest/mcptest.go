package mcptest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type Call struct {
	Method        string
	Tool          string
	Args          map[string]any
	Authorization string

	// Set only on a preview-lane GET. Tool is empty there and Path names the
	// file; UserAgent is recorded because the real host is behind a bot filter
	// that answers some agents with 403.
	Path      string
	UserAgent string
}

type Server struct {
	t   *testing.T
	srv *httptest.Server

	mu     sync.Mutex
	calls  []Call
	served map[string][]byte

	// Optional per-path override of the preview lane's HTTP behaviour.
	serveHook func(path string, w http.ResponseWriter) bool

	tool func(name string, args map[string]any) Reply
}

type Reply struct {
	Text       string
	IsError    bool
	HTTPStatus int
	HTTPBody   string
	RawBody    string
}

func New(t *testing.T, tool func(name string, args map[string]any) Reply) *Server {
	t.Helper()
	f := &Server{t: t, tool: tool, served: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// servePrefix is where the fake answers the preview lane. The real host puts
// the project in the hostname and the file under /serve/; only the second half
// matters to dsx, which parses none of it.
const servePrefix = "/serve/"

// PutServe registers the bytes the preview lane returns for a path. A path
// with no bytes registered answers 404 "file not found", as the real host does.
func (f *Server) PutServe(path string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.served[path] = body
}

// ServeHook takes over the preview lane. Returning false falls through to the
// registered bytes. It is how a test reaches a redirect, a truncated body or a
// 5xx — shapes PutServe cannot express.
func (f *Server) ServeHook(fn func(path string, w http.ResponseWriter) bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.serveHook = fn
}

// ServeURL is the preview URL for a path, pointed at this fake. Its host is
// the endpoint's own host, which is the second of the two hosts dsx will
// fetch from — see allowServeHost.
func (f *Server) ServeURL(path string) string {
	return f.srv.URL + servePrefix + (&url.URL{Path: path}).EscapedPath() + "?t=fake-preview-token"
}

// PreviewReply is a render_preview reply for a path, shaped as the real one is.
func (f *Server) PreviewReply(path string) string {
	b, err := json.Marshal(map[string]any{
		"serve_url":  f.ServeURL(path),
		"open_url":   "https://claude.ai/design/p/proj?file=" + url.QueryEscape(path),
		"expires_at": 1784806176,
		"note":       "Open serve_url in your browser tooling only",
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func (f *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, servePrefix) {
		f.servePreview(w, r)
		return
	}

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
	f.calls = append(f.calls, Call{
		Method:        req.Method,
		Tool:          p.Name,
		Args:          p.Arguments,
		Authorization: r.Header.Get("Authorization"),
	})
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

func (f *Server) servePreview(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, servePrefix)

	f.mu.Lock()
	f.calls = append(f.calls, Call{
		Method:        r.Method,
		Path:          path,
		Authorization: r.Header.Get("Authorization"),
		UserAgent:     r.Header.Get("User-Agent"),
	})
	hook := f.serveHook
	body, known := f.served[path]
	f.mu.Unlock()

	if hook != nil && hook(path, w) {
		return
	}
	if !known {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "file not found")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(body)
}

// PreviewGets returns the preview-lane requests recorded so far.
func (f *Server) PreviewGets() []Call {
	var out []Call
	for _, c := range f.Recorded() {
		if c.Method == http.MethodGet {
			out = append(out, c)
		}
	}
	return out
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

func EnvelopeFor(path, etag, body string) string {
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(body)
	return fmt.Sprintf("<untrusted-project-content path=%q etag=%q>\n%s\n</untrusted-project-content>", path, etag, esc)
}

func (f *Server) URL() string { return f.srv.URL }
