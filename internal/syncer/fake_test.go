package syncer

import (
	"encoding/json"
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
