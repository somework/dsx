package cli

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

// This file has a near-twin in internal/syncer, and that is deliberate.
//
// The fake endpoint itself lives in internal/mcptest, which every package can
// reach. What cannot be shared is this thin adapter: syncer's tests are internal
// ones (package syncer, because they drive planPull and friends), so they cannot
// import anything that imports syncer -- and any package holding listingFor
// would have to. Go's test rules leave no third option; ~30 duplicated lines is
// the price. If you are about to merge the two, that cycle is why you cannot.
//
// What stays out of mcptest is what mcptest deliberately does not know:
//
//   - how to build a client (mcptest does not import mcp, so mcp's own internal
//     tests can import mcptest without a cycle)
//   - the domain shapes: a listing is []syncer.RemoteEntry, and that type belongs
//     to the sync side, not to the transport
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
func listingFor(entries ...syncer.RemoteEntry) string {
	b, err := json.Marshal(entries)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func fileEntry(path, etag string, size int64) syncer.RemoteEntry {
	return syncer.RemoteEntry{Path: path, Type: "file", Size: size, Etag: etag}
}

func dirEntry(path string) syncer.RemoteEntry {
	return syncer.RemoteEntry{Path: path, Type: "directory"}
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

// mkfile writes a file, creating its parents. Its twin is in internal/syncer's
// ignore_test.go, for the same reason as everything above.
func mkfile(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// syncFirstCall returns the first call to tool, or fails. Twin in
// internal/syncer's sync_gen_test.go.
func syncFirstCall(t *testing.T, f *fakeMCP, tool string) mcptest.Call {
	t.Helper()
	for _, c := range f.Recorded() {
		if c.Tool == tool {
			return c
		}
	}
	t.Fatalf("%s was never called; calls: %v", tool, f.Recorded())
	return mcptest.Call{}
}

// syncSeedState writes a ledger for a test to find.
//
// It marshals syncer.State rather than calling the ledger's own writer: save is
// unexported and stays that way, because writing the ledger is the sync's job
// and the CLI has no business doing it. The on-disk shape this depends on is
// pinned by internal/syncer's ledger_golden_test.go against hand-written bytes,
// which is the only thing that can catch a renamed json tag -- a round trip
// through these structs would only prove the code equals itself.
func syncSeedState(t *testing.T, dir string, st syncer.State) {
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

func syncLedgerExists(t *testing.T, dir string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, syncer.StateFileName))
	return err == nil
}
