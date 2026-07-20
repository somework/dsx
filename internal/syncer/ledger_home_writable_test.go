package syncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// unwritableDsx builds a .dsx that exists, is a real directory, and is not
// writable by the invoking user — the shape checkLedgerHome's shape checks
// (symlink, ENOTDIR, !IsDir) all pass, since none of them probe permissions.
func unwritableDsx(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions do not block writes")
	}
	if err := os.MkdirAll(StateDir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(StateDir(dir), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(StateDir(dir), 0o700) })
}

func TestCheckLedgerHomeRefusesADirectoryItCannotWriteInto(t *testing.T) {
	dir := t.TempDir()
	unwritableDsx(t, dir)

	err := checkLedgerHome(dir)
	if err == nil {
		t.Fatal("checkLedgerHome accepted a .dsx it cannot write into")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindLocal {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindLocal)
	}
}

// TestPullRefusesAnUnwritableDsxBeforeFetchingAnything reproduces the
// sequence a root-owned or otherwise foreign-owned .dsx produces: pull would
// otherwise fetch and overwrite a.css, then fail to save the ledger, leaving
// a.css rewritten with no record of it — the next run calls it a conflict.
func TestPullRefusesAnUnwritableDsxBeforeFetchingAnything(t *testing.T) {
	dir := t.TempDir()
	original := "old content\n"
	mkfile(t, dir, "a.css", original)
	syncSeedState(t, dir, State{
		ProjectID: "proj-A",
		Files: map[string]FileState{
			"a.css": {Etag: "e0", Size: int64(len(original)), SHA: SHA256Hex([]byte(original))},
		},
	})

	unwritableDsx(t, dir)

	newBody := "NEW CONTENT\n"
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("a.css", "e1", int64(len(newBody))))}
		case "read_file":
			return fakeReply{Text: envelopeFor("a.css", "e1", newBody)}
		}
		return fakeReply{Text: "[]"}
	})

	_, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 1,
	})
	if err == nil {
		t.Fatal("want a refusal")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindLocal {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindLocal)
	}
	if got := f.CountTool("list_files"); got != 0 {
		t.Errorf("list_files called %d times, want 0 — the refusal must precede the fetch", got)
	}
	if got := readBack(t, filepath.Join(dir, "a.css")); got != original {
		t.Errorf("a.css = %q, want untouched %q", got, original)
	}
}

// TestPushRefusesAnUnwritableDsxBeforeUploadingAnything is the push half of
// the same shape: a successful write_files call followed by a failed
// st.save would leave the server ahead of the ledger with no record of it.
func TestPushRefusesAnUnwritableDsxBeforeUploadingAnything(t *testing.T) {
	dir := t.TempDir()
	body := "a{}\n"
	mkfile(t, dir, "a.css", body)
	syncSeedState(t, dir, State{
		ProjectID: "proj-A",
		Files: map[string]FileState{
			"a.css": {Etag: "e0", Size: int64(len(body)), SHA: SHA256Hex([]byte(body))},
		},
	})
	// Change the tracked file locally so push has something to write.
	mkfile(t, dir, "a.css", "a{color:red}\n")

	unwritableDsx(t, dir)

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("a.css", "e0", int64(len(body))))}
		case "write_files":
			return fakeReply{Text: `{"etags":{"a.css":"e1"},"written":1}`}
		}
		return fakeReply{Text: "[]"}
	})

	_, err := Push(context.Background(), fakeClient(f), PushOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 1,
	})
	if err == nil {
		t.Fatal("want a refusal")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindLocal {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindLocal)
	}
	if got := f.CountTool("list_files"); got != 0 {
		t.Errorf("list_files called %d times, want 0 — the refusal must precede the upload", got)
	}
	if got := f.CountTool("write_files"); got != 0 {
		t.Errorf("write_files called %d times, want 0", got)
	}
}
