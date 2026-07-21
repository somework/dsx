package synccmd

import (
	"os"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/syncer"
)

const sampleProjectID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

func noLedger(string) (string, string, error) { return "", "", nil }

// This file exists because one refusal, twice, advised creating a directory
// named after a project id — first as `dsx pull <project> <uuid>`, then, after
// the directory check was hoisted, as `dsx clone <project> <uuid>`. Both times
// the advice was reached by typing a lone project id where dsx wanted a
// directory. The verbs take no argument now, so the habit lands on a single
// refusal, and this is the guard on what that refusal says.
func TestASoleProjectIDPositionalIsNotAdvisedAsADirectory(t *testing.T) {
	t.Chdir(t.TempDir())

	_, _, err := resolveSyncTarget("pull", []string{sampleProjectID}, noLedger)
	if err == nil {
		t.Fatal("a lone project id was accepted")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
	msg := err.Error()
	if strings.Contains(msg, sampleProjectID+" "+sampleProjectID) {
		t.Errorf("refusal advises creating a directory named after the project:\n%s", msg)
	}
	if strings.Contains(msg, "to create it") {
		t.Errorf("refusal advises creating the id-named path at all:\n%s", msg)
	}
	if !strings.Contains(msg, "looks like a project id") {
		t.Errorf("an id-shaped positional was answered as an ordinary one:\n%s", msg)
	}
	if !strings.Contains(msg, "<dir>") {
		t.Errorf("refusal does not show where the directory goes:\n%s", msg)
	}
}

// The positive control: an ordinary positional must keep the ordinary refusal,
// which names -C. Without it the id-shaped branch could swallow every argument
// and this file would still be green.
func TestAnOrdinaryPositionalIsAnsweredWithDashC(t *testing.T) {
	t.Chdir(t.TempDir())

	_, _, err := resolveSyncTarget("pull", []string{"design"}, noLedger)
	if err == nil {
		t.Fatal("a positional was accepted")
	}
	msg := err.Error()
	if strings.Contains(msg, "looks like a project id") {
		t.Errorf("an ordinary path was answered as an id:\n%s", msg)
	}
	if !strings.Contains(msg, "dsx -C design pull") {
		t.Errorf("refusal does not name the replacement:\n%s", msg)
	}
}

// Both branches refuse; neither resolves anything. A wrong guess about the
// id's shape costs the reader the other message, never a different action.
func TestBothRefusalsAreRefusals(t *testing.T) {
	t.Chdir(t.TempDir())

	for _, pos := range []string{sampleProjectID, "design"} {
		project, dir, err := resolveSyncTarget("push", []string{pos}, noLedger)
		if err == nil {
			t.Fatalf("%q was accepted", pos)
		}
		if project != "" || dir != "" {
			t.Errorf("%q resolved to project=%q dir=%q; a refusal must resolve nothing", pos, project, dir)
		}
	}
}

// A directory named like a project id is now reached the way every other
// directory is — by standing in it — so the shape that used to need a special
// case needs none. The test stays because the id-shaped refusal above must not
// grow into a rule about the working directory.
func TestAUUIDNamedDirectoryThatIsBoundStillResolves(t *testing.T) {
	parent := t.TempDir()
	t.Chdir(parent)
	if err := os.Mkdir(sampleProjectID, 0o755); err != nil {
		t.Fatal(err)
	}
	syncSeedState(t, sampleProjectID, syncer.State{ProjectID: "proj-A"})
	t.Chdir(sampleProjectID)

	project, dir, err := resolveSyncTarget("pull", nil, boundProject)
	if err != nil {
		t.Fatalf("a bound UUID-named directory was refused: %v", err)
	}
	if project != "proj-A" {
		t.Errorf("project = %q, want proj-A", project)
	}
	if dir != "." {
		t.Errorf("dir = %q, want \".\"", dir)
	}
}
