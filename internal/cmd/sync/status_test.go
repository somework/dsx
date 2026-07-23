package synccmd

import (
	"context"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
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
	for _, want := range []string{"untracked locally:", "scratch.md", "as of the last fetch or pull:", "theirs.css"} {
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

// TestPushRefusesForceAndLeaseTogether: the two ask for different writes —
// one sends no precondition, the other sends the etag the last fetch
// recorded. Ranking them silently would keep one of the two things the
// caller asked for and drop the other without saying which.
func TestPushRefusesForceAndLeaseTogether(t *testing.T) {
	_, c := maincliFake(t, "unreachable")
	dir := t.TempDir()

	err := cmdPush(context.Background(), c, append(syncIn(t, dir, "proj-A"), "--force", "--force-with-lease"))
	if err == nil {
		t.Fatal("push accepted --force and --force-with-lease together")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind = %v, want %v", got, dsxerr.KindUsage)
	}
	if !strings.Contains(err.Error(), "name one") {
		t.Errorf("the refusal is not the mutual-exclusion one: %v", err)
	}
	// Positive control by wording, not by kind: --force-with-lease alone is
	// also refused here (nothing has been fetched, so there is no snapshot to
	// lease against) and that refusal is KindUsage too. Only the text tells
	// the two apart, so only the text can prove this assertion is not passing
	// on a push that rejects everything.
	for _, flag := range []string{"--force", "--force-with-lease"} {
		err := cmdPush(context.Background(), c, append(syncIn(t, dir, "proj-A"), flag))
		if err != nil && strings.Contains(err.Error(), "name one") {
			t.Errorf("%s alone was refused as a flag conflict: %v", flag, err)
		}
	}
}

// TestPushWiresLeaseThroughToTheEngine: syncer's own tests prove what a lease
// decides; only this proves the flag reaches it. Without a snapshot the
// engine refuses before the round trip, and that refusal is the observable
// end of the wire — a flag that never arrived would let push run instead.
func TestPushWiresLeaseThroughToTheEngine(t *testing.T) {
	dir := t.TempDir()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})
	c := fakeClient(f)

	err := cmdPush(context.Background(), c, append(syncIn(t, dir, "proj-A"), "--force-with-lease"))
	if err == nil {
		t.Fatal("push --force-with-lease ran with nothing to lease against")
	}
	if !strings.Contains(err.Error(), "lease against") {
		t.Errorf("the refusal is not the engine's lease refusal: %v", err)
	}
	// Positive control: the same push without the flag must get through, or
	// the assertion above would pass on any refusal at all.
	if _, pErr := captureStdout(t, func() error {
		return cmdPush(context.Background(), c, syncIn(t, dir, "proj-A"))
	}); pErr != nil {
		t.Errorf("a plain push failed, so the refusal above proves nothing: %v", pErr)
	}
}
