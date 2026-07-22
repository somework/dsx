package synccmd

import (
	"context"
	"strings"
	"testing"
)

// statusAfterFetch stands a bound, fetched tree up and leaves the process
// inside it. status resolves the tree it is standing in, so the chdir is not
// scaffolding — it is how the verb is reached at all.
func statusAfterFetch(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	body := "body{color:red}\n"
	maincliWriteFile(t, dir, "shared.css", body)

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(
				fileEntry("shared.css", "e1", int64(len(body))),
				fileEntry("theirs.css", "e2", 11))}
		case "read_file":
			p, _ := args["path"].(string)
			if p == "shared.css" {
				return fakeReply{Text: envelopeFor(p, "e1", body)}
			}
		}
		return fakeReply{Text: "[]"}
	})
	c := fakeClient(f)

	if _, err := captureStdout(t, func() error {
		return cmdPin(context.Background(), c, []string{"proj-A", dir})
	}); err != nil {
		t.Fatalf("pin: %v", err)
	}
	t.Chdir(dir)
	if _, err := captureStdout(t, func() error {
		return cmdFetch(context.Background(), c, nil)
	}); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	return dir
}

// TestStatusPrintsBothHalvesUnderTheirOwnHeadings is the wiring test the old
// two-key envelope tests became: syncer.Status decides, and this proves the
// command reaches it and prints what it returned.
func TestStatusPrintsBothHalvesUnderTheirOwnHeadings(t *testing.T) {
	statusAfterFetch(t)
	maincliWriteFile(t, ".", "scratch.md", "mine\n")

	out, err := captureStdout(t, func() error { return cmdStatus(nil) })
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{"untracked locally:", "scratch.md", "as of the last dsx fetch:", "theirs.css"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output is missing %q:\n%s", want, out)
		}
	}
	// shared.css was fetched and proved, so it must not read as a conflict.
	if strings.Contains(out, "shared.css: differs") || strings.Contains(out, "untracked, differs") {
		t.Errorf("a verified path read as differing:\n%s", out)
	}
}

func TestStatusQuietPrintsNothing(t *testing.T) {
	statusAfterFetch(t)

	out, err := captureStdout(t, func() error { return cmdStatus([]string{"-q"}) })
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Errorf("-q printed %q", out)
	}
}
