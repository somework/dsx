package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Invariant 9, driven end to end through runPush -- which nothing else does.
//
// This is the guard that matters, and the suite had no equivalent. The two
// existing tests named for this invariant (TestIgnoredPathIsNeverPrunedFrom-
// TheServer and ...FromDisk) hand-assemble the correct filtering themselves and
// call planPush directly, so they prove the decision is right given correct
// input. They cannot see the wiring that produces it. TestSyncCallers-
// CannotFilterOneSide reads runPull's and runPush's syntax, so it sees only the
// shapes it was taught: move the function to another file, rename it, or hoist
// the calls into a helper, and it matches nothing and passes.
//
// This test is blind to all of that because it asks the only question that
// matters: did an ignored path reach delete_files? Every bypass shape fails it.
//
// Two traps, both load-bearing:
//
//   - push's delete sends "files" -- a list of {path, if_match} maps -- while
//     `dsx rm` sends "paths". A probe reading args["paths"] passes vacuously.
//   - --prune is only exposed by a ledger that tracked the path BEFORE it became
//     ignored. With an empty ledger, prune is a correct no-op via invariant 4
//     and this test would pass no matter how broken the filtering is.
func TestRunPushNeverPrunesAnIgnoredPathFromTheServer(t *testing.T) {
	dir := t.TempDir()

	// dist/ is ignored now. It was not when app.js was pulled, so the ledger
	// still tracks it as ours and unmodified -- invariant 4's proof is
	// satisfied, which is exactly what makes it eligible for prune.
	if err := os.WriteFile(filepath.Join(dir, ".dsxignore"), []byte("dist/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.md"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	body := []byte("app")
	st := syncState{
		ProjectID: "p1",
		Files: map[string]fileState{
			"dist/app.js": {Etag: "e2", Size: int64(len(body)), SHA: sha256hex(body)},
			"keep.md":     {Etag: "e1", Size: 4, SHA: sha256hex([]byte("keep"))},
		},
	}
	if err := st.save(dir); err != nil {
		t.Fatal(err)
	}

	var deleted []string
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(
				fileEntry("keep.md", "e1", 4),
				fileEntry("dist/app.js", "e2", 3),
			)}
		case "finalize_plan":
			return fakeReply{Text: `{"plan_token":"tok"}`}
		case "delete_files":
			// push sends "files", not "paths" -- see the trap above.
			if fs, ok := args["files"].([]any); ok {
				for _, e := range fs {
					if m, ok := e.(map[string]any); ok {
						if p, ok := m["path"].(string); ok {
							deleted = append(deleted, p)
						}
					}
				}
			}
			return fakeReply{Text: `{"deleted":1}`}
		case "write_files":
			return fakeReply{Text: `{"etags":{},"written":0}`}
		}
		return fakeReply{Text: "[]"}
	})

	if _, err := runPush(context.Background(), fakeClient(f), pushOpts{
		projectID: "p1",
		dir:       dir,
		prune:     true,
	}); err != nil {
		t.Fatalf("runPush: %v", err)
	}

	for _, p := range deleted {
		if p == "dist/app.js" {
			t.Fatalf("INVARIANT 9 BROKEN: push --prune sent a merely-ignored path to delete_files.\n"+
				"dist/app.js is ignored here, so the scan never saw it. If the listing was not "+
				"filtered too, that absence reads as a local delete and the server destroys the "+
				"file — with a matching if_match, so it complies. delete_files carried: %v", deleted)
		}
	}
}

// The mirror on the pull side: an ignored path must not be deleted from disk.
// runPull is the caller here, so this catches a one-sided filter in survey's
// other consumer.
func TestRunPullNeverPrunesAnIgnoredPathFromDisk(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".dsxignore"), []byte("dist/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	appJS := filepath.Join(dir, "dist", "app.js")
	if err := os.WriteFile(appJS, []byte("app"), 0o600); err != nil {
		t.Fatal(err)
	}

	body := []byte("app")
	st := syncState{
		ProjectID: "p1",
		Files:     map[string]fileState{"dist/app.js": {Etag: "e2", Size: int64(len(body)), SHA: sha256hex(body)}},
	}
	if err := st.save(dir); err != nil {
		t.Fatal(err)
	}

	// The server no longer lists dist/app.js. Were the scan filtered but not the
	// listing -- or either side dropped -- prune would read that as "deleted on
	// the server" and remove the only copy.
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor()}
		}
		return fakeReply{Text: "[]"}
	})

	if _, err := runPull(context.Background(), fakeClient(f), pullOpts{
		projectID:   "p1",
		dir:         dir,
		concurrency: 4,
		prune:       true,
	}); err != nil {
		t.Fatalf("runPull: %v", err)
	}

	if _, err := os.Stat(appJS); err != nil {
		t.Fatalf("INVARIANT 9 BROKEN: pull --prune deleted a merely-ignored file from disk: %v", err)
	}
}
