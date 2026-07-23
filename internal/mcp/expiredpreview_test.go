package mcp

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// The measured shape of an expired serve_url, end to end: 302 to the editor
// page. It must not become bytes on anyone's disk.
func TestAnExpiredPreviewURLNeverBecomesBytes(t *testing.T) {
	f := previewFake(t)
	f.PutServe("og.png", []byte("never mind the real bytes"))
	f.ServeHook(func(_ string, w http.ResponseWriter) bool {
		w.Header().Set("Location", "https://claude.ai/design/p/proj?file=og.png&present=1")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusFound)
		_, _ = w.Write([]byte(`<a href="https://claude.ai/design/p/proj?file=og.png&amp;present=1">Found</a>.`))
		return true
	})

	c := New("tok", WithEndpoint(f.URL()))
	body, err := c.ReadBinary(context.Background(), "proj", "og.png", 4096)
	if err == nil {
		t.Fatalf("an expired preview URL produced %d bytes", len(body))
	}
	if body != nil {
		t.Fatalf("bytes came back alongside the error: %q", body)
	}
	if !strings.Contains(err.Error(), "redirected") {
		t.Fatalf("unexpected error: %v", err)
	}
}
