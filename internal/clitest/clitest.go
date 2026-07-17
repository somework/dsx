// Package clitest is the fake endpoint adapter that every command package tests
// against: how to build a client, the domain shapes, and captureStdout.
//
// It is not a _test.go file, and that is the point. internal/cli and every
// internal/cmd/<group> need this same adapter, and each is tested by its own
// internal tests -- so without a real package they would each carry a copy. Only
// _test.go files import it, so it never reaches the binary.
//
// internal/syncer's fake_test.go stays a duplicate and cannot use this: syncer's
// tests are internal ones (they drive planPull), so they cannot import anything
// that imports syncer -- and this package does. That is the cycle its header
// describes, and it is still real. This package is the third option for
// everything above syncer, not for syncer itself.
//
// What stays out of internal/mcptest is what mcptest deliberately does not know:
// how to build a client (mcptest never imports mcp, so mcp's own internal tests
// can use it), the domain shapes (a listing is []syncer.RemoteEntry, which
// belongs to the sync side and not the transport), and this process's os.Stdout.
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

// Client points a real client at the fake. WithEndpoint is the only legal way
// in: Client.endpoint is unexported and stays that way.
func Client(f *Server) *mcp.Client {
	return mcp.New("test-token", mcp.WithEndpoint(f.URL()))
}

// ListingFor renders a list_files reply: project-relative paths, one directory
// deep, with directories carrying no etag.
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

// CaptureStdout runs fn with os.Stdout redirected and returns what it printed.
// Commands print through fmt.Println directly, so this is the only way to assert
// on their output without restructuring every one of them.
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

// Mkfile writes a file, creating its parents. Its twin is in internal/syncer's
// ignore_test.go, for the same reason as everything above.
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

// FirstCall returns the first call to tool, or fails.
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

// SeedState writes a ledger for a test to find.
//
// It marshals syncer.State rather than calling the ledger's own writer: save is
// unexported and stays that way, because writing the ledger is the sync's job
// and the CLI has no business doing it. The on-disk shape this depends on is
// pinned by internal/syncer's ledger_golden_test.go against hand-written bytes,
// which is the only thing that can catch a renamed json tag -- a round trip
// through these structs would only prove the code equals itself.
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
