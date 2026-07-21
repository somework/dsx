package syncer

import (
	"context"
	"testing"
)

// TestFetchThenPushTreatsAProvenPathAsVerified and its Pull sibling below are
// the only tests in the package that produce a Baseline through Fetch itself
// and then feed it to Push/Pull through one fake MCP server, rather than
// hand-writing a Baseline{ProjectID, Endpoint, Verified} literal (as
// baseline_wiring_test.go and first_contact_test.go do). A hand-written
// literal cannot catch a Fetch that forgets to record one of the binding's
// two halves — see corrupt_baseline_test.go for the ProjectID half; this
// covers Endpoint, which nothing else exercises end to end.
func TestFetchThenPushTreatsAProvenPathAsVerified(t *testing.T) {
	dir := t.TempDir()
	body := []byte("shared bytes\n")
	mkfile(t, dir, "readme.md", string(body))

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(fileEntry("readme.md", "e1", int64(len(body))))}
		}
		p, _ := args["path"].(string)
		if p == "readme.md" {
			return fakeReply{Text: envelopeFor(p, "e1", string(body))}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})
	c := fakeClient(f)

	if _, err := Fetch(context.Background(), c, FetchOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 2}); err != nil {
		t.Fatalf("Fetch errored: %v", err)
	}

	rep, err := Push(context.Background(), c, PushOpts{ProjectID: "proj-A", Dir: dir})
	if err != nil {
		t.Fatalf("Push errored: %v", err)
	}
	if rep.Verified != 1 {
		t.Errorf("Verified = %d, want 1 — a Fetch-produced baseline must prove the byte match", rep.Verified)
	}
	if len(rep.Conflicts) != 0 {
		t.Errorf("Conflicts = %v, want none", rep.Conflicts)
	}
	if got := f.CountTool("write_files"); got != 0 {
		t.Errorf("write_files called %d times, want 0: a proven path must never be written", got)
	}
}

func TestFetchThenPullTreatsAProvenPathAsVerified(t *testing.T) {
	dir := t.TempDir()
	body := []byte("shared bytes\n")
	mkfile(t, dir, "readme.md", string(body))

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(fileEntry("readme.md", "e1", int64(len(body))))}
		}
		p, _ := args["path"].(string)
		if p == "readme.md" {
			return fakeReply{Text: envelopeFor(p, "e1", string(body))}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})
	c := fakeClient(f)

	if _, err := Fetch(context.Background(), c, FetchOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 2}); err != nil {
		t.Fatalf("Fetch errored: %v", err)
	}

	rep, err := Pull(context.Background(), c, PullOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 2})
	if err != nil {
		t.Fatalf("Pull errored: %v", err)
	}
	if rep.Verified != 1 {
		t.Errorf("Verified = %d, want 1 — a Fetch-produced baseline must prove the byte match", rep.Verified)
	}
	if len(rep.Conflicts) != 0 {
		t.Errorf("Conflicts = %v, want none", rep.Conflicts)
	}
	if len(rep.Fetched) != 0 {
		t.Errorf("Fetched = %v, want none: a proven path must not be re-downloaded", rep.Fetched)
	}
	// One read_file call total, from Fetch itself — Pull must not repeat it.
	if got := f.CountTool("read_file"); got != 1 {
		t.Errorf("read_file called %d times, want 1 (Fetch only)", got)
	}
}
