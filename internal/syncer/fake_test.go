package syncer

import (
	"encoding/json"
	"testing"

	"github.com/somework/dsx/internal/mcp"
	"github.com/somework/dsx/internal/mcptest"
)

// This file has a near-twin in internal/cli, and that is deliberate.
//
// The fake endpoint itself lives in internal/mcptest, which every package can
// reach. What cannot be shared is this thin adapter: these tests are internal
// ones (package syncer, because they drive planPull and friends), so they cannot
// import anything that imports syncer -- and any package holding listingFor
// would have to. Go's test rules leave no third option; ~30 duplicated lines is
// the price. If you are about to merge the two, that cycle is why you cannot.
//
// The twins are not identical, and staticcheck is why: cli's carries
// captureStdout and mkfile, which nothing here uses. Copy across only what the
// side you are on actually calls, or U1000 fails the build.
//
// What stays out of mcptest is what mcptest deliberately does not know:
//
//   - how to build a client (mcptest does not import mcp, so mcp's own internal
//     tests can import mcptest without a cycle)
//   - the domain shapes: a listing is []RemoteEntry, and RemoteEntry belongs to
//     the sync side, not to the transport
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
