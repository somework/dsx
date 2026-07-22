package syncer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// The server dropped `logo.svg`; the ledger says Binary:true and disk still
// holds it. A plain push must plan a conflict, not a write.
//
// Asserted at the caller's surface: the write_files payload, the rendered line,
// the --json document and the exit-code-bearing Outcome.
func TestPlainPushDoesNotResurrectABinaryPathTheServerDeleted(t *testing.T) {
	dir := t.TempDir()
	body := []byte("<svg/>user-authored")
	full := filepath.Join(dir, "logo.svg")
	if err := os.WriteFile(full, body, 0o600); err != nil {
		t.Fatal(err)
	}

	st := State{ProjectID: "p1", Files: map[string]FileState{
		"logo.svg": {Etag: "e1", Binary: true, Size: int64(len(body)), SHA: SHA256Hex(body)},
	}}
	if err := st.save(dir); err != nil {
		t.Fatal(err)
	}

	var wrote []string
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor()} // the server dropped it
		case "write_files":
			if fs, ok := args["files"].([]any); ok {
				for _, e := range fs {
					if m, ok := e.(map[string]any); ok {
						if p, ok := m["path"].(string); ok {
							wrote = append(wrote, p)
						}
					}
				}
			}
			return fakeReply{Text: `{"etags":{"logo.svg":"e2"},"written":1}`}
		case "finalize_plan":
			return fakeReply{Text: `{"plan_token":"tok"}`}
		}
		return fakeReply{Text: "[]"}
	})

	rep, err := Push(context.Background(), fakeClient(f), PushOpts{
		ProjectID: "p1",
		Dir:       dir,
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	if len(wrote) > 0 {
		t.Errorf("a plain `dsx push` re-created a path the server deleted: write_files carried %v", wrote)
	}

	rendered := rep.Render(false)
	if !strings.Contains(rendered, "logo.svg") {
		t.Errorf("plain `push` never mentions the path it refused to send.\nrendered:\n%s", rendered)
	}

	// Wording, not just presence.
	for _, lie := range []string{"--force would DELETE", "`dsx pull` first", "the only copy is gone"} {
		if strings.Contains(rendered, lie) {
			t.Errorf("rendered line gives advice that is false for a path the server no longer has: %q\n%s", lie, rendered)
		}
	}

	// The machine-facing half of the same surface.
	if js := rep.Render(true); !strings.Contains(js, "logo.svg") {
		t.Errorf("--json document omits the path entirely:\n%s", js)
	}

	out := rep.Outcome()
	if out == nil {
		t.Fatalf("Outcome is nil -> exit 0: the run reports plain success on the very run that "+
			"undid a server-side deletion.\nrendered:\n%s", rendered)
	}
	if got := dsxerr.ExitCodeFor(out); got != dsxerr.KindConflict.ExitCode() {
		t.Errorf("exit code = %d, want %d (conflict)", got, dsxerr.KindConflict.ExitCode())
	}

	// The hint is assembled from a different list than Render's ladder, so the
	// same strings are checked again here.
	for _, lie := range []string{"--force would DELETE", "`dsx pull` first", "the only copy is gone"} {
		if strings.Contains(out.Error(), lie) {
			t.Errorf("Outcome hint gives advice that is false for a path the server no longer has: %q\n%v", lie, out)
		}
	}

	// The loops above are purely negative, and an empty hint contains no lie.
	// So say what the hint must CONTAIN, not only what it must not.
	for _, want := range []string{
		"are gone from the server", // the situation, named
		"delete them here",         // the escape that keeps the deletion
		"--force to re-upload",     // the escape that undoes it, spelled as a choice
	} {
		if !strings.Contains(out.Error(), want) {
			t.Errorf("Outcome hint never says %q, so an agent is told a path conflicts and "+
				"nothing about why or what to do:\n%v", want, out)
		}
	}
}

// The escape hatch stays open: --force re-uploads the path.
func TestForcedPushStillRestoresABinaryPathTheServerDeleted(t *testing.T) {
	dir := t.TempDir()
	body := []byte("<svg/>user-authored")
	if err := os.WriteFile(filepath.Join(dir, "logo.svg"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	st := State{ProjectID: "p1", Files: map[string]FileState{
		"logo.svg": {Etag: "e1", Binary: true, Size: int64(len(body)), SHA: SHA256Hex(body)},
	}}
	if err := st.save(dir); err != nil {
		t.Fatal(err)
	}

	var wrote []string
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor()}
		case "write_files":
			if fs, ok := args["files"].([]any); ok {
				for _, e := range fs {
					if m, ok := e.(map[string]any); ok {
						if p, ok := m["path"].(string); ok {
							wrote = append(wrote, p)
						}
					}
				}
			}
			return fakeReply{Text: `{"etags":{"logo.svg":"e2"},"written":1}`}
		case "finalize_plan":
			return fakeReply{Text: `{"plan_token":"tok"}`}
		}
		return fakeReply{Text: "[]"}
	})

	rep, err := Push(context.Background(), fakeClient(f), PushOpts{
		ProjectID: "p1",
		Dir:       dir,
		Force:     true,
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if len(wrote) != 1 || wrote[0] != "logo.svg" {
		t.Errorf("--force no longer re-uploads the path: write_files carried %v", wrote)
	}
	if out := rep.Outcome(); out != nil {
		t.Errorf("--force still reports a conflict: %v", out)
	}
}
