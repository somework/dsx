package mcp

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/somework/dsx/internal/mcptest"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parsing %q: %v", raw, err)
	}
	return u
}

// The preview URL is server-controlled: whatever render_preview names is what
// dsx would GET. Only two hosts may be reached — see allowServeHost.
func TestOnlyThePreviewHostAndTheEndpointsOwnHostMayBeFetched(t *testing.T) {
	endpoint := mustURL(t, "https://api.anthropic.com/v1/design/mcp")

	for _, tc := range []struct {
		name string
		raw  string
		want bool
	}{
		{"the real preview host", "https://abc.claudeusercontent.com/v1/design/projects/p/serve/a.png", true},
		{"the real host, uppercased", "https://ABC.CLAUDEUSERCONTENT.COM/serve/a.png", true},
		{"the endpoint's own host", "https://api.anthropic.com/anything", true},

		{"a lookalike suffix", "https://evil-claudeusercontent.com/serve/a.png", false},
		{"the suffix as a subdomain of something else", "https://x.claudeusercontent.com.evil.test/serve/a.png", false},
		{"the suffix in the path", "https://evil.test/.claudeusercontent.com/serve/a.png", false},
		{"the suffix in the query", "https://evil.test/serve/a.png?h=.claudeusercontent.com", false},
		{"the suffix as userinfo", "https://x.claudeusercontent.com@evil.test/serve/a.png", false},
		{"userinfo even on the real host", "https://u:p@x.claudeusercontent.com/serve/a.png", false},
		{"plain http on the real host", "http://x.claudeusercontent.com/serve/a.png", false},
		{"the bare apex, no leading dot", "https://claudeusercontent.com/serve/a.png", false},
		{"a third-party host", "https://evil.test/serve/a.png", false},
		{"no host at all", "file:///etc/passwd", false},
		{"the endpoint's host on another scheme", "http://api.anthropic.com/x", false},
		{"the endpoint's host on another port", "https://api.anthropic.com:8443/x", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := allowServeHost(mustURL(t, tc.raw), endpoint); got != tc.want {
				t.Fatalf("allowServeHost(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// An endpoint that does not parse must not become permission to fetch anywhere.
func TestAnUnparseableEndpointGrantsNothing(t *testing.T) {
	if allowServeHost(mustURL(t, "https://evil.test/serve/a.png"), nil) {
		t.Fatal("a nil endpoint admitted a third-party host")
	}
	if !allowServeHost(mustURL(t, "https://x.claudeusercontent.com/serve/a.png"), nil) {
		t.Fatal("a nil endpoint should still admit the real preview host")
	}
}

func previewFake(t *testing.T) *mcptest.Server {
	t.Helper()
	var f *mcptest.Server
	f = mcptest.New(t, func(name string, args map[string]any) mcptest.Reply {
		if name != "render_preview" {
			t.Fatalf("unexpected tool %q", name)
		}
		return mcptest.Reply{Text: f.PreviewReply(args["path"].(string))}
	})
	return f
}

func TestReadBinaryReturnsTheServedBytes(t *testing.T) {
	f := previewFake(t)
	want := []byte{0x89, 'P', 'N', 'G', 0x00, 0xff, 0xfe}
	f.PutServe("a.png", want)

	c := New("test-token", WithEndpoint(f.URL()))
	got, err := c.ReadBinary(context.Background(), "proj", "a.png", int64(len(want)))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("got % x, want % x", got, want)
	}
}

// The preview host neither needs nor should see the OAuth token: its own
// credential is in the URL, and the bearer would be a second secret handed to
// a second host for nothing (invariant 8).
func TestReadBinaryNeverSendsTheOAuthTokenToThePreviewHost(t *testing.T) {
	f := previewFake(t)
	f.PutServe("a.png", []byte("xx"))

	c := New("super-secret-token", WithEndpoint(f.URL()))
	if _, err := c.ReadBinary(context.Background(), "proj", "a.png", 2); err != nil {
		t.Fatal(err)
	}

	gets := f.PreviewGets()
	if len(gets) != 1 {
		t.Fatalf("want exactly one preview GET, got %d", len(gets))
	}
	if gets[0].Authorization != "" {
		t.Fatalf("the preview GET carried an Authorization header: %q", gets[0].Authorization)
	}
	if gets[0].UserAgent != serveUserAgent {
		t.Fatalf("User-Agent = %q, want %q", gets[0].UserAgent, serveUserAgent)
	}
}

func TestReadBinaryRefusesAHostRenderPreviewShouldNotHaveNamed(t *testing.T) {
	f := mcptest.New(t, func(string, map[string]any) mcptest.Reply {
		return mcptest.Reply{Text: `{"serve_url":"https://evil.test/serve/a.png?t=secret-token-value"}`}
	})
	c := New("tok", WithEndpoint(f.URL()))
	_, err := c.ReadBinary(context.Background(), "proj", "a.png", 10)
	if err == nil {
		t.Fatal("fetched from a host neither the endpoint nor the preview host")
	}
	if !strings.Contains(err.Error(), "evil.test") {
		t.Fatalf("the refusal does not name the host it refused: %v", err)
	}
	if strings.Contains(err.Error(), "secret-token-value") {
		t.Fatalf("the refusal leaked the preview token: %v", err)
	}
}

// Measured 2026-07-23: an EXPIRED serve_url answers 302 to the claude.ai
// editor page, not 403 or 404. So this refusal is not only an SSRF guard —
// it is what stops an hour-old URL delivering an HTML page to a caller that
// asked for a PNG.
func TestReadBinaryRefusesARedirect(t *testing.T) {
	f := previewFake(t)
	f.PutServe("a.png", []byte("xx"))
	f.ServeHook(func(path string, w http.ResponseWriter) bool {
		http.Redirect(w, &http.Request{}, "https://evil.test/steal", http.StatusFound)
		return true
	})

	c := New("tok", WithEndpoint(f.URL()))
	_, err := c.ReadBinary(context.Background(), "proj", "a.png", 2)
	if err == nil {
		t.Fatal("followed a redirect off the preview host")
	}
	if !strings.Contains(err.Error(), "redirected") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// list_files' size bounds the read, so a body that keeps coming cannot be
// swallowed whole before invariant 1's exact check ever runs.
func TestReadBinaryRefusesABodyBiggerThanTheListingSize(t *testing.T) {
	f := previewFake(t)
	f.PutServe("a.png", make([]byte, 4096))

	c := New("tok", WithEndpoint(f.URL()))
	if _, err := c.ReadBinary(context.Background(), "proj", "a.png", 4095); err == nil {
		t.Fatal("accepted a body larger than the listing size")
	}
	if _, err := c.ReadBinary(context.Background(), "proj", "a.png", 4096); err != nil {
		t.Fatalf("refused a body of exactly the listing size: %v", err)
	}
}

// The exact-length check alone would catch an oversize body — after reading
// all of it into memory. The bound is what makes a body that never ends cost
// the listing's size and not the machine.
func TestReadBinaryStopsReadingAtTheListingSize(t *testing.T) {
	const total = 8 << 20

	var written atomic.Int64
	f := previewFake(t)
	f.PutServe("a.png", nil)
	f.ServeHook(func(_ string, w http.ResponseWriter) bool {
		chunk := make([]byte, 64<<10)
		for written.Load() < total {
			n, err := w.Write(chunk)
			written.Add(int64(n))
			if err != nil {
				break
			}
		}
		return true
	})

	c := New("tok", WithEndpoint(f.URL()))
	if _, err := c.ReadBinary(context.Background(), "proj", "a.png", 1024); err == nil {
		t.Fatal("accepted a body that never matches the listing size")
	}
	if got := written.Load(); got >= total {
		t.Fatalf("read the whole %d-byte body; the listing size must bound the read (server wrote %d)", total, got)
	}
}

func TestReadBinaryReportsANonOKPreviewStatus(t *testing.T) {
	f := previewFake(t)
	c := New("tok", WithEndpoint(f.URL()))

	_, err := c.ReadBinary(context.Background(), "proj", "missing.png", 10)
	if err == nil {
		t.Fatal("a 404 from the preview host passed as bytes")
	}
	if !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "file not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReadBinaryRefusesAReplyWithNoServeURL(t *testing.T) {
	for _, text := range []string{`{}`, `{"serve_url":""}`, `not json at all`, `{"serve_url":"://"}`} {
		f := mcptest.New(t, func(string, map[string]any) mcptest.Reply {
			return mcptest.Reply{Text: text}
		})
		c := New("tok", WithEndpoint(f.URL()))
		if _, err := c.ReadBinary(context.Background(), "proj", "a.png", 10); err == nil {
			t.Fatalf("accepted render_preview reply %q", text)
		}
	}
}

// Every error path is a printed surface. None of them may carry the token
// that rides in the preview URL's query.
func TestNoReadBinaryErrorEverCarriesThePreviewURL(t *testing.T) {
	const token = "t=the-preview-token"

	cases := []struct {
		name string
		fake func(t *testing.T) *mcptest.Server
		path string
	}{
		{"a 404", func(t *testing.T) *mcptest.Server { return previewFake(t) }, "missing.png"},
		{"a redirect", func(t *testing.T) *mcptest.Server {
			f := previewFake(t)
			f.PutServe("a.png", []byte("x"))
			f.ServeHook(func(_ string, w http.ResponseWriter) bool {
				w.Header().Set("Location", "https://evil.test/steal")
				w.WriteHeader(http.StatusFound)
				return true
			})
			return f
		}, "a.png"},
		{"a connection that dies mid-body", func(t *testing.T) *mcptest.Server {
			f := previewFake(t)
			f.PutServe("a.png", make([]byte, 64))
			f.ServeHook(func(_ string, w http.ResponseWriter) bool {
				w.Header().Set("Content-Length", "64")
				_, _ = w.Write(make([]byte, 8))
				if fl, ok := w.(http.Flusher); ok {
					fl.Flush()
				}
				panic(http.ErrAbortHandler)
			})
			return f
		}, "a.png"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.fake(t)
			c := New("tok", WithEndpoint(f.URL()))
			_, err := c.ReadBinary(context.Background(), "proj", tc.path, 64)
			if err == nil {
				return
			}
			if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "?t=") ||
				strings.Contains(err.Error(), "fake-preview-token") {
				t.Fatalf("error text carries the preview URL: %v", err)
			}
		})
	}
}
