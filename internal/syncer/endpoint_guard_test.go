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

const guardProject = "proj-A"

// seedEndpointLedger writes a ledger that tracks one unmodified file, bound to
// project and endpoint.
func seedEndpointLedger(t *testing.T, dir, endpoint string, body []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "a.css"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	st := State{
		ProjectID: guardProject,
		Endpoint:  endpoint,
		Files: map[string]FileState{
			"a.css": {Etag: "e1", Size: int64(len(body)), SHA: SHA256Hex(body)},
		},
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, StateFileName), append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A ledger synced against one host, then pointed at another, must refuse.
// Without the guard the second host's empty listing reads as "every tracked
// file was deleted on the server" and --prune removes the tree unforced.
func TestPullRefusesAnEndpointTheLedgerWasNotSyncedAgainst(t *testing.T) {
	dir := t.TempDir()
	body := []byte("a{}")
	seedEndpointLedger(t, dir, "https://api.anthropic.com/v1/design/mcp", body)

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})
	c := fakeClient(f)

	_, err := Pull(context.Background(), c, PullOpts{
		ProjectID: guardProject, Dir: dir, Concurrency: 1, Prune: true,
	})
	if err == nil {
		t.Fatal("Pull succeeded against a foreign endpoint; want a refusal")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "a.css")); statErr != nil {
		t.Errorf("a.css was removed by a pull that should never have planned: %v", statErr)
	}
}

func TestPushRefusesAnEndpointTheLedgerWasNotSyncedAgainst(t *testing.T) {
	dir := t.TempDir()
	body := []byte("a{}")
	seedEndpointLedger(t, dir, "https://api.anthropic.com/v1/design/mcp", body)

	var called []string
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		called = append(called, name)
		return fakeReply{Text: listingFor()}
	})
	c := fakeClient(f)

	_, err := Push(context.Background(), c, PushOpts{
		ProjectID: guardProject, Dir: dir, Concurrency: 1, Prune: true,
	})
	if err == nil {
		t.Fatal("Push succeeded against a foreign endpoint; want a refusal")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
	if len(called) != 0 {
		t.Errorf("tools called=%v, want none — the refusal must precede the network", called)
	}
}

// --force does not unlock it, matching the project guard beside it.
func TestEndpointRefusalIsNotUnlockedByForce(t *testing.T) {
	dir := t.TempDir()
	seedEndpointLedger(t, dir, "https://api.anthropic.com/v1/design/mcp", []byte("a{}"))

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})
	c := fakeClient(f)

	if _, err := Pull(context.Background(), c, PullOpts{
		ProjectID: guardProject, Dir: dir, Concurrency: 1, Prune: true, Force: true,
	}); err == nil {
		t.Fatal("--force unlocked the endpoint refusal")
	}
}

// A ledger written before dsx recorded the endpoint has the field empty; it
// must keep working.
func TestAnEndpointlessLedgerStillSyncs(t *testing.T) {
	dir := t.TempDir()
	body := []byte("a{}")
	seedEndpointLedger(t, dir, "", body)

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor(fileEntry("a.css", "e1", int64(len(body))))}
	})
	c := fakeClient(f)

	if _, err := Pull(context.Background(), c, PullOpts{
		ProjectID: guardProject, Dir: dir, Concurrency: 1,
	}); err != nil {
		t.Fatalf("a ledger with no recorded endpoint was refused: %v", err)
	}
}

// The path is not part of the identity: a vendor moving /v1/design/mcp to
// /v2/design/mcp must not strand every ledger on disk.
func TestOnlySchemeAndHostDecideEndpointIdentity(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b string
		same bool
	}{
		{"identical", "https://x.example/v1/mcp", "https://x.example/v1/mcp", true},
		{"path moved", "https://x.example/v1/mcp", "https://x.example/v2/mcp", true},
		{"query added", "https://x.example/mcp", "https://x.example/mcp?beta=1", true},
		{"host case", "https://X.Example/mcp", "https://x.example/mcp", true},
		{"other host", "https://x.example/mcp", "https://evil.example/mcp", false},
		{"other scheme", "https://x.example/mcp", "http://x.example/mcp", false},
		{"other port", "https://x.example:443/mcp", "https://x.example:8443/mcp", false},
		{"unparseable pair", "::not a url", "::not a url", true},
		{"unparseable vs real", "::not a url", "https://x.example/mcp", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameEndpoint(tc.a, tc.b); got != tc.same {
				t.Errorf("sameEndpoint(%q, %q)=%v, want %v", tc.a, tc.b, got, tc.same)
			}
		})
	}
}

// The refusal must not tell the reader to delete the ledger: clearing it makes
// every path untracked, and push --force then writes with no if_match at all
// (plan.go's conflict switch is skipped, leaving IfMatch empty).
func TestEndpointRefusalDoesNotAdviseDeletingTheLedger(t *testing.T) {
	dir := t.TempDir()
	seedEndpointLedger(t, dir, "https://api.anthropic.com/v1/design/mcp", []byte("a{}"))

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})
	_, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: guardProject, Dir: dir, Concurrency: 1,
	})
	if err == nil {
		t.Fatal("want a refusal")
	}
	for _, bad := range []string{"delete the ledger", "remove the ledger", "rm "} {
		if strings.Contains(strings.ToLower(err.Error()), bad) {
			t.Errorf("refusal advises %q, which opens an unguarded --force write: %s", bad, err)
		}
	}
}
