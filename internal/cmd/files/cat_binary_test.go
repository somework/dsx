package files

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/clitest"
	"github.com/somework/dsx/internal/dsxerr"
)

var catPNG = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0xff, 0xfe, 0x00}

func binaryCatFake(t *testing.T) *clitest.Server {
	t.Helper()
	var f *clitest.Server
	f = clitest.New(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("assets/og.png", "e1", int64(len(catPNG))))}
		case "read_file":
			return fakeReply{
				Text:    `read_file: "assets/og.png" is a binary file (stored base64); read_file only returns text content`,
				IsError: true,
			}
		case "render_preview":
			return fakeReply{Text: f.PreviewReply(args["path"].(string))}
		}
		return fakeReply{Text: "[]"}
	})
	f.PutServe("assets/og.png", catPNG)
	return f
}

// Bytes that are not valid UTF-8 have no business on a terminal, and a
// terminal is cat's whole default.
func TestCatRefusesABinaryWithNoDestination(t *testing.T) {
	f := binaryCatFake(t)
	out, err := captureStdout(t, func() error {
		return cmdCat(context.Background(), fakeClient(f), []string{"p1", "assets/og.png"})
	})
	if err == nil {
		t.Fatal("cat streamed a binary to stdout")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("classified %q, want usage", got)
	}
	if !strings.Contains(err.Error(), "--out") {
		t.Errorf("the refusal does not name the flag that works: %v", err)
	}
	if out != "" {
		t.Errorf("stdout was not left empty: %q", out)
	}
	if n := f.CountTool("render_preview"); n != 0 {
		t.Errorf("a preview URL was minted for a refused read (%d)", n)
	}
}

func TestCatWithOutFetchesABinaryOverThePreviewLane(t *testing.T) {
	f := binaryCatFake(t)
	dest := filepath.Join(t.TempDir(), "og.png")

	out, err := captureStdout(t, func() error {
		return cmdCat(context.Background(), fakeClient(f), []string{"p1", "assets/og.png", "--out", dest, "--json"})
	})
	if err != nil {
		t.Fatalf("cat --out: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(catPNG) {
		t.Fatalf("wrote % x, want % x", got, catPNG)
	}

	var doc struct {
		Path  string `json:"path"`
		Etag  string `json:"etag"`
		Bytes int    `json:"bytes"`
		Out   string `json:"out"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("--json is not JSON: %q", out)
	}
	if doc.Bytes != len(catPNG) || doc.Path != "assets/og.png" {
		t.Fatalf("unexpected reply: %+v", doc)
	}
	// The preview host supplies no etag comparable with list_files'; the
	// listing's is the only one there is, and it must be the one reported.
	if doc.Etag != "e1" {
		t.Fatalf("etag = %q, want the listing's", doc.Etag)
	}
}

// Invariant 1 travels with the lane. The specific way this one breaks is an
// .html, which the server serves with a preview harness prepended.
func TestCatRefusesPreviewBytesThatDisagreeWithTheListing(t *testing.T) {
	var f *clitest.Server
	f = clitest.New(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("page.html", "e1", 54))}
		case "read_file":
			return fakeReply{Text: `read_file: "page.html" is a binary file (stored base64)`, IsError: true}
		case "render_preview":
			return fakeReply{Text: f.PreviewReply(args["path"].(string))}
		}
		return fakeReply{Text: "[]"}
	})
	f.PutServe("page.html", []byte(strings.Repeat("x", 16000)))

	dest := filepath.Join(t.TempDir(), "page.html")
	_, err := captureStdout(t, func() error {
		return cmdCat(context.Background(), fakeClient(f), []string{"p1", "page.html", "--out", dest})
	})
	if err == nil {
		t.Fatal("a body disagreeing with the listing was written")
	}
	if _, sErr := os.Stat(dest); sErr == nil {
		t.Fatal("the file was written despite the size disagreement")
	}
}

func TestCatNeverSendsTheTokenToThePreviewHost(t *testing.T) {
	f := binaryCatFake(t)
	dest := filepath.Join(t.TempDir(), "og.png")
	if _, err := captureStdout(t, func() error {
		return cmdCat(context.Background(), fakeClient(f), []string{"p1", "assets/og.png", "--out", dest})
	}); err != nil {
		t.Fatal(err)
	}
	gets := f.PreviewGets()
	if len(gets) == 0 {
		t.Fatal("no preview request was made, so this guard checked nothing")
	}
	for _, g := range gets {
		if g.Authorization != "" {
			t.Fatalf("the preview GET carried Authorization: %q", g.Authorization)
		}
	}
}
