package main

import (
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/somework/dsx/internal/mcp"
	"github.com/somework/dsx/internal/mcptest"
)

// The fake endpoint moved to internal/mcptest so that every package's tests can
// reach it. What stays here is what mcptest deliberately does not know:
//
//   - how to build a client (mcptest does not import mcp, so mcp's own internal
//     tests can import mcptest without a cycle)
//   - the domain shapes: a listing is []RemoteEntry, and RemoteEntry belongs to
//     the sync side, not to the transport
//   - captureStdout, which is about this process's os.Stdout
//
// mcptest's own doc records what a fake is and is not for; that argument has not
// changed by moving.

type fakeMCP = mcptest.Server
type fakeReply = mcptest.Reply

func newFakeMCP(t *testing.T, tool func(name string, args map[string]any) fakeReply) *fakeMCP {
	return mcptest.New(t, tool)
}

var envelopeFor = mcptest.EnvelopeFor

// fakeClient points a real client at the fake. WithEndpoint is the only legal
// way in: Client.endpoint is unexported and stays that way.
func fakeClient(f *fakeMCP) *mcp.Client {
	return mcp.New("test-token", mcp.WithEndpoint(f.URL()))
}

// listingFor renders a list_files reply: project-relative paths, one directory
// deep, with directories carrying no etag.
func listingFor(entries ...RemoteEntry) string {
	b, err := json.Marshal(entries)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func fileEntry(path, etag string, size int64) RemoteEntry {
	return RemoteEntry{Path: path, Type: "file", Size: size, Etag: etag}
}

func dirEntry(path string) RemoteEntry {
	return RemoteEntry{Path: path, Type: "directory"}
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
