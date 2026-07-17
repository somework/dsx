package files

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/somework/dsx/internal/clitest"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/syncer"
)

// These tests drive cmdTree/cmdPut directly, so they must be internal (package
// files) tests — the wrappers are unexported on purpose. The fake endpoint lives
// in internal/clitest, shared by every command package; these aliases keep the
// moved tests spelled as they were in internal/cli. See internal/cli/fake_test.go
// for why the adapter lives in internal/clitest rather than being reimplemented.
type fakeReply = clitest.Reply

var (
	newFakeMCP    = clitest.New
	fakeClient    = clitest.Client
	captureStdout = clitest.CaptureStdout
	listingFor    = clitest.ListingFor
	fileEntry     = clitest.FileEntry
	mkfile        = clitest.Mkfile
)

// cmdsReplyJSON answers every tool with one fixed JSON document.
func cmdsReplyJSON(text string) func(string, map[string]any) fakeReply {
	return func(string, map[string]any) fakeReply { return fakeReply{Text: text} }
}

// TestCmdTreeClampsConcurrencyBelowOneToOne.
//
// Not cosmetic: syncer.WalkTree sizes its semaphore from this number, and a zero-sized
// buffered channel is an unbuffered one, on which the first send blocks forever.
// Without the clamp `-j 0` hangs until the context dies rather than listing
// anything, so the deadline below is what makes the regression visible as a
// failure instead of a stall.
func TestCmdTreeClampsConcurrencyBelowOneToOne(t *testing.T) {
	f := newFakeMCP(t, cmdsReplyJSON(listingFor(fileEntry("a.css", "e1", 1))))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := captureStdout(t, func() error {
		return cmdTree(ctx, fakeClient(f), []string{"p1", "-j", "0", "--json"})
	})
	if err != nil {
		t.Fatalf("tree -j 0 failed: %v", err)
	}

	var got []syncer.RemoteEntry
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout did not parse: %v (%q)", err, out)
	}
	if len(got) != 1 || got[0].Path != "a.css" {
		t.Errorf("tree = %#v, want the one file", got)
	}
}

func TestPutClassifiesAConflictEvenWithACallerSuppliedPlanToken(t *testing.T) {
	// emitWrite short-circuited to emit() when the caller passed --plan, and
	// emit() never classifies. Same tool, same reply, opposite exit code: 3
	// without --plan, 1 with it. The live test that "pinned" this called
	// conflictFromToolError directly and never went through cmdPut, so it passed.
	body := `{"conflicts":[{"path":"a.css","etag":"999"}],"message":"write_files: refused — … Nothing was written."}`
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: body, IsError: true}
	})

	dir := t.TempDir()
	mkfile(t, dir, "a.css", "x")
	err := cmdPut(t.Context(), fakeClient(f), []string{
		"p1", "a.css", filepath.Join(dir, "a.css"), "--plan", "tok", "--if-match", "stale",
	})
	if err == nil {
		t.Fatal("the server refused the write and put reported success")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindConflict {
		t.Fatalf("put --plan classified a conflict as %q (exit %d); without --plan it is %q (exit %d). "+
			"Same tool, same reply, opposite answer.", got, got.ExitCode(), dsxerr.KindConflict, dsxerr.ExitConflict)
	}
}

func TestPutSelfAuthorisesLikePushDoes(t *testing.T) {
	// push recovers from needs_project_grant by minting a path-scoped
	// plan_token. put, cp and support-js went through emit and did not, so the
	// same write that push completes left `dsx put` at exit 1 with
	// {"error":"error"} and no next step — on a project that has no standing
	// grant, which is the default.
	var sawPlan bool
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "write_files":
			if _, ok := args["plan_token"]; !ok {
				return fakeReply{
					HTTPStatus: 403,
					HTTPBody:   `{"error":"needs_project_grant","project_id":"p1","prompt":"Allow Claude to edit this project?"}`,
				}
			}
			return fakeReply{Text: `{"etags":{"a.css":"e9"},"written":1}`}
		case "finalize_plan":
			sawPlan = true
			return fakeReply{Text: `{"plan_token":"plan_abc"}`}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	dir := t.TempDir()
	mkfile(t, dir, "a.css", "body{}")
	err := cmdPut(t.Context(), fakeClient(f), []string{"p1", "a.css", filepath.Join(dir, "a.css")})
	if err != nil {
		t.Fatalf("put did not recover from needs_project_grant the way push does: %v", err)
	}
	if !sawPlan {
		t.Error("put never called finalize_plan")
	}
}
