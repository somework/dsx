package syncer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// writeRawLedger puts arbitrary bytes where the ledger lives.
func writeRawLedger(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(StateDir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(StatePath(dir), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const damagedLedger = `{
  "project_id": "",
  "files": {
    "a.css": {"etag": "e1", "size": 3, "sha256": "deadbeef"}
  }
}
`

// A ledger that tracks files but names no project is damaged, and the damage is
// exactly the shape both bind guards short-circuit on: they compare only when
// st.ProjectID != "". Such a ledger is therefore accepted as the ledger of ANY
// project, while its etags and SHAs stay fully trusted by the prune loop.
func TestLoadStateRefusesAFilesMapWithNoProject(t *testing.T) {
	dir := t.TempDir()
	writeRawLedger(t, dir, damagedLedger)

	_, err := LoadState(dir)
	if err == nil {
		t.Fatal("LoadState accepted a ledger tracking files under no project")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindLocal {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindLocal)
	}
	if !strings.Contains(err.Error(), stateBaseName) {
		t.Errorf("refusal does not name the file: %s", err)
	}
}

// An empty files map with no project is how every unsynced directory looks.
func TestLoadStateAcceptsAnUnboundEmptyLedger(t *testing.T) {
	dir := t.TempDir()
	writeRawLedger(t, dir, `{"project_id":"","files":{}}`)

	st, err := LoadState(dir)
	if err != nil {
		t.Fatalf("an empty unbound ledger was refused: %v", err)
	}
	if st.ProjectID != "" || len(st.Files) != 0 {
		t.Errorf("state=%+v, want empty", st)
	}
}

// A missing ledger stays the normal unsynced case, not damage.
func TestLoadStateStillAcceptsAMissingLedger(t *testing.T) {
	if _, err := LoadState(t.TempDir()); err != nil {
		t.Fatalf("a missing ledger was refused: %v", err)
	}
}

// The damage must stop the operation before the prune loop can act on the
// orphaned etags, on both sides.
func TestPullRefusesADamagedLedgerBeforeTouchingDisk(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.css"), []byte("a{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRawLedger(t, dir, damagedLedger)

	var called []string
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		called = append(called, name)
		return fakeReply{Text: listingFor()}
	})

	if _, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "any-project", Dir: dir, Concurrency: 1, Prune: true,
	}); err == nil {
		t.Fatal("Pull ran against a damaged ledger")
	}
	if len(called) != 0 {
		t.Errorf("tools called=%v, want none", called)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.css")); err != nil {
		t.Errorf("a.css was removed on the strength of an orphaned etag: %v", err)
	}
}

func TestPushRefusesADamagedLedgerBeforeTouchingTheServer(t *testing.T) {
	dir := t.TempDir()
	writeRawLedger(t, dir, damagedLedger)

	var called []string
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		called = append(called, name)
		return fakeReply{Text: listingFor()}
	})

	if _, err := Push(context.Background(), fakeClient(f), PushOpts{
		ProjectID: "any-project", Dir: dir, Concurrency: 1, Prune: true,
	}); err == nil {
		t.Fatal("Push ran against a damaged ledger")
	}
	if len(called) != 0 {
		t.Errorf("tools called=%v, want none", called)
	}
}

// --force is not a repair tool: it would send the orphaned etags as if_match,
// or drop if_match entirely.
func TestDamagedLedgerRefusalIsNotUnlockedByForce(t *testing.T) {
	dir := t.TempDir()
	writeRawLedger(t, dir, damagedLedger)

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})
	if _, err := Push(context.Background(), fakeClient(f), PushOpts{
		ProjectID: "any-project", Dir: dir, Concurrency: 1, Prune: true, Force: true,
	}); err == nil {
		t.Fatal("--force unlocked a damaged ledger")
	}
}

// The repair route named must be pull, never push --force: pull into a
// populated directory reports conflicts, push --force overwrites the server.
func TestDamagedLedgerRefusalNamesPullNotForce(t *testing.T) {
	dir := t.TempDir()
	writeRawLedger(t, dir, damagedLedger)

	_, err := LoadState(dir)
	if err == nil {
		t.Fatal("want a refusal")
	}
	msg := err.Error()
	if !strings.Contains(msg, "dsx pull") {
		t.Errorf("refusal does not name the repair route: %s", msg)
	}
	if strings.Contains(msg, "--force") {
		t.Errorf("refusal mentions --force, which overwrites rather than repairs: %s", msg)
	}
}
