package synccmd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestAFailedDiffRendersAnIncompleteReport mirrors
// TestAFailedFetchRendersAnIncompleteReport: cmdDiff's error path (rep.
// Incomplete = true, then print, then return err) had no test at any level.
func TestAFailedDiffRendersAnIncompleteReport(t *testing.T) {
	dir := t.TempDir()
	maincliWriteFile(t, dir, "a.css", "abc\n")
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{IsError: true, Text: "boom"}
	})

	out, err := captureStdout(t, func() error {
		return cmdDiff(context.Background(), fakeClient(f), syncIn(t, dir, "proj-A"))
	})
	if err == nil {
		t.Fatal("diff succeeded against a dead endpoint")
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "incomplete") {
		t.Errorf("stdout=%q, want it to open with \"incomplete\" — a reader piping stdout\n"+
			"sees only this line and would read the run as a clean success", out)
	}
}

func TestAFailedDiffJSONCarriesIncomplete(t *testing.T) {
	dir := t.TempDir()
	maincliWriteFile(t, dir, "a.css", "abc\n")
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{IsError: true, Text: "boom"}
	})

	out, err := captureStdout(t, func() error {
		return cmdDiff(context.Background(), fakeClient(f), append(syncIn(t, dir, "proj-A"), "--json"))
	})
	if err == nil {
		t.Fatal("want an error")
	}
	var m map[string]any
	if uErr := json.Unmarshal([]byte(out), &m); uErr != nil {
		t.Fatalf("stdout is not one JSON document: %v (%q)", uErr, out)
	}
	if m["incomplete"] != true {
		t.Errorf("failed run did not mark the report incomplete: %q", out)
	}
}
