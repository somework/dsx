package syncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRunPushNeverPrunesAnIgnoredPathFromTheServer(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, ".dsxignore"), []byte("dist/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.md"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	body := []byte("app")
	st := State{
		ProjectID: "p1",
		Files: map[string]FileState{
			"dist/app.js": {Etag: "e2", Size: int64(len(body)), SHA: SHA256Hex(body)},
			"keep.md":     {Etag: "e1", Size: 4, SHA: SHA256Hex([]byte("keep"))},
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

	if _, err := Push(context.Background(), fakeClient(f), PushOpts{
		ProjectID: "p1",
		Dir:       dir,
		Prune:     true,
	}); err != nil {
		t.Fatalf("Push: %v", err)
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
	st := State{
		ProjectID: "p1",
		Files:     map[string]FileState{"dist/app.js": {Etag: "e2", Size: int64(len(body)), SHA: SHA256Hex(body)}},
	}
	if err := st.save(dir); err != nil {
		t.Fatal(err)
	}

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor()}
		}
		return fakeReply{Text: "[]"}
	})

	if _, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID:   "p1",
		Dir:         dir,
		Concurrency: 4,
		Prune:       true,
	}); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	if _, err := os.Stat(appJS); err != nil {
		t.Fatalf("INVARIANT 9 BROKEN: pull --prune deleted a merely-ignored file from disk: %v", err)
	}
}
