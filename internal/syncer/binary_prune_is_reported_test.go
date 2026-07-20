package syncer

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// Shape: the ledger says `logo.svg` is Binary:true, the server dropped the
// path, and disk still holds the only copy. Keeping the file is guarded
// elsewhere; this pins the other half — a plain, non-force `pull --prune` must
// SAY so. Asserted at the rendered line and the exit-code-bearing Outcome.
func TestPlainPullPruneReportsAnUnprunableBinaryPath(t *testing.T) {
	dir := t.TempDir()
	body := []byte("<svg/>user-authored")
	full := filepath.Join(dir, "logo.svg")
	if err := os.WriteFile(full, body, 0o600); err != nil {
		t.Fatal(err)
	}

	// Binary:true BESIDE a real SHA matching the file on disk, so the sha guard
	// cannot divert this path.
	st := State{ProjectID: "p1", Files: map[string]FileState{
		"logo.svg": {Etag: "e1", Binary: true, Size: int64(len(body)), SHA: SHA256Hex(body)},
	}}
	if err := st.save(dir); err != nil {
		t.Fatal(err)
	}

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor()}
		}
		return fakeReply{Text: "[]"}
	})

	rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID:   "p1",
		Dir:         dir,
		Concurrency: 4,
		Prune:       true,
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	if _, err := os.Stat(full); err != nil {
		t.Fatalf("pull --prune deleted the only copy of a file dsx can never re-fetch: %v", err)
	}

	rendered := rep.Render(false)
	if !strings.Contains(rendered, "logo.svg") {
		t.Errorf("plain `pull --prune` never mentions the path it refused to prune.\n"+
			"rendered:\n%s", rendered)
	}

	// Wording, not just presence: no force level prunes this path, so neither
	// force remedy may be offered for it.
	for _, lie := range []string{"--force would DELETE", "--force to overwrite"} {
		if strings.Contains(rendered, lie) {
			t.Errorf("rendered line promises %q for a path no force level can prune:\n%s", lie, rendered)
		}
	}

	out := rep.Outcome(false)
	if out == nil {
		t.Fatalf("Outcome is nil -> exit 0: the run reports success while a tracked path was "+
			"neither pruned nor surfaced.\nrendered:\n%s", rendered)
	}
	if got := dsxerr.ExitCodeFor(out); got != dsxerr.KindConflict.ExitCode() {
		t.Errorf("exit code = %d, want %d (conflict)", got, dsxerr.KindConflict.ExitCode())
	}
}

// The push side carries the mirror guard, but there it is deliberately SILENT
// and the asymmetry is the point: silent AND undeleted. If someone "restores
// symmetry" with the pull side, this goes red.
func TestPlainPushPruneIsSilentAboutASteadyStateBinaryPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep.md"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The image is on the server, tracked binary:true, and absent from disk.
	st := State{ProjectID: "p1", Files: map[string]FileState{
		"keep.md":  {Etag: "e1", Size: 4, SHA: SHA256Hex([]byte("keep"))},
		"logo.png": {Etag: "e9", Binary: true},
	}}
	if err := st.save(dir); err != nil {
		t.Fatal(err)
	}

	var deleted []string
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(
				fileEntry("keep.md", "e1", 4),
				fileEntry("logo.png", "e9", 120),
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

	rep, err := Push(context.Background(), fakeClient(f), PushOpts{
		ProjectID: "p1",
		Dir:       dir,
		Prune:     true,
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	if slices.Contains(deleted, "logo.png") {
		t.Errorf("push --prune deleted a binary path from the server: delete_files carried %v", deleted)
	}
	if out := rep.Outcome(false); out != nil {
		t.Errorf("plain `push --prune` reports a conflict: %v\nrendered:\n%s", out, rep.Render(false))
	}
	if strings.Contains(rep.Render(false), "logo.png") {
		t.Errorf("plain `push --prune` warns about a path that is simply normal:\n%s", rep.Render(false))
	}
}
