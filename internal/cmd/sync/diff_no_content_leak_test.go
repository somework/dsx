package synccmd

import (
	"context"
	"strings"
	"testing"
)

// TestDiffNeverPrintsFileContent: diff's own doc comment says "it never
// prints a hunk" (dsx exists so bytes do not pass through a model's context).
// Nothing previously asserted this — a stub that leaked downloaded content
// onto stdout in the Differs branch would pass every other diff test
// untouched. remoteMarker only ever exists in the differing bodies' bytes,
// never in a path name or a classification word, so its absence from stdout
// is proof no content reached the terminal, in both render modes.
func TestDiffNeverPrintsFileContent(t *testing.T) {
	dir := t.TempDir()
	const remoteMarker = "XKCD-REMOTE-BODY-MARKER-7f3a"
	localBody := "local version, no marker here\n"
	remoteBody := "remote version carries " + remoteMarker + " inside it\n"
	maincliWriteFile(t, dir, "differs.css", localBody)

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(fileEntry("differs.css", "e1", int64(len(remoteBody))))}
		}
		p, _ := args["path"].(string)
		return fakeReply{Text: envelopeFor(p, "e1", remoteBody)}
	})

	textOut, err := captureStdout(t, func() error {
		return cmdDiff(context.Background(), fakeClient(f), []string{"proj-A", dir})
	})
	if err != nil {
		t.Fatalf("cmdDiff: %v", err)
	}
	if strings.Contains(textOut, remoteMarker) {
		t.Errorf("text-mode diff output leaked the remote body's content: %q", textOut)
	}
	if strings.Contains(textOut, localBody) {
		t.Errorf("text-mode diff output leaked the local body's content: %q", textOut)
	}

	f2 := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(fileEntry("differs.css", "e1", int64(len(remoteBody))))}
		}
		p, _ := args["path"].(string)
		return fakeReply{Text: envelopeFor(p, "e1", remoteBody)}
	})
	jsonOut, err := captureStdout(t, func() error {
		return cmdDiff(context.Background(), fakeClient(f2), []string{"proj-A", dir, "--json"})
	})
	if err != nil {
		t.Fatalf("cmdDiff --json: %v", err)
	}
	if strings.Contains(jsonOut, remoteMarker) {
		t.Errorf("--json diff output leaked the remote body's content: %q", jsonOut)
	}
	if strings.Contains(jsonOut, localBody) {
		t.Errorf("--json diff output leaked the local body's content: %q", jsonOut)
	}
}
