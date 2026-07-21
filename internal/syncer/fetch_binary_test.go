package syncer

import (
	"context"
	"slices"
	"testing"
)

// TestFetchRecordsNoEntryForABinaryPath: a binary path is never on disk as
// the server's bytes (read_file refuses it), so a baseline entry for one is
// a claim dsx cannot back — see Baseline's doc comment. Only reachable
// before this test via -tags=live, which the default gate never runs; the
// mocked-fake framework already simulates a binary refusal for Pull
// (binary_write_lane_test.go), so the same shape closes this gap here.
func TestFetchRecordsNoEntryForABinaryPath(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "logo.png", "not really png bytes")
	textBody := []byte("hello\n")
	mkfile(t, dir, "readme.md", string(textBody))

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(
				fileEntry("logo.png", "ePng", 4096),
				fileEntry("readme.md", "eTxt", int64(len(textBody))))}
		}
		p, _ := args["path"].(string)
		switch p {
		case "logo.png":
			return fakeReply{Text: p + " is a binary file and cannot be read", IsError: true}
		case "readme.md":
			return fakeReply{Text: envelopeFor(p, "eTxt", string(textBody))}
		}
		return fakeReply{Text: "unexpected path " + p, IsError: true}
	})
	c := fakeClient(f)

	rep, err := Fetch(context.Background(), c, FetchOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 2})
	if err != nil {
		t.Fatalf("Fetch errored on a binary refusal: %v", err)
	}

	if !slices.Contains(rep.Skipped, "logo.png") {
		t.Errorf("Skipped = %v, want logo.png present", rep.Skipped)
	}
	if slices.Contains(rep.Fetched, "logo.png") {
		t.Errorf("Fetched = %v, a binary refusal must never be recorded", rep.Fetched)
	}

	// Positive control: the sibling text path must be fully baselined, so
	// the assertions above are not passing because Fetch broke entirely.
	if !slices.Contains(rep.Fetched, "readme.md") {
		t.Errorf("Fetched = %v, want readme.md present", rep.Fetched)
	}

	bl, err := loadBaseline(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := bl.Verified["logo.png"]; ok {
		t.Errorf("baseline holds an entry for a binary refusal: %+v", bl.Verified["logo.png"])
	}
	if _, ok := bl.Verified["readme.md"]; !ok {
		t.Errorf("baseline missing readme.md")
	}
}
