package syncer

import (
	"context"
	"slices"
	"strings"
	"testing"
)

var fetchPNG = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0xff, 0xfe, 0x00}

// fetchBinaryFake serves one binary and one text path. servePreview decides
// whether the preview host has bytes for the binary at all.
func fetchBinaryFake(t *testing.T, textBody []byte, servePreview bool) *fakeMCP {
	t.Helper()
	var f *fakeMCP
	f = newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(
				fileEntry("logo.png", "ePng", int64(len(fetchPNG))),
				fileEntry("readme.md", "eTxt", int64(len(textBody))))}
		case "render_preview":
			return fakeReply{Text: f.PreviewReply(args["path"].(string))}
		case "read_file":
			switch args["path"] {
			case "logo.png":
				return fakeReply{Text: `read_file: "logo.png" is a binary file (stored base64)`, IsError: true}
			case "readme.md":
				return fakeReply{Text: envelopeFor("readme.md", "eTxt", string(textBody))}
			}
		}
		return fakeReply{Text: "unexpected call", IsError: true}
	})
	if servePreview {
		f.PutServe("logo.png", fetchPNG)
	}
	return f
}

// This file used to assert the opposite, on a premise the preview lane
// retired: "a binary path is never on disk as the server's bytes, so a
// baseline entry for one is a claim dsx cannot back". dsx can back it now, and
// an untracked binary is exactly the path no ledger entry could ever speak for
// — the gap that left an asset stuck in `status`' untracked-differs with no
// command able to clear it.
func TestFetchRecordsAnUntrackedBinaryThroughThePreviewLane(t *testing.T) {
	dir := t.TempDir()
	textBody := []byte("hello\n")
	mkfile(t, dir, "logo.png", string(fetchPNG))
	mkfile(t, dir, "readme.md", string(textBody))

	f := fetchBinaryFake(t, textBody, true)
	rep, err := Fetch(context.Background(), fakeClient(f), FetchOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 2,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !slices.Contains(rep.Fetched, "logo.png") {
		t.Fatalf("the binary was not recorded: %+v", rep)
	}
	// Positive control: the sibling text path must still be baselined, or the
	// assertion above could be passing on a Fetch that lost its text lane.
	if !slices.Contains(rep.Fetched, "readme.md") {
		t.Fatalf("the text path was not recorded: %+v", rep)
	}
	if len(rep.Skipped) != 0 {
		t.Fatalf("something was skipped: %v", rep.Skipped)
	}

	bl, err := loadBaseline(dir)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := bl.Verified["logo.png"]
	if !ok {
		t.Fatal("the baseline holds no entry for the binary")
	}
	if e.SHA != SHA256Hex(fetchPNG) || e.Etag != "ePng" || e.Size != int64(len(fetchPNG)) {
		t.Fatalf("the recorded entry is wrong: %+v", e)
	}
}

// What survives of the old premise, narrower: a path the preview host will not
// serve still gets no entry. It must not take the rest of the run down with it
// — fetch records what it can about a whole tree, and nobody named this path.
func TestFetchSkipsABinaryThePreviewHostWillNotServe(t *testing.T) {
	dir := t.TempDir()
	textBody := []byte("hello\n")
	mkfile(t, dir, "logo.png", string(fetchPNG))
	mkfile(t, dir, "readme.md", string(textBody))

	f := fetchBinaryFake(t, textBody, false)
	rep, err := Fetch(context.Background(), fakeClient(f), FetchOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 2,
	})
	if err != nil {
		t.Fatalf("one unserveable asset failed the whole fetch: %v", err)
	}
	if !slices.Contains(rep.Skipped, "logo.png") {
		t.Fatalf("the unserveable path is not reported: %+v", rep)
	}
	if slices.Contains(rep.Fetched, "logo.png") {
		t.Fatalf("an unserveable path was recorded: %+v", rep)
	}
	if !slices.Contains(rep.Fetched, "readme.md") {
		t.Fatalf("the sibling text path was lost with it: %+v", rep)
	}
	// Named, not counted: a bare number leaves the reader unable to tell which
	// path `status` will keep calling untracked-differs.
	if !strings.Contains(rep.Render(false), "logo.png") {
		t.Fatalf("the rendered line does not name it:\n%s", rep.Render(false))
	}

	bl, _ := loadBaseline(dir)
	if _, ok := bl.Verified["logo.png"]; ok {
		t.Fatal("the baseline holds an entry for a path nothing served")
	}
}

// The preview lane hands back no etag comparable with list_files', so the pair
// fetch is about to record — the pre-download listing's etag beside the
// downloaded sha — is only true if nothing moved under it.
func TestFetchRecordsNoBinaryEntryWhenTheListingMovedMidDownload(t *testing.T) {
	dir := t.TempDir()
	textBody := []byte("hello\n")
	mkfile(t, dir, "logo.png", string(fetchPNG))
	mkfile(t, dir, "readme.md", string(textBody))

	listings := 0
	var f *fakeMCP
	f = newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			listings++
			etag := "ePng"
			if listings > 1 {
				etag = "ePng2"
			}
			return fakeReply{Text: listingFor(
				fileEntry("logo.png", etag, int64(len(fetchPNG))),
				fileEntry("readme.md", "eTxt", int64(len(textBody))))}
		case "render_preview":
			return fakeReply{Text: f.PreviewReply(args["path"].(string))}
		case "read_file":
			switch args["path"] {
			case "logo.png":
				return fakeReply{Text: `read_file: "logo.png" is a binary file (stored base64)`, IsError: true}
			case "readme.md":
				return fakeReply{Text: envelopeFor("readme.md", "eTxt", string(textBody))}
			}
		}
		return fakeReply{Text: "unexpected call", IsError: true}
	})
	f.PutServe("logo.png", fetchPNG)

	rep, err := Fetch(context.Background(), fakeClient(f), FetchOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 2,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !slices.Contains(rep.Skipped, "logo.png") {
		t.Fatalf("the raced path is not reported: %+v", rep)
	}
	bl, _ := loadBaseline(dir)
	if e, ok := bl.Verified["logo.png"]; ok {
		t.Fatalf("a pair the run cannot attribute was recorded anyway: %+v", e)
	}
	// The text path came back through read_file, whose etag describes the very
	// bytes it served, so no listing can unseat it.
	if _, ok := bl.Verified["readme.md"]; !ok {
		t.Fatal("the text path lost its entry to a race it was never part of")
	}
}

// The whole point, end to end: an untracked asset `status` could not speak for
// becomes one it can.
func TestAFetchedBinaryMakesStatusStopCallingItUnproven(t *testing.T) {
	dir := t.TempDir()
	textBody := []byte("hello\n")
	mkfile(t, dir, "logo.png", string(fetchPNG))
	mkfile(t, dir, "readme.md", string(textBody))

	// Control first: with no fetch behind it, status must have something to
	// say about the asset, or the assertion below proves nothing.
	before := fetchBinaryFake(t, textBody, false)
	if _, err := Fetch(context.Background(), fakeClient(before), FetchOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 2,
	}); err != nil {
		t.Fatal(err)
	}
	rep, err := Status(StatusOpts{Dir: dir, ProjectID: "proj-A"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(rep.UntrackedDiffers, "logo.png") {
		t.Fatalf("the control is wrong: an unproven asset is not in untracked-differs:\n%+v", rep)
	}

	after := fetchBinaryFake(t, textBody, true)
	if _, err := Fetch(context.Background(), fakeClient(after), FetchOpts{
		ProjectID: "proj-A", Dir: dir, Concurrency: 2,
	}); err != nil {
		t.Fatal(err)
	}
	rep, err = Status(StatusOpts{Dir: dir, ProjectID: "proj-A"})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(rep.UntrackedDiffers, "logo.png") {
		t.Fatalf("status still calls a proven asset untracked-differs:\n%+v", rep)
	}
	if !slices.Contains(rep.UntrackedSame, "logo.png") {
		t.Fatalf("the proof did not move it to untracked-matches:\n%+v", rep)
	}
	if !strings.Contains(rep.Render(false), "untracked, matches") {
		t.Fatalf("the rendered line does not say so:\n%s", rep.Render(false))
	}
}
