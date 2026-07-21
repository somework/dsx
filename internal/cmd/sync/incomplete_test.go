package synccmd

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/syncer"
)

// The report goes to stdout and the error to stderr, so `dsx pull > log` keeps
// only the reassuring half. A run that ended in an error must not read as a
// clean zero-byte success.
func TestAFailedPullRendersAnIncompleteReport(t *testing.T) {
	dir := t.TempDir()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{IsError: true, Text: "boom"}
	})

	out, err := captureStdout(t, func() error {
		return cmdSync(context.Background(), fakeClient(f), "pull", []string{syncBound(t, dir, "proj-A")})
	})
	if err == nil {
		t.Fatal("pull succeeded against a dead endpoint")
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "incomplete:") {
		t.Errorf("stdout=%q, want it to open with \"incomplete:\" — a reader piping stdout\n"+
			"sees only this line and would read the run as a clean success", out)
	}
}

func TestAFailedPushRendersAnIncompleteReport(t *testing.T) {
	dir := t.TempDir()
	syncSeedState(t, dir, syncer.State{ProjectID: "proj-A", Files: map[string]syncer.FileState{}})
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{IsError: true, Text: "boom"}
	})

	out, err := captureStdout(t, func() error {
		return cmdSync(context.Background(), fakeClient(f), "push", []string{syncBound(t, dir, "proj-A")})
	})
	if err == nil {
		t.Fatal("push succeeded against a dead endpoint")
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "incomplete:") {
		t.Errorf("stdout=%q, want an \"incomplete:\" prefix", out)
	}
}

// The success bytes must not move: omitempty keeps the key out entirely.
func TestASuccessfulPullJSONHasNoIncompleteKey(t *testing.T) {
	dir := t.TempDir()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})

	out, err := captureStdout(t, func() error {
		return cmdSync(context.Background(), fakeClient(f), "pull", []string{syncBound(t, dir, "proj-A"), "--json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if uErr := json.Unmarshal([]byte(out), &m); uErr != nil {
		t.Fatalf("stdout is not one JSON document: %v (%q)", uErr, out)
	}
	if _, present := m["incomplete"]; present {
		t.Errorf("a successful run emitted an incomplete key: %q", out)
	}
}

func TestAFailedPullJSONCarriesIncomplete(t *testing.T) {
	dir := t.TempDir()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{IsError: true, Text: "boom"}
	})

	out, err := captureStdout(t, func() error {
		return cmdSync(context.Background(), fakeClient(f), "pull", []string{syncBound(t, dir, "proj-A"), "--json"})
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

// status is a two-key {pull,push} envelope. The comment at the push-error
// return already says why a lone report must not be rendered there; the pull
// half rendered one anyway.
func TestStatusRendersNoLoneReportWhenThePullHalfFails(t *testing.T) {
	dir := t.TempDir()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{IsError: true, Text: "boom"}
	})

	out, err := captureStdout(t, func() error {
		return cmdSync(context.Background(), fakeClient(f), "status", []string{syncBound(t, dir, "proj-A")})
	})
	if err == nil {
		t.Fatal("status succeeded against a failing endpoint")
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("status printed %q on an error path; it is a two-key {pull,push}\n"+
			"envelope and a lone half is not one", out)
	}
}

func TestStatusJSONOnAnErrorPathIsNotALoneReport(t *testing.T) {
	dir := t.TempDir()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{IsError: true, Text: "boom"}
	})

	out, err := captureStdout(t, func() error {
		return cmdSync(context.Background(), fakeClient(f), "status", []string{syncBound(t, dir, "proj-A"), "--json"})
	})
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.TrimSpace(out) == "" {
		return
	}
	var m map[string]any
	if uErr := json.Unmarshal([]byte(out), &m); uErr != nil {
		t.Fatalf("stdout is not one JSON document: %v (%q)", uErr, out)
	}
	if _, ok := m["pull"]; !ok {
		t.Errorf("status --json emitted a bare report, not the {pull,push} envelope: %q", out)
	}
}
