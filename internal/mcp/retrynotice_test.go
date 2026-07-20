package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// safeBuf collects notices from whatever goroutine writes them.
type safeBuf struct {
	mu sync.Mutex
	sb strings.Builder
}

func (b *safeBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.Write(p)
}

func (b *safeBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.String()
}

// flakyServer fails the first n requests with a retryable status.
func flakyServer(t *testing.T, n int32) *httptest.Server {
	t.Helper()
	var seen atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen.Add(1) <= n {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A hung request is 120s of silence, and dsx retries it four times. The
// impulsive answer to silence is Ctrl-C, which by invariant 3 is a full
// failure rather than a short success — so the wait has to say it is a wait.
func TestARetryAnnouncesItselfOnTheNoticeWriter(t *testing.T) {
	srv := flakyServer(t, 1)
	var buf safeBuf
	c := New("test-token", WithEndpoint(srv.URL), WithRetryNotice(&buf))

	if _, err := c.ToolsList(context.Background()); err != nil {
		t.Fatalf("call failed after a retryable fault: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "retrying") {
		t.Errorf("notice=%q, want it to say the call is being retried", got)
	}
	if !strings.Contains(got, "2/4") {
		t.Errorf("notice=%q, want the attempt of the total — a bare "+
			"\"retrying\" does not say whether it is nearly over", got)
	}
}

// A clean call must stay silent: the notice exists to explain a wait, and a
// call that did not wait has nothing to explain.
func TestACleanCallWritesNoNotice(t *testing.T) {
	srv := flakyServer(t, 0)
	var buf safeBuf
	c := New("test-token", WithEndpoint(srv.URL), WithRetryNotice(&buf))

	if _, err := c.ToolsList(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "" {
		t.Errorf("a clean call wrote %q", got)
	}
}

// Every later attempt is announced, so a caller watching the terminal can see
// the retries running out rather than guessing.
func TestEveryRetryIsAnnounced(t *testing.T) {
	srv := flakyServer(t, 2)
	var buf safeBuf
	c := New("test-token", WithEndpoint(srv.URL), WithRetryNotice(&buf))

	if _, err := c.ToolsList(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if n := strings.Count(got, "retrying"); n != 2 {
		t.Errorf("announced %d retries, want 2:\n%s", n, got)
	}
	if !strings.Contains(got, "3/4") {
		t.Errorf("notice=%q, want the second retry numbered 3/4", got)
	}
}

// The writer is optional: a nil notice writer is the default and must not
// panic, so nothing outside the CLI has to opt out.
func TestNoNoticeWriterIsSilentAndSafe(t *testing.T) {
	srv := flakyServer(t, 1)
	c := New("test-token", WithEndpoint(srv.URL))

	if _, err := c.ToolsList(context.Background()); err != nil {
		t.Fatalf("a client with no notice writer failed: %v", err)
	}
}
