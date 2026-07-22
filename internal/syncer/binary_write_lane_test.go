package syncer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// The ledger holds two binary shapes:
//
//	shape 1 {Etag, Binary:true, SHA:""}       -- dsx has never held these bytes.
//	shape 2 {Etag, Binary:true, SHA:<real>}   -- dsx sent these bytes and
//	                                             recorded their hash.
//
// binPNG is invalid UTF-8 on purpose -- a real binary body, not a stand-in.
var binPNG = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0xff, 0xfe, 0x00, 0x01}

// writeSpecsFrom pulls the payload the server would actually receive out of a
// write_files call, so these tests assert what is SENT rather than what a
// decision slice holds.
func writeSpecsFrom(args map[string]any) []map[string]any {
	var out []map[string]any
	fs, ok := args["files"].([]any)
	if !ok {
		return nil
	}
	for _, e := range fs {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func seedBinary(t *testing.T, dir, path string, body []byte, st State) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, path), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.save(dir); err != nil {
		t.Fatal(err)
	}
}

// Shape 2, server still at the ledger etag, local bytes edited. The file must
// be sent, guarded by the recorded etag.
func TestPlainPushSendsABinaryDsxItselfUploadedGuardedByItsEtag(t *testing.T) {
	dir := t.TempDir()
	sent := []byte("edited-png-bytes\xff\xfe")
	seedBinary(t, dir, "logo.png", sent, State{ProjectID: "p1", Files: map[string]FileState{
		// shape 2: Binary carried beside a SHA that differs from the file on
		// disk, i.e. a local edit.
		"logo.png": {Etag: "e1", Binary: true, Size: int64(len(binPNG)), SHA: SHA256Hex(binPNG)},
	}})

	var specs []map[string]any
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("logo.png", "e1", int64(len(binPNG))))}
		case "write_files":
			specs = append(specs, writeSpecsFrom(args)...)
			return fakeReply{Text: `{"etags":{"logo.png":"e2"},"written":1}`}
		case "finalize_plan":
			return fakeReply{Text: `{"plan_token":"tok"}`}
		}
		return fakeReply{Text: "[]"}
	})

	rep, err := Push(context.Background(), fakeClient(f), PushOpts{ProjectID: "p1", Dir: dir})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	if len(specs) != 1 {
		t.Fatalf("a plain push of a binary dsx itself uploaded sent %d files, want 1.\n"+
			"rendered: %s", len(specs), rep.Render(false))
	}
	if got := specs[0]["if_match"]; got != "e1" {
		t.Errorf("if_match = %v, want the ledger etag %q", got, "e1")
	}

	rendered := rep.Render(false)
	if !strings.Contains(rendered, "pushed 1") {
		t.Errorf("rendered line does not report the write:\n%s", rendered)
	}
	if out := rep.Outcome(); out != nil {
		t.Errorf("Outcome is non-nil -> exit %d on an ordinary successful push: %v",
			dsxerr.ExitCodeFor(out), out)
	}
}

// The server moves between the listing and the write; the if_match must be
// what makes it refuse, with nothing landing.
func TestABinaryWriteIsRefusedWhenTheServerMovesAfterTheListing(t *testing.T) {
	dir := t.TempDir()
	seedBinary(t, dir, "logo.png", []byte("mine\xff"), State{ProjectID: "p1", Files: map[string]FileState{
		"logo.png": {Etag: "e1", Binary: true, Size: int64(len(binPNG)), SHA: SHA256Hex(binPNG)},
	}})

	var landed []string
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("logo.png", "e1", int64(len(binPNG))))}
		case "write_files":
			// The server raced to "raced" after serving the listing, so the
			// ledger's "e1" is refused. An ABSENT if_match is not a guard here
			// and the write lands.
			for _, s := range writeSpecsFrom(args) {
				if im, ok := s["if_match"]; ok && im != "raced" {
					return fakeReply{
						Text:    `{"conflicts":[{"path":"logo.png","etag":"raced"}]}`,
						IsError: true,
					}
				}
				landed = append(landed, s["path"].(string))
			}
			return fakeReply{Text: `{"etags":{"logo.png":"e2"},"written":1}`}
		case "finalize_plan":
			return fakeReply{Text: `{"plan_token":"tok"}`}
		}
		return fakeReply{Text: "[]"}
	})

	rep, err := Push(context.Background(), fakeClient(f), PushOpts{ProjectID: "p1", Dir: dir})
	if err == nil {
		t.Fatalf("a racing server accepted the write: Push returned nil.\nrendered: %s", rep.Render(false))
	}
	if len(landed) != 0 {
		t.Errorf("bytes landed despite the race: %v -- the if_match is a field, not a guard", landed)
	}
	if got := dsxerr.ExitCodeFor(err); got != dsxerr.KindConflict.ExitCode() {
		t.Errorf("exit code = %d, want %d (conflict): %v", got, dsxerr.KindConflict.ExitCode(), err)
	}
}

// Shape 2 whose server copy HAS moved: the push must refuse, in the binary
// wording rather than the generic one.
func TestPlainPushRefusesABinaryWhoseServerCopyMovedAhead(t *testing.T) {
	dir := t.TempDir()
	seedBinary(t, dir, "logo.png", []byte("mine\xff"), State{ProjectID: "p1", Files: map[string]FileState{
		"logo.png": {Etag: "e1", Binary: true, Size: int64(len(binPNG)), SHA: SHA256Hex(binPNG)},
	}})

	var specs []map[string]any
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			// moved ahead of the ledger
			return fakeReply{Text: listingFor(fileEntry("logo.png", "e2", int64(len(binPNG))))}
		case "write_files":
			specs = append(specs, writeSpecsFrom(args)...)
			return fakeReply{Text: `{"etags":{"logo.png":"e3"},"written":1}`}
		case "finalize_plan":
			return fakeReply{Text: `{"plan_token":"tok"}`}
		}
		return fakeReply{Text: "[]"}
	})

	rep, err := Push(context.Background(), fakeClient(f), PushOpts{ProjectID: "p1", Dir: dir})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if len(specs) != 0 {
		t.Errorf("dsx overwrote a binary whose server copy it has never seen: %v", specs)
	}

	rendered := rep.Render(false)
	if !strings.Contains(rendered, "the only copy is gone") {
		t.Errorf("a moved binary is not rendered in the binary wording:\n%s\n"+
			"want the line naming that dsx cannot read the server's copy", rendered)
	}
	// The generic rung must not fire instead.
	if strings.Contains(rendered, "`dsx pull` first") {
		t.Errorf("a moved binary is rendered in the generic wording:\n%s", rendered)
	}

	// --json: the path must appear under binary_conflicts.
	var doc struct {
		Conflicts       []string `json:"conflicts"`
		BinaryConflicts []string `json:"binary_conflicts"`
	}
	if err := json.Unmarshal([]byte(rep.Render(true)), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.BinaryConflicts) != 1 || doc.BinaryConflicts[0] != "logo.png" {
		t.Errorf("binary_conflicts = %v, want [logo.png]", doc.BinaryConflicts)
	}
	if len(doc.Conflicts) != 1 || doc.Conflicts[0] != "logo.png" {
		t.Errorf("conflicts = %v, want [logo.png]", doc.Conflicts)
	}

	out := rep.Outcome()
	if out == nil {
		t.Fatalf("Outcome is nil -> exit 0 on a run that refused to write")
	}
	if got := dsxerr.ExitCodeFor(out); got != dsxerr.KindConflict.ExitCode() {
		t.Errorf("exit code = %d, want %d", got, dsxerr.KindConflict.ExitCode())
	}
	if strings.Contains(out.Error(), "`dsx pull` first") {
		t.Errorf("Outcome hint gives an agent the generic wording for a binary conflict:\n%v", out)
	}
}

// Shape 1 -- SHA "" -- must STILL refuse. Asserted end-to-end: zero writes
// reach the server.
func TestPlainPushStillRefusesABinaryDsxHasNeverRead(t *testing.T) {
	dir := t.TempDir()
	seedBinary(t, dir, "logo.png", []byte("placeholder!"), State{ProjectID: "p1", Files: map[string]FileState{
		// No SHA: the refusal shape.
		"logo.png": {Etag: "e1", Binary: true},
	}})

	var specs []map[string]any
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("logo.png", "e1", 2<<20))}
		case "write_files":
			specs = append(specs, writeSpecsFrom(args)...)
			return fakeReply{Text: `{"etags":{"logo.png":"e2"},"written":1}`}
		case "finalize_plan":
			return fakeReply{Text: `{"plan_token":"tok"}`}
		}
		return fakeReply{Text: "[]"}
	})

	rep, err := Push(context.Background(), fakeClient(f), PushOpts{ProjectID: "p1", Dir: dir})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if len(specs) != 0 {
		t.Errorf("dsx blindly overwrote a server file it has never read: %v", specs)
	}
	if !strings.Contains(rep.Render(false), "the only copy is gone") {
		t.Errorf("the binary refusal wording is gone:\n%s", rep.Render(false))
	}
	out := rep.Outcome()
	if out == nil {
		t.Fatalf("Outcome is nil -> exit 0 on a refused blind overwrite")
	}
	if got := dsxerr.ExitCodeFor(out); got != dsxerr.KindConflict.ExitCode() {
		t.Errorf("exit code = %d, want %d", got, dsxerr.KindConflict.ExitCode())
	}
}

// The delete lane must not move with the write lane: --prune deletes only what
// we can prove was ours and unmodified (invariant 4). Shape 2 specifically.
func TestPlainPushPruneStillKeepsABinaryDsxUploaded(t *testing.T) {
	dir := t.TempDir()
	// logo.png is NOT on disk -- the steady state after a forced push, or simply
	// deleted locally. keep.md gives the run something legitimate to do.
	if err := os.WriteFile(filepath.Join(dir, "keep.md"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := State{ProjectID: "p1", Files: map[string]FileState{
		"logo.png": {Etag: "e1", Binary: true, Size: int64(len(binPNG)), SHA: SHA256Hex(binPNG)},
		"keep.md":  {Etag: "k1", Size: 4, SHA: SHA256Hex([]byte("keep"))},
	}}
	if err := st.save(dir); err != nil {
		t.Fatal(err)
	}

	var deleted []string
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(
				fileEntry("logo.png", "e1", int64(len(binPNG))),
				fileEntry("keep.md", "k1", 4),
			)}
		case "write_files":
			return fakeReply{Text: `{"etags":{"keep.md":"k2"},"written":1}`}
		case "delete_files":
			for _, s := range writeSpecsFrom(args) {
				deleted = append(deleted, s["path"].(string))
			}
			return fakeReply{Text: `{"deleted":1}`}
		case "finalize_plan":
			return fakeReply{Text: `{"plan_token":"tok"}`}
		}
		return fakeReply{Text: "[]"}
	})

	rep, err := Push(context.Background(), fakeClient(f), PushOpts{ProjectID: "p1", Dir: dir, Prune: true})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	for _, p := range deleted {
		if p == "logo.png" {
			t.Fatalf("push --prune deleted the server's only copy of a binary dsx cannot re-fetch "+
				"(delete_files carried %v): invariant 4.", deleted)
		}
	}
	if len(rep.Deleted) != 0 {
		t.Errorf("Deleted = %v, want empty", rep.Deleted)
	}
}
