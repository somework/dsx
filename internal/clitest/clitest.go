package clitest

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/somework/dsx/internal/mcp"
	"github.com/somework/dsx/internal/mcptest"
	"github.com/somework/dsx/internal/syncer"
)

type (
	Server = mcptest.Server
	Reply  = mcptest.Reply
	Call   = mcptest.Call
)

func New(t *testing.T, tool func(name string, args map[string]any) Reply) *Server {
	return mcptest.New(t, tool)
}

var EnvelopeFor = mcptest.EnvelopeFor

func Client(f *Server) *mcp.Client {
	return mcp.New("test-token", mcp.WithEndpoint(f.URL()))
}

func ListingFor(entries ...syncer.RemoteEntry) string {
	b, err := json.Marshal(entries)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func FileEntry(path, etag string, size int64) syncer.RemoteEntry {
	return syncer.RemoteEntry{Path: path, Type: "file", Size: size, Etag: etag}
}

func DirEntry(path string) syncer.RemoteEntry {
	return syncer.RemoteEntry{Path: path, Type: "directory"}
}

func CaptureStdout(t *testing.T, fn func() error) (string, error) {
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

func Mkfile(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func FirstCall(t *testing.T, f *Server, tool string) Call {
	t.Helper()
	for _, c := range f.Recorded() {
		if c.Tool == tool {
			return c
		}
	}
	t.Fatalf("%s was never called; calls: %v", tool, f.Recorded())
	return Call{}
}

func SeedState(t *testing.T, dir string, st syncer.State) {
	t.Helper()
	if st.Files == nil {
		st.Files = map[string]syncer.FileState{}
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, syncer.StateFileName), b, 0o644); err != nil {
		t.Fatalf("seeding ledger: %v", err)
	}
}

func LedgerExists(t *testing.T, dir string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, syncer.StateFileName))
	return err == nil
}
