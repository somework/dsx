package syncer

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/somework/dsx/internal/mcp"
	"github.com/somework/dsx/internal/mcptest"
)

type fakeMCP = mcptest.Server
type fakeReply = mcptest.Reply

func newFakeMCP(t *testing.T, tool func(name string, args map[string]any) fakeReply) *fakeMCP {
	return mcptest.New(t, tool)
}

var envelopeFor = mcptest.EnvelopeFor

func fakeClient(f *fakeMCP) *mcp.Client {
	return mcp.New("test-token", mcp.WithEndpoint(f.URL()))
}

// listingFor models the server, which answers an empty ARRAY for a directory
// with no files — measured, including for a path that does not exist at all.
// A nil variadic marshals to `null`, which the server never sends and which
// WalkTree deliberately refuses as "not a listing".
func listingFor(entries ...RemoteEntry) string {
	if entries == nil {
		entries = []RemoteEntry{}
	}
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

// LedgerExistsForTest reports whether a ledger was written to dir.
func LedgerExistsForTest(dir string) bool {
	_, err := os.Stat(StatePath(dir))
	return err == nil
}

// seedFirstContactLedger makes dir an established sync tracking README.md.
func seedFirstContactLedger(t *testing.T, dir string) {
	t.Helper()
	original := []byte("original\n")
	st := State{
		ProjectID: "proj-A",
		Files: map[string]FileState{
			"README.md": {Etag: "e0", Size: int64(len(original)), SHA: SHA256Hex(original)},
		},
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(StateDir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(StatePath(dir), append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
