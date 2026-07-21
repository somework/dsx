package synccmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/syncer"
)

// TestPinRefusesAMissingDirectory: pin makes no round trip on its own typo,
// same as fetch and for the same reason cmdSync's dry runs do — an empty
// local scan is what makes push --prune read the whole server tree as user
// deletions, so "the directory is not there" must never reach a plan.
func TestPinRefusesAMissingDirectory(t *testing.T) {
	parent := t.TempDir()
	missing := filepath.Join(parent, "typo")

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})
	_, err := captureStdout(t, func() error {
		return cmdPin(context.Background(), fakeClient(f), []string{"proj-A", missing})
	})
	if err == nil {
		t.Fatal("pin accepted a directory that does not exist")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
	if _, statErr := os.Stat(missing); statErr == nil {
		t.Error("pin created the directory it refused — it must leave no trace")
	}
	if got := f.CountTool("list_files"); got != 0 {
		t.Errorf("list_files called %d times, want 0", got)
	}
}

// TestPinThenFetchThenStatusReportsNoConflicts is the only end-to-end test of
// the four capabilities as the workflow actually asked for: bring an
// already-populated directory under dsx control (pin, unlike clone, does not
// require an empty one), prove its bytes match the server (fetch), and see
// status agree there is nothing to resolve.
//
// The second half — added per the design doc's own "least sure of" note —
// creates a new file between the fetch and the status and records the exact
// output text a human would see, without tuning the implementation to hide
// it: the design is what it is, and whether that reads badly is the owner's
// call.
func TestPinThenFetchThenStatusReportsNoConflicts(t *testing.T) {
	dir := t.TempDir()
	body := "body{color:red}\n"
	newBody := "new{display:none}\n"
	maincliWriteFile(t, dir, "shared.css", body)

	// new.css is on the server from the start but not yet on disk, so fetch's
	// narrow present-and-untracked set (syncer.Fetch's doc comment) skips it —
	// exactly the shape that lets a file "appear between fetch and status".
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(
				fileEntry("shared.css", "e1", int64(len(body))),
				fileEntry("new.css", "e2", int64(len(newBody))),
			)}
		case "read_file":
			p, _ := args["path"].(string)
			switch p {
			case "shared.css":
				return fakeReply{Text: envelopeFor(p, "e1", body)}
			case "new.css":
				return fakeReply{Text: envelopeFor(p, "e2", newBody)}
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

	if _, err := captureStdout(t, func() error {
		return cmdFetch(context.Background(), c, []string{dir})
	}); err != nil {
		t.Fatalf("fetch: %v", err)
	}

	statusJSON := func() (pull syncer.PullReport, push syncer.PushReport) {
		t.Helper()
		out, err := captureStdout(t, func() error {
			return cmdSync(context.Background(), c, "status", []string{dir, "--json"})
		})
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		var got struct {
			Pull syncer.PullReport `json:"pull"`
			Push syncer.PushReport `json:"push"`
		}
		if uErr := json.Unmarshal([]byte(out), &got); uErr != nil {
			t.Fatalf("status --json is not one JSON document: %v\n%s", uErr, out)
		}
		return got.Pull, got.Push
	}

	pull, push := statusJSON()
	if len(pull.Conflicts) != 0 {
		t.Errorf("pull conflicts = %v, want none", pull.Conflicts)
	}
	if len(push.Conflicts) != 0 {
		t.Errorf("push conflicts = %v, want none", push.Conflicts)
	}
	if pull.Verified != 1 {
		t.Errorf("pull verified = %d, want 1 (shared.css)", pull.Verified)
	}

	// Now the file appears — byte-identical to what the server already holds,
	// but dsx has no way to know that without downloading it.
	maincliWriteFile(t, dir, "new.css", newBody)

	pull2, push2 := statusJSON()
	pullText, err := captureStdout(t, func() error {
		return cmdSync(context.Background(), c, "status", []string{dir})
	})
	if err != nil {
		t.Fatalf("status (text): %v", err)
	}

	// This is the finding, not a bug this test is meant to catch: a file
	// created after `dsx fetch` reads as a conflict even when its bytes are
	// identical to the server's, because nothing ever verified it. Report the
	// actual text; do not change the design to make this assertion pass
	// differently — see "surprises" in the C9 report.
	if !containsPath(pull2.Conflicts, "new.css") {
		t.Errorf("pull conflicts after adding new.css = %v, want it to include new.css "+
			"(unverified — this is the documented gap, not a defect)", pull2.Conflicts)
	}
	if !containsPath(push2.Conflicts, "new.css") {
		t.Errorf("push conflicts after adding new.css = %v, want it to include new.css", push2.Conflicts)
	}
	// shared.css must still read clean — the new conflict must not spill onto
	// the path that was actually fetched and verified.
	if containsPath(pull2.Conflicts, "shared.css") || containsPath(push2.Conflicts, "shared.css") {
		t.Errorf("shared.css leaked into a conflict list: pull=%v push=%v", pull2.Conflicts, push2.Conflicts)
	}

	t.Logf("dsx status (text) after adding a file identical to the server's copy, "+
		"without re-running fetch:\n%s", pullText)
}

// TestPinRecordsTheClientsEndpoint proves cmdPin's wiring of c.Endpoint()
// into syncer.PinOpts.Endpoint (internal/cmd/sync/pin.go) end to end.
// syncer.Pin's own unit tests build PinOpts by hand and never exercise this
// line, so a regression here (e.g. hardcoding Endpoint: "") would leave
// every ledger written by `dsx pin` with Endpoint == "", silently defeating
// invariant 13's endpoint half for every directory ever bound via pin.
func TestPinRecordsTheClientsEndpoint(t *testing.T) {
	dir := t.TempDir()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})
	c := fakeClient(f)
	if c.Endpoint() == "" {
		t.Fatal("test fixture's own client has no endpoint — the test would prove nothing")
	}

	if _, err := captureStdout(t, func() error {
		return cmdPin(context.Background(), c, []string{"proj-A", dir})
	}); err != nil {
		t.Fatalf("pin: %v", err)
	}

	st, err := syncer.LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState after pin: %v", err)
	}
	if st.Endpoint != c.Endpoint() {
		t.Errorf("ledger Endpoint = %q, want %q (the client's own endpoint)", st.Endpoint, c.Endpoint())
	}
}

// TestPinWithOnePositionalDefaultsDirToCurrentDirectory covers cmdPin's
// case 1 branch (`dsx pin <project>`, dir defaulting to "."), untested
// until now: extraargs_test.go's baseInvocations and every other pin_test.go
// case always pass two positionals.
func TestPinWithOnePositionalDefaultsDirToCurrentDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})
	if _, err := captureStdout(t, func() error {
		return cmdPin(context.Background(), fakeClient(f), []string{"proj-A"})
	}); err != nil {
		t.Fatalf("pin: %v", err)
	}

	st, err := syncer.LoadState(".")
	if err != nil {
		t.Fatalf("LoadState after pin: %v", err)
	}
	if st.ProjectID != "proj-A" {
		t.Errorf("ProjectID = %q, want proj-A — dir must have defaulted to the current directory", st.ProjectID)
	}
}

// TestPinSupportsJSONOutput: usageFooter documents --json for "every
// command" (internal/cli/usage.go), and every other command in the binary
// wires cmd.JSONFlag. cmdPin did not, so `dsx pin <project> <dir> --json`
// failed at flag parsing instead of producing machine-readable output.
func TestPinSupportsJSONOutput(t *testing.T) {
	dir := t.TempDir()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})

	out, err := captureStdout(t, func() error {
		return cmdPin(context.Background(), fakeClient(f), []string{"proj-A", dir, "--json"})
	})
	if err != nil {
		t.Fatalf("pin --json: %v", err)
	}
	var got struct {
		Project string `json:"project"`
		Dir     string `json:"dir"`
	}
	if uErr := json.Unmarshal([]byte(out), &got); uErr != nil {
		t.Fatalf("pin --json did not print one JSON document: %v\n%s", uErr, out)
	}
	if got.Project != "proj-A" || got.Dir != dir {
		t.Errorf("got %+v, want project=proj-A dir=%s", got, dir)
	}
}

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}
