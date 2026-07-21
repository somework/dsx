package syncer

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// TestPinRefusesADirectoryThatAlreadyTracksFiles: pin only binds a directory
// whose ledger is empty. Without this check pin would silently wipe every
// tracked etag, leaving the user one --force away from IfMatch == "" on
// every subsequent write (plan.go's push candidate logic).
func TestPinRefusesADirectoryThatAlreadyTracksFiles(t *testing.T) {
	dir := t.TempDir()
	seeded := State{ProjectID: "proj-A", Files: map[string]FileState{
		"a.css": {Etag: "e1", Size: 3, SHA: SHA256Hex([]byte("abc"))},
	}}
	if err := seeded.save(dir); err != nil {
		t.Fatal(err)
	}
	before := readBack(t, StatePath(dir))

	err := Pin(PinOpts{ProjectID: "proj-A", Endpoint: "https://home.example/mcp", Dir: dir})
	if err == nil {
		t.Fatal("pin accepted a directory that already tracks files")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
	if got := readBack(t, StatePath(dir)); got != before {
		t.Error("pin rewrote a ledger it refused to touch")
	}
}

// TestPinRefusesAForeignEndpoint: invariant 13's binding is (project,
// endpoint) and pin must never be an unforced way to defeat the endpoint
// half of it.
func TestPinRefusesAForeignEndpoint(t *testing.T) {
	dir := t.TempDir()
	seeded := State{ProjectID: "proj-A", Endpoint: "https://elsewhere.example/mcp", Files: map[string]FileState{}}
	if err := seeded.save(dir); err != nil {
		t.Fatal(err)
	}
	before := readBack(t, StatePath(dir))

	err := Pin(PinOpts{ProjectID: "proj-A", Endpoint: "https://home.example/mcp", Dir: dir})
	if err == nil {
		t.Fatal("pin accepted a foreign endpoint")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
	// endpointRefusal is shared by pull, push, fetch and pin, each passing its
	// own verb literal; a copy-pasted wrong one (e.g. "push") would be a
	// confusing but silent message defect nothing else here catches.
	if got := err.Error(); !strings.Contains(got, "refusing to pin") {
		t.Errorf("error text = %q, want it to name pin (\"refusing to pin\")", got)
	}
	if got := readBack(t, StatePath(dir)); got != before {
		t.Error("pin rewrote a ledger it refused to touch")
	}
}

// TestPinRefusesAForeignProject is the project half of the same binding,
// exercised on its own for the same reason
// TestFetchRefusesAForeignProjectBeforeTheRoundTrip exists beside the
// endpoint test: nothing previously covered this half in isolation.
func TestPinRefusesAForeignProject(t *testing.T) {
	dir := t.TempDir()
	seeded := State{ProjectID: "proj-A", Files: map[string]FileState{}}
	if err := seeded.save(dir); err != nil {
		t.Fatal(err)
	}
	before := readBack(t, StatePath(dir))

	err := Pin(PinOpts{ProjectID: "proj-B", Endpoint: "https://home.example/mcp", Dir: dir})
	if err == nil {
		t.Fatal("pin accepted a foreign project")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
	if got := readBack(t, StatePath(dir)); got != before {
		t.Error("pin rewrote a ledger it refused to touch")
	}
}

// TestPinRefusalLeavesNoTrace asserts invariant 16 across every refusal
// shape Pin itself can produce: the ledger stays byte-identical to what it
// held before the call, or — for the symlink shape, which has no ledger at
// all — nothing is written through the link.
func TestPinRefusalLeavesNoTrace(t *testing.T) {
	t.Run("tracked files", func(t *testing.T) {
		dir := t.TempDir()
		seeded := State{ProjectID: "proj-A", Files: map[string]FileState{
			"a.css": {Etag: "e1", Size: 3, SHA: SHA256Hex([]byte("abc"))},
		}}
		if err := seeded.save(dir); err != nil {
			t.Fatal(err)
		}
		before := readBack(t, StatePath(dir))

		if err := Pin(PinOpts{ProjectID: "proj-A", Endpoint: "https://home.example/mcp", Dir: dir}); err == nil {
			t.Fatal("want a refusal")
		}
		if got := readBack(t, StatePath(dir)); got != before {
			t.Error("ledger changed after a refused pin")
		}
	})

	t.Run("foreign project", func(t *testing.T) {
		dir := t.TempDir()
		seeded := State{ProjectID: "proj-A", Files: map[string]FileState{}}
		if err := seeded.save(dir); err != nil {
			t.Fatal(err)
		}
		before := readBack(t, StatePath(dir))

		if err := Pin(PinOpts{ProjectID: "proj-B", Endpoint: "https://home.example/mcp", Dir: dir}); err == nil {
			t.Fatal("want a refusal")
		}
		if got := readBack(t, StatePath(dir)); got != before {
			t.Error("ledger changed after a refused pin")
		}
	})

	t.Run("foreign endpoint", func(t *testing.T) {
		dir := t.TempDir()
		seeded := State{ProjectID: "proj-A", Endpoint: "https://elsewhere.example/mcp", Files: map[string]FileState{}}
		if err := seeded.save(dir); err != nil {
			t.Fatal(err)
		}
		before := readBack(t, StatePath(dir))

		if err := Pin(PinOpts{ProjectID: "proj-A", Endpoint: "https://home.example/mcp", Dir: dir}); err == nil {
			t.Fatal("want a refusal")
		}
		if got := readBack(t, StatePath(dir)); got != before {
			t.Error("ledger changed after a refused pin")
		}
	})

	t.Run("symlinked .dsx", func(t *testing.T) {
		dir, target := symlinkedDsxDir(t)
		if err := Pin(PinOpts{ProjectID: "proj-A", Endpoint: "https://home.example/mcp", Dir: dir}); err == nil {
			t.Fatal("want a refusal")
		}
		entries, err := os.ReadDir(target)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Errorf("something was written through the symlink: %v", entries)
		}
	})
}

// TestPinnedLedgerLoadsClean: State{ProjectID, Endpoint, Files: {}} is a
// legal ledger — state.go's damage guard fires only on
// ProjectID == "" && len(Files) > 0 — and LoadState must read it back with
// no complaint and no fixup surprise.
func TestPinnedLedgerLoadsClean(t *testing.T) {
	dir := t.TempDir()
	if err := Pin(PinOpts{ProjectID: "proj-A", Endpoint: "https://home.example/mcp", Dir: dir}); err != nil {
		t.Fatalf("Pin: %v", err)
	}

	st, err := LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState after pin: %v", err)
	}
	if st.ProjectID != "proj-A" {
		t.Errorf("ProjectID = %q, want proj-A", st.ProjectID)
	}
	if st.Endpoint != "https://home.example/mcp" {
		t.Errorf("Endpoint = %q, want https://home.example/mcp", st.Endpoint)
	}
	if len(st.Files) != 0 {
		t.Errorf("Files = %v, want empty", st.Files)
	}

	b, err := os.ReadFile(StatePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["files"]; !ok {
		t.Error(`ledger has no "files" key`)
	}
}
