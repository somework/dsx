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

type fakeReply = clitest.Reply

var (
	newFakeMCP    = clitest.New
	fakeClient    = clitest.Client
	captureStdout = clitest.CaptureStdout
	listingFor    = clitest.ListingFor
	fileEntry     = clitest.FileEntry
	mkfile        = clitest.Mkfile
)

func cmdsReplyJSON(text string) func(string, map[string]any) fakeReply {
	return func(string, map[string]any) fakeReply { return fakeReply{Text: text} }
}

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
