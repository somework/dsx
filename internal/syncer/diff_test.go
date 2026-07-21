package syncer

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// TestDiffIsCorrectWithNoBaseline: with no baseline.json at all, Diff must
// still classify every path correctly — same, local-only, remote-only, and
// differs (with the actual sizes) — paying for it with a download of every
// present-both path rather than relying on a proof it does not have.
func TestDiffIsCorrectWithNoBaseline(t *testing.T) {
	dir := t.TempDir()
	sameBody := []byte("identical bytes\n")
	localBody := []byte("local only\n")
	remoteBody := []byte("remote only\n")
	localDiffers := []byte("local version\n")
	remoteDiffers := []byte("remote version, longer\n")

	mkfile(t, dir, "same.css", string(sameBody))
	mkfile(t, dir, "local.css", string(localBody))
	mkfile(t, dir, "differs.css", string(localDiffers))

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(
				fileEntry("same.css", "e1", int64(len(sameBody))),
				fileEntry("remote.css", "e2", int64(len(remoteBody))),
				fileEntry("differs.css", "e3", int64(len(remoteDiffers))),
			)}
		}
		p, _ := args["path"].(string)
		switch p {
		case "same.css":
			return fakeReply{Text: envelopeFor(p, "e1", string(sameBody))}
		case "remote.css":
			return fakeReply{Text: envelopeFor(p, "e2", string(remoteBody))}
		case "differs.css":
			return fakeReply{Text: envelopeFor(p, "e3", string(remoteDiffers))}
		}
		return fakeReply{Text: "unexpected path " + p, IsError: true}
	})
	c := fakeClient(f)

	rep, err := Diff(context.Background(), c, DiffOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 2})
	if err != nil {
		t.Fatalf("Diff errored: %v", err)
	}

	if !slices.Equal(rep.Same, []string{"same.css"}) {
		t.Errorf("Same = %v, want [same.css]", rep.Same)
	}
	if !slices.Equal(rep.LocalOnly, []string{"local.css"}) {
		t.Errorf("LocalOnly = %v, want [local.css]", rep.LocalOnly)
	}
	if !slices.Equal(rep.RemoteOnly, []string{"remote.css"}) {
		t.Errorf("RemoteOnly = %v, want [remote.css]", rep.RemoteOnly)
	}
	if len(rep.Differs) != 1 {
		t.Fatalf("Differs = %v, want exactly one entry", rep.Differs)
	}
	d := rep.Differs[0]
	if d.Path != "differs.css" {
		t.Errorf("Differs[0].Path = %q, want differs.css", d.Path)
	}
	if d.LocalSize != int64(len(localDiffers)) {
		t.Errorf("Differs[0].LocalSize = %d, want %d", d.LocalSize, len(localDiffers))
	}
	if d.RemoteSize != int64(len(remoteDiffers)) {
		t.Errorf("Differs[0].RemoteSize = %d, want %d", d.RemoteSize, len(remoteDiffers))
	}

	// No baseline.json was written or read — fetch writes, diff reads.
	if _, err := os.Stat(BaselinePath(dir)); !os.IsNotExist(err) {
		t.Errorf("baseline.json exists after a run with no prior fetch: stat err = %v", err)
	}
}

// TestDiffSkipsDownloadForProvenPaths is the measured payoff of `fetch`: a
// path proven by a fresh baseline entry costs Diff zero read_file calls,
// where the identical path costs one without a baseline. The count must
// actually fall, not merely "not rise" — a stub returning zero reads
// unconditionally would pass a same-only assertion.
func TestDiffSkipsDownloadForProvenPaths(t *testing.T) {
	body := []byte("shared bytes, unchanged since the last fetch\n")

	setup := func(t *testing.T) (dir string, f *fakeMCP) {
		dir = t.TempDir()
		mkfile(t, dir, "shared.css", string(body))
		f = newFakeMCP(t, func(name string, args map[string]any) fakeReply {
			if name == "list_files" {
				return fakeReply{Text: listingFor(fileEntry("shared.css", "e1", int64(len(body))))}
			}
			p, _ := args["path"].(string)
			return fakeReply{Text: envelopeFor(p, "e1", string(body))}
		})
		return dir, f
	}

	t.Run("without a baseline", func(t *testing.T) {
		dir, f := setup(t)
		c := fakeClient(f)
		rep, err := Diff(context.Background(), c, DiffOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 1})
		if err != nil {
			t.Fatalf("Diff errored: %v", err)
		}
		if !slices.Equal(rep.Same, []string{"shared.css"}) {
			t.Errorf("Same = %v, want [shared.css]", rep.Same)
		}
		if got := f.CountTool("read_file"); got != 1 {
			t.Errorf("read_file called %d times, want 1 (no baseline to prove it)", got)
		}
	})

	t.Run("with a fresh baseline", func(t *testing.T) {
		dir, f := setup(t)
		bl := Baseline{ProjectID: "proj-A", Verified: map[string]BaselineEntry{
			"shared.css": {Etag: "e1", Size: int64(len(body)), SHA: SHA256Hex(body)},
		}}
		c := fakeClient(f)
		bl.Endpoint = c.Endpoint()
		if err := bl.save(dir); err != nil {
			t.Fatal(err)
		}

		rep, err := Diff(context.Background(), c, DiffOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 1})
		if err != nil {
			t.Fatalf("Diff errored: %v", err)
		}
		if !slices.Equal(rep.Same, []string{"shared.css"}) {
			t.Errorf("Same = %v, want [shared.css]", rep.Same)
		}
		if got := f.CountTool("read_file"); got != 0 {
			t.Errorf("read_file called %d times, want 0 — the baseline proves it with no download", got)
		}
	})
}

// TestDiffMaterialisesOnlyTheRemoteSideOfDifferingPaths: --out writes exactly
// the differing paths' remote bytes, through WriteAtomic, and touches nothing
// else — not the same path, not the remote-only path (Diff never downloaded
// its bytes to begin with).
func TestDiffMaterialisesOnlyTheRemoteSideOfDifferingPaths(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(t.TempDir(), "out")
	sameBody := []byte("identical\n")
	localVersion := []byte("mine\n")
	remoteVersion := []byte("theirs, and longer\n")
	remoteOnlyBody := []byte("only on the server\n")

	mkfile(t, dir, "same.css", string(sameBody))
	mkfile(t, dir, "differs.css", string(localVersion))

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(
				fileEntry("same.css", "e1", int64(len(sameBody))),
				fileEntry("differs.css", "e2", int64(len(remoteVersion))),
				fileEntry("remote.css", "e3", int64(len(remoteOnlyBody))),
			)}
		}
		p, _ := args["path"].(string)
		switch p {
		case "same.css":
			return fakeReply{Text: envelopeFor(p, "e1", string(sameBody))}
		case "differs.css":
			return fakeReply{Text: envelopeFor(p, "e2", string(remoteVersion))}
		case "remote.css":
			t.Fatalf("read_file called for remote.css, which Diff never needs to download to classify remote-only")
		}
		return fakeReply{Text: "unexpected path " + p, IsError: true}
	})
	c := fakeClient(f)

	rep, err := Diff(context.Background(), c, DiffOpts{ProjectID: "proj-A", Dir: dir, Out: out, Concurrency: 2})
	if err != nil {
		t.Fatalf("Diff errored: %v", err)
	}
	if len(rep.Differs) != 1 || rep.Differs[0].Path != "differs.css" {
		t.Fatalf("Differs = %v, want exactly [differs.css]", rep.Differs)
	}

	got, err := os.ReadFile(filepath.Join(out, "differs.css"))
	if err != nil {
		t.Fatalf("out/differs.css was not written: %v", err)
	}
	if string(got) != string(remoteVersion) {
		t.Errorf("out/differs.css = %q, want the remote bytes %q", got, remoteVersion)
	}

	for _, rel := range []string{"same.css", "remote.css"} {
		if _, err := os.Stat(filepath.Join(out, rel)); !os.IsNotExist(err) {
			t.Errorf("%s was materialised into --out, want only differing paths written (stat err = %v)", rel, err)
		}
	}
}

// TestDiffRefusesAForeignProject and TestDiffRefusesAForeignEndpoint: the
// (project, endpoint) binding (invariant 13) is checked before the round
// trip, same as Fetch and Pin.
func TestDiffRefusesAForeignProject(t *testing.T) {
	dir := t.TempDir()
	seeded := State{ProjectID: "proj-A", Files: map[string]FileState{}}
	if err := seeded.save(dir); err != nil {
		t.Fatal(err)
	}

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})
	_, err := Diff(context.Background(), fakeClient(f), DiffOpts{ProjectID: "proj-B", Dir: dir})
	if err == nil {
		t.Fatal("Diff accepted a directory bound to a different project")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
	if got := f.CountTool("list_files"); got != 0 {
		t.Errorf("list_files called %d times, want 0", got)
	}
}

func TestDiffRefusesAForeignEndpoint(t *testing.T) {
	dir := t.TempDir()
	seeded := State{ProjectID: "proj-A", Endpoint: "https://elsewhere.example/mcp", Files: map[string]FileState{}}
	if err := seeded.save(dir); err != nil {
		t.Fatal(err)
	}

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})
	_, err := Diff(context.Background(), fakeClient(f), DiffOpts{ProjectID: "proj-A", Dir: dir})
	if err == nil {
		t.Fatal("Diff accepted a directory bound to a different endpoint")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
	if !strings.Contains(err.Error(), "refusing to diff") {
		t.Errorf("error text = %q, want it to name diff (%q)", err.Error(), "refusing to diff")
	}
	if got := f.CountTool("list_files"); got != 0 {
		t.Errorf("list_files called %d times, want 0", got)
	}
}

// TestDiffWritesNoEntryOnALengthMismatch: invariant 1 extends to Diff the
// same way it extends to Fetch — a decoded length disagreeing with
// list_files' size must fail the run rather than classify from corrupt bytes.
func TestDiffWritesNoEntryOnALengthMismatch(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "bad.css", "whatever local bytes\n")

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(fileEntry("bad.css", "eBad", 999))}
		}
		p, _ := args["path"].(string)
		return fakeReply{Text: envelopeFor(p, "eBad", "short")}
	})
	c := fakeClient(f)

	_, err := Diff(context.Background(), c, DiffOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 1})
	if err == nil {
		t.Fatal("Diff succeeded despite a decoded length disagreeing with the listing's size")
	}
	if !strings.Contains(err.Error(), "refusing to classify") {
		t.Errorf("err = %v, want it to mention the length mismatch", err)
	}
}

// TestDiffInterruptedReturnsAnErrorAndMaterialisesNothing: invariant 3 — an
// interrupted run is a failure, not a partial classification, and --out must
// not have received a fragment of it.
func TestDiffInterruptedReturnsAnErrorAndMaterialisesNothing(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(t.TempDir(), "out")
	mkfile(t, dir, "a.css", "local a\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor(fileEntry("a.css", "e1", 8))}
	})
	c := fakeClient(f)

	_, err := Diff(ctx, c, DiffOpts{ProjectID: "proj-A", Dir: dir, Out: out, Concurrency: 1})
	if err == nil {
		t.Fatal("Diff succeeded against an already-cancelled context")
	}
	if entries, rErr := os.ReadDir(out); rErr == nil && len(entries) != 0 {
		t.Errorf("--out was materialised despite the interrupted run: %v", entries)
	}
}

// TestDiffReportRendersOneLinePerPathInTextMode covers the non-JSON Render
// branch: one line per path, all four classifications represented, "differs"
// carrying both sizes.
func TestDiffReportRendersOneLinePerPathInTextMode(t *testing.T) {
	rep := DiffReport{
		Same:       []string{"same.css"},
		LocalOnly:  []string{"local.css"},
		RemoteOnly: []string{"remote.css"},
		Differs:    []DiffPair{{Path: "differs.css", LocalSize: 3, RemoteSize: 30}},
	}
	got := rep.Render(false)
	for _, want := range []string{
		"same.css: same",
		"local.css: local-only",
		"remote.css: remote-only",
		"differs.css: differs (local 3 B, remote 30 B)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Render output missing %q; got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "same.css: same, ") {
		t.Errorf("Render printed a hunk-shaped line, not a classification: %s", got)
	}

	empty := DiffReport{}.Render(false)
	if empty == "" {
		t.Error("Render of an empty report printed nothing")
	}

	incomplete := DiffReport{Incomplete: true}.Render(false)
	if !strings.HasPrefix(incomplete, "incomplete") {
		t.Errorf("Render did not mark an incomplete report: %q", incomplete)
	}
}
