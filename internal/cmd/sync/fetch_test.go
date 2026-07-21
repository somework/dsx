package synccmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/syncer"
)

// TestFetchRefusesAForeignEndpointBeforeTheRoundTrip: invariant 13's binding
// is (project, endpoint), and fetch must check it before dialing anything —
// list_files must never be called against a ledger bound to a different
// server.
func TestFetchRefusesAForeignEndpointBeforeTheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	syncSeedState(t, dir, syncer.State{
		ProjectID: "proj-A",
		Endpoint:  "https://elsewhere.example/mcp",
	})

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})
	_, err := captureStdout(t, func() error {
		return cmdFetch(context.Background(), fakeClient(f), []string{syncBound(t, dir, "proj-A")})
	})
	if err == nil {
		t.Fatal("fetch accepted a directory bound to a different endpoint")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
	if got := f.CountTool("list_files"); got != 0 {
		t.Errorf("list_files called %d times, want 0 — the endpoint guard must run before the round trip", got)
	}
}

// TestFetchRefusesAMissingDirectory: fetch makes no round trip on its own
// typo, same as pin and diff, and for the same reason cmdSync's dry runs do —
// an empty local scan is what makes push --prune read the whole server tree
// as user deletions.
func TestFetchRefusesAMissingDirectory(t *testing.T) {
	parent := t.TempDir()
	missing := filepath.Join(parent, "typo")

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})
	_, err := captureStdout(t, func() error {
		return cmdFetch(context.Background(), fakeClient(f), []string{missing})
	})
	if err == nil {
		t.Fatal("fetch accepted a directory that does not exist")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
	if _, statErr := os.Stat(missing); statErr == nil {
		t.Error("fetch created the directory it refused — it must leave no trace")
	}
	if got := f.CountTool("list_files"); got != 0 {
		t.Errorf("list_files called %d times, want 0", got)
	}
}

// TestFetchWritesTheReportAndSucceeds is the thin cmd-layer wiring check: the
// syncer-level behaviour is already exhaustively covered in
// internal/syncer/fetch_test.go, so this only proves cmdFetch actually calls
// through and prints a report.
func TestFetchWritesTheReportAndSucceeds(t *testing.T) {
	dir := t.TempDir()
	body := []byte("hello\n")
	maincliWriteFile(t, dir, "a.css", string(body))

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(fileEntry("a.css", "e1", int64(len(body))))}
		}
		p, _ := args["path"].(string)
		return fakeReply{Text: envelopeFor(p, "e1", string(body))}
	})
	out, err := captureStdout(t, func() error {
		return cmdFetch(context.Background(), fakeClient(f), []string{syncBound(t, dir, "proj-A")})
	})
	if err != nil {
		t.Fatalf("cmdFetch errored: %v", err)
	}
	if out == "" {
		t.Error("cmdFetch printed nothing")
	}
	if _, err := os.Stat(syncer.BaselinePath(dir)); err != nil {
		t.Errorf("baseline.json was not written: %v", err)
	}
}

// TestAFailedFetchRendersAnIncompleteReport: cmdFetch's error path (rep.
// Incomplete = true, then print, then return err) had no test at any level —
// mirrors incomplete_test.go's pull/push coverage, which this command never
// shared.
func TestAFailedFetchRendersAnIncompleteReport(t *testing.T) {
	dir := t.TempDir()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{IsError: true, Text: "boom"}
	})

	out, err := captureStdout(t, func() error {
		return cmdFetch(context.Background(), fakeClient(f), []string{syncBound(t, dir, "proj-A")})
	})
	if err == nil {
		t.Fatal("fetch succeeded against a dead endpoint")
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "incomplete:") {
		t.Errorf("stdout=%q, want it to open with \"incomplete:\"", out)
	}
}

func TestAFailedFetchJSONCarriesIncomplete(t *testing.T) {
	dir := t.TempDir()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{IsError: true, Text: "boom"}
	})

	out, err := captureStdout(t, func() error {
		return cmdFetch(context.Background(), fakeClient(f), []string{syncBound(t, dir, "proj-A"), "--json"})
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
