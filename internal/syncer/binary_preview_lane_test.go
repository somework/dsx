package syncer

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// pngBytes is content read_file refuses: not valid UTF-8, which is the whole
// of what the server means by "binary".
var pngBytes = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0xff, 0xfe, 0x00, 0x01}

// previewLaneFake answers list_files with one binary path, read_file with the
// server's real refusal, and render_preview with a URL its own preview lane
// serves.
func previewLaneFake(t *testing.T, path, etag string, body []byte) *fakeMCP {
	t.Helper()
	var f *fakeMCP
	f = newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry(path, etag, int64(len(body))))}
		case "read_file":
			return fakeReply{
				Text:    `read_file: "` + path + `" is a binary file (stored base64); read_file only returns text content`,
				IsError: true,
			}
		case "render_preview":
			return fakeReply{Text: f.PreviewReply(args["path"].(string))}
		}
		return fakeReply{Text: "[]"}
	})
	f.PutServe(path, body)
	return f
}

func seedTrackedBinary(t *testing.T, dir, path, etag string) {
	t.Helper()
	st := State{ProjectID: "p1", Files: map[string]FileState{
		path: {Etag: etag, Binary: true},
	}}
	if err := st.save(dir); err != nil {
		t.Fatal(err)
	}
}

// Without the flag nothing changes: the lane is opt-in, and the report still
// says the path was skipped.
func TestPlainPullStillSkipsABinaryAndNeverAsksThePreviewHost(t *testing.T) {
	dir := t.TempDir()
	seedTrackedBinary(t, dir, "og.png", "e1")
	f := previewLaneFake(t, "og.png", "e2", pngBytes)

	rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4,
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !slices.Contains(rep.Binary, "og.png") {
		t.Fatalf("a plain pull no longer reports the skip: %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(dir, "og.png")); err == nil {
		t.Fatal("a plain pull wrote a binary the caller never asked for")
	}
	if n := f.CountTool("render_preview"); n != 0 {
		t.Fatalf("a plain pull minted %d preview URL(s); the lane is opt-in", n)
	}
	if got := len(f.PreviewGets()); got != 0 {
		t.Fatalf("a plain pull made %d preview request(s)", got)
	}
}

func TestPullBinaryWritesTheFileAndClearsTheMarker(t *testing.T) {
	dir := t.TempDir()
	seedTrackedBinary(t, dir, "og.png", "e1")
	f := previewLaneFake(t, "og.png", "e1", pngBytes)

	rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4, Binary: true,
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !slices.Contains(rep.Fetched, "og.png") {
		t.Fatalf("the path is not reported as fetched: %+v", rep)
	}
	if len(rep.Binary) != 0 {
		t.Fatalf("--binary still reports the path as skipped: %+v", rep.Binary)
	}

	got, err := os.ReadFile(filepath.Join(dir, "og.png"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(pngBytes) {
		t.Fatalf("wrote % x, want % x", got, pngBytes)
	}

	st, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	entry := st.Files["og.png"]
	if entry.SHA != SHA256Hex(pngBytes) {
		t.Fatalf("the ledger recorded no sha of the bytes it wrote: %+v", entry)
	}
	if entry.Etag != "e1" {
		t.Fatalf("the ledger recorded etag %q, want the listing's", entry.Etag)
	}
	// The marker means "dsx has never read this path's bytes back from the
	// server". It has now, so every refusal keyed off it must stop firing.
	if entry.Binary {
		t.Fatalf("the Binary marker survived a successful preview fetch: %+v", entry)
	}
}

// The one case the user actually hits first: the files are already on disk,
// downloaded by hand. Nothing is rewritten — a rename swaps the inode, breaking
// hard links and destroying xattrs (invariant 15's accepted cost), and there is
// no reason to pay it for bytes that already match.
func TestPullBinaryAdoptsAnIdenticalLocalCopyWithoutRewritingIt(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "og.png")
	if err := os.WriteFile(full, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	seedTrackedBinary(t, dir, "og.png", "e1")
	f := previewLaneFake(t, "og.png", "e1", pngBytes)

	rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4, Binary: true,
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !slices.Contains(rep.Adopted, "og.png") {
		t.Fatalf("an identical local copy was not adopted: %+v", rep)
	}
	if len(rep.Fetched) != 0 {
		t.Fatalf("Fetched names an act — nothing was written: %+v", rep.Fetched)
	}
	if len(rep.Conflicts) != 0 {
		t.Fatalf("an identical copy was called a conflict: %+v", rep.Conflicts)
	}

	after, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("the file was rewritten though its bytes already matched (mtime %v -> %v)",
			before.ModTime(), after.ModTime())
	}
	if after.Mode() != before.Mode() {
		t.Fatalf("mode changed %v -> %v", before.Mode(), after.Mode())
	}

	st, _ := LoadState(dir)
	e := st.Files["og.png"]
	if e.SHA != SHA256Hex(pngBytes) || e.Size != int64(len(pngBytes)) || e.Etag != "e1" {
		t.Fatalf("the adopted path was not recorded: %+v", e)
	}
	// The marker STAYS on an adopted path. dsx did not write these bytes — the
	// user did, and they match by coincidence of content. Both prune loops read
	// Binary:true as "not ours", which is exactly what invariant 17 requires
	// here; the sha beside it is what stops push calling it a conflict.
	if !e.Binary {
		t.Fatalf("adoption cleared the marker, so a plain --prune can now delete "+
			"a file dsx never wrote: %+v", e)
	}
}

// Invariant 17's named failure, both halves, on the adoption path: once
// `tracked` stops meaning "dsx put these bytes here" and starts meaning "these
// bytes match", a plain --prune deletes the only copy in either direction.
func TestAnAdoptedPathStaysOutOfBothPruneLoops(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "og.png")
	if err := os.WriteFile(full, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	seedTrackedBinary(t, dir, "og.png", "e1")
	f := previewLaneFake(t, "og.png", "e1", pngBytes)

	if _, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4, Binary: true,
	}); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	st := mustLoad(t, dir)
	if st.Files["og.png"].SHA == "" {
		t.Fatal("the fixture did not adopt, so neither half below proves anything")
	}

	// Push half: the user deletes their own copy. A plain `push --prune` must
	// not answer that by deleting the server's.
	onServer := map[string]RemoteEntry{
		"og.png": {Path: "og.png", Type: "file", Etag: "e1", Size: int64(len(pngBytes))},
	}
	pd := planPush(onServer, map[string]localFile{}, st, nil, nil, forceNone, true)
	if slices.Contains(pd.Delete, "og.png") {
		t.Errorf("a plain `push --prune` deletes the server's only copy of a file dsx never wrote")
	}

	// Pull half: a teammate deletes it server-side. A plain `pull --prune`
	// must not answer that by deleting the user's.
	onDisk := map[string]localFile{
		"og.png": {Path: "og.png", SHA: SHA256Hex(pngBytes), Size: int64(len(pngBytes))},
	}
	ld := planPull(map[string]RemoteEntry{}, onDisk, st, nil, false, true, false)
	if slices.Contains(ld.Delete, "og.png") {
		t.Errorf("a plain `pull --prune` deletes the user's only copy of a file dsx never wrote")
	}
	if !slices.Contains(ld.PruneBinary, "og.png") {
		t.Errorf("it was kept but not surfaced: %+v", ld)
	}
}

// A path routed by the binary-lane arm jumps over every conflict arm, so the
// compensating check must fire for the ROUTE. Keyed on the download instead, a
// server copy that has since become valid UTF-8 reads back through read_file
// with the check switched off, and a modified local file is overwritten with
// no --force and no conflict.
func TestABinaryThatBecameTextStillCannotOverwriteAModifiedLocalFile(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "og.png")
	mine := []byte("my own unsaved work\n")
	if err := os.WriteFile(full, mine, 0o600); err != nil {
		t.Fatal(err)
	}
	seedTrackedBinary(t, dir, "og.png", "e1")

	// read_file SUCCEEDS now: the server's copy is valid UTF-8, which
	// PROTOCOL.md records as served whatever the extension says.
	nowText := "server text now\n"
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("og.png", "e1", int64(len(nowText))))}
		case "read_file":
			return fakeReply{Text: envelopeFor("og.png", "e1", nowText)}
		}
		return fakeReply{Text: "[]"}
	})

	rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4, Binary: true,
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	got, _ := os.ReadFile(full)
	if string(got) != string(mine) {
		t.Fatalf("--binary overwrote a modified local file with no --force: %q", got)
	}
	if !slices.Contains(rep.Conflicts, "og.png") {
		t.Fatalf("no conflict was reported for the overwrite it refused: %+v", rep)
	}
	if rep.Outcome() == nil {
		t.Fatal("Outcome is nil -> exit 0 on a refused overwrite")
	}
}

// The other side of the same arm: identical bytes are adopted whichever lane
// served them, and the marker stays for the same reason.
func TestABinaryThatBecameTextIsAdoptedWhenItMatches(t *testing.T) {
	dir := t.TempDir()
	body := "server text now\n"
	full := filepath.Join(dir, "og.png")
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	seedTrackedBinary(t, dir, "og.png", "e1")

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("og.png", "e1", int64(len(body))))}
		case "read_file":
			return fakeReply{Text: envelopeFor("og.png", "e1", body)}
		}
		return fakeReply{Text: "[]"}
	})

	rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4, Binary: true,
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !slices.Contains(rep.Adopted, "og.png") {
		t.Fatalf("an identical copy was not adopted: %+v", rep)
	}
	if e := mustLoad(t, dir).Files["og.png"]; e.SHA == "" || !e.Binary {
		t.Fatalf("the adopted entry is wrong: %+v", e)
	}
}

// A second --binary run must not re-download what the first one recorded.
func TestASecondBinaryPullReDownloadsNothing(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "og.png")
	if err := os.WriteFile(full, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	seedTrackedBinary(t, dir, "og.png", "e1")
	f := previewLaneFake(t, "og.png", "e1", pngBytes)

	for range 2 {
		if _, err := Pull(context.Background(), fakeClient(f), PullOpts{
			ProjectID: "p1", Dir: dir, Concurrency: 4, Binary: true,
		}); err != nil {
			t.Fatalf("Pull: %v", err)
		}
	}
	if n := f.CountTool("render_preview"); n != 1 {
		t.Fatalf("the preview lane ran %d times across two runs; the second had nothing to fetch", n)
	}
}

func TestPullBinaryRefusesToOverwriteALocalCopyThatDiffers(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "og.png")
	mine := []byte{0x89, 'P', 'N', 'G', 0xff, 0x00, 'm', 'i', 'n', 'e', 0x01, 0x02}
	if err := os.WriteFile(full, mine, 0o600); err != nil {
		t.Fatal(err)
	}
	seedTrackedBinary(t, dir, "og.png", "e1")
	f := previewLaneFake(t, "og.png", "e1", pngBytes)

	rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4, Binary: true,
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	got, _ := os.ReadFile(full)
	if string(got) != string(mine) {
		t.Fatal("--binary overwrote a local copy that differs, with no --force")
	}
	if !slices.Contains(rep.BinaryDiverged, "og.png") {
		t.Fatalf("the divergence is not reported: %+v", rep)
	}
	if !slices.Contains(rep.Conflicts, "og.png") {
		t.Fatalf("a divergence that is not in Conflicts carries no exit code: %+v", rep)
	}
	out := rep.Outcome()
	if out == nil {
		t.Fatal("Outcome is nil -> exit 0 while a local file disagrees with the server")
	}
	if code := dsxerr.ExitCodeFor(out); code != dsxerr.KindConflict.ExitCode() {
		t.Fatalf("exit code %d, want %d", code, dsxerr.KindConflict.ExitCode())
	}
	// The wording must not send the reader to `dsx fetch`: fetch cannot record
	// a baseline for a tracked path, so it would be advice that never works.
	if strings.Contains(out.Error(), "dsx fetch") {
		t.Fatalf("the hint names `dsx fetch` for a tracked path: %v", out)
	}
	// Nothing was proven about these bytes, so nothing may be recorded.
	st, _ := LoadState(dir)
	if e := st.Files["og.png"]; e.SHA != "" || !e.Binary {
		t.Fatalf("a refused path was recorded anyway: %+v", e)
	}
}

func TestPullBinaryUnderForceOverwritesALocalCopyThatDiffers(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "og.png")
	if err := os.WriteFile(full, []byte("something else entirely"), 0o600); err != nil {
		t.Fatal(err)
	}
	seedTrackedBinary(t, dir, "og.png", "e1")
	f := previewLaneFake(t, "og.png", "e1", pngBytes)

	rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4, Binary: true, Force: true,
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	got, _ := os.ReadFile(full)
	if string(got) != string(pngBytes) {
		t.Fatalf("--force did not overwrite: % x", got)
	}
	if len(rep.Conflicts) != 0 {
		t.Fatalf("--force still reported a conflict: %+v", rep.Conflicts)
	}
}

// The preview lane serves whatever a path holds NOW, and hands back no etag
// comparable with list_files'. The second listing is the only thing that ties
// the bytes to the revision the run planned against.
func TestPullBinaryRecordsNothingWhenTheListingMovedMidDownload(t *testing.T) {
	dir := t.TempDir()
	seedTrackedBinary(t, dir, "og.png", "e1")

	listings := 0
	var f *fakeMCP
	f = newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			listings++
			// The second listing — the post-download one — shows a path
			// somebody rewrote while dsx was reading it.
			etag := "e1"
			if listings > 1 {
				etag = "e2"
			}
			return fakeReply{Text: listingFor(fileEntry("og.png", etag, int64(len(pngBytes))))}
		case "read_file":
			return fakeReply{Text: `read_file: "og.png" is a binary file (stored base64)`, IsError: true}
		case "render_preview":
			return fakeReply{Text: f.PreviewReply(args["path"].(string))}
		}
		return fakeReply{Text: "[]"}
	})
	f.PutServe("og.png", pngBytes)

	rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4, Binary: true,
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !slices.Contains(rep.Raced, "og.png") {
		t.Fatalf("the race is not reported: %+v", rep)
	}
	// The bytes did land — Fetched names that act and must keep naming it.
	if !slices.Contains(rep.Fetched, "og.png") {
		t.Fatalf("Fetched dropped a path whose bytes were written: %+v", rep)
	}
	st, _ := LoadState(dir)
	e := st.Files["og.png"]
	if e.SHA != "" {
		t.Fatalf("a ledger entry pairs the planned etag with bytes from another revision: %+v", e)
	}
	if e.Etag != "e1" || !e.Binary {
		t.Fatalf("the ledger entry was not left as it was, so the next run cannot simply retry: %+v", e)
	}
	if !strings.Contains(rep.Render(false), "again") {
		t.Fatalf("the rendered line does not tell the reader to run it again:\n%s", rep.Render(false))
	}
}

// A length disagreeing with list_files is a wrong decode, and this lane has a
// specific way to produce one: the server prepends a ~16 KiB preview harness to
// an .html served through it.
func TestPullBinaryRefusesBytesWhoseLengthDisagreesWithTheListing(t *testing.T) {
	dir := t.TempDir()
	seedTrackedBinary(t, dir, "og.png", "e1")

	var f *fakeMCP
	f = newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("og.png", "e1", int64(len(pngBytes))))}
		case "read_file":
			return fakeReply{Text: `read_file: "og.png" is a binary file (stored base64)`, IsError: true}
		case "render_preview":
			return fakeReply{Text: f.PreviewReply(args["path"].(string))}
		}
		return fakeReply{Text: "[]"}
	})
	f.PutServe("og.png", pngBytes[:len(pngBytes)-1])

	_, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4, Binary: true,
	})
	if err == nil {
		t.Fatal("a short body passed the size assertion")
	}
	if _, sErr := os.Stat(filepath.Join(dir, "og.png")); sErr == nil {
		t.Fatal("the file was written despite the size disagreement")
	}
}

// --binary is the caller asking for exactly these bytes. Not getting them is
// the same kind of event as read_file failing, not a quieter skip.
func TestPullBinaryFailsRatherThanDegradingToASkip(t *testing.T) {
	dir := t.TempDir()
	seedTrackedBinary(t, dir, "og.png", "e1")

	var f *fakeMCP
	f = newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("og.png", "e1", int64(len(pngBytes))))}
		case "read_file":
			return fakeReply{Text: `read_file: "og.png" is a binary file (stored base64)`, IsError: true}
		case "render_preview":
			return fakeReply{Text: f.PreviewReply(args["path"].(string))}
		}
		return fakeReply{Text: "[]"}
	})
	// Nothing registered for the path: the preview host answers 404.

	rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4, Binary: true,
	})
	if err == nil {
		t.Fatalf("a preview-lane failure passed as success: %+v", rep)
	}
	if slices.Contains(rep.Binary, "og.png") {
		t.Fatal("a failed --binary fetch was reported as an ordinary skip")
	}
}

// The end the whole change exists for: a `push` that called the path a binary
// conflict must stop, with no --force anywhere.
func TestOnceThePreviewLaneHasRunPushStopsCallingItABinaryConflict(t *testing.T) {
	dir := t.TempDir()
	seedTrackedBinary(t, dir, "og.png", "e1")
	f := previewLaneFake(t, "og.png", "e1", pngBytes)

	before := planPush(
		map[string]RemoteEntry{"og.png": {Path: "og.png", Type: "file", Etag: "e1", Size: int64(len(pngBytes))}},
		map[string]localFile{"og.png": {Path: "og.png", SHA: SHA256Hex(pngBytes), Size: int64(len(pngBytes))}},
		mustLoad(t, dir), nil, nil, forceNone, false)
	if !slices.Contains(before.BinaryConflicts, "og.png") {
		t.Fatalf("the fixture does not reproduce the conflict this test exists to clear: %+v", before)
	}

	if _, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4, Binary: true,
	}); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	after := planPush(
		map[string]RemoteEntry{"og.png": {Path: "og.png", Type: "file", Etag: "e1", Size: int64(len(pngBytes))}},
		map[string]localFile{"og.png": {Path: "og.png", SHA: SHA256Hex(pngBytes), Size: int64(len(pngBytes))}},
		mustLoad(t, dir), nil, nil, forceNone, false)
	if len(after.BinaryConflicts) != 0 {
		t.Fatalf("push still calls it a binary conflict after the bytes were verified: %+v", after.BinaryConflicts)
	}
	if after.Unchanged != 1 {
		t.Fatalf("push does not see the path as unchanged: %+v", after)
	}
}

func mustLoad(t *testing.T, dir string) State {
	t.Helper()
	st, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// The OAuth token is ambient input, never handed to a second host (invariant 8).
// mcp has the unit-level guard; this one holds the wiring the sync engine
// actually uses.
func TestTheSyncEngineNeverSendsTheTokenToThePreviewHost(t *testing.T) {
	dir := t.TempDir()
	seedTrackedBinary(t, dir, "og.png", "e1")
	f := previewLaneFake(t, "og.png", "e1", pngBytes)

	if _, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4, Binary: true,
	}); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	gets := f.PreviewGets()
	if len(gets) == 0 {
		t.Fatal("no preview request was made, so this guard checked nothing")
	}
	for _, g := range gets {
		if g.Authorization != "" {
			t.Fatalf("the preview GET carried Authorization: %q", g.Authorization)
		}
	}
}

// A run that wrote bytes and recorded nothing for them has not done what
// --binary asked. Under -q the exit code is the only channel that can say so,
// so a raced path must carry one — the same reasoning that put PruneBinary,
// which is also not a choice between two versions, into Conflicts.
func TestARacedPathCarriesANonZeroExitAndNamesTheRetry(t *testing.T) {
	dir := t.TempDir()
	seedTrackedBinary(t, dir, "og.png", "e1")

	listings := 0
	var f *fakeMCP
	f = newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			listings++
			etag := "e1"
			if listings > 1 {
				etag = "e2"
			}
			return fakeReply{Text: listingFor(fileEntry("og.png", etag, int64(len(pngBytes))))}
		case "read_file":
			return fakeReply{Text: `read_file: "og.png" is a binary file (stored base64)`, IsError: true}
		case "render_preview":
			return fakeReply{Text: f.PreviewReply(args["path"].(string))}
		}
		return fakeReply{Text: "[]"}
	})
	f.PutServe("og.png", pngBytes)

	rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4, Binary: true,
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !slices.Contains(rep.Conflicts, "og.png") {
		t.Fatalf("a raced path is not in Conflicts, so nothing carries an exit code: %+v", rep)
	}
	out := rep.Outcome()
	if out == nil {
		t.Fatal("Outcome is nil -> exit 0 while a file sits on disk with no ledger entry")
	}
	if code := dsxerr.ExitCodeFor(out); code != dsxerr.KindConflict.ExitCode() {
		t.Fatalf("exit code %d, want %d", code, dsxerr.KindConflict.ExitCode())
	}
	// The remedy is to run it again, never --force: nothing here is a
	// disagreement to overrule.
	if !strings.Contains(out.Error(), "dsx pull --binary") {
		t.Errorf("the hint does not name the retry: %v", out)
	}
	if strings.Contains(out.Error(), "--force") {
		t.Errorf("the hint offers --force for a path nobody disagrees about: %v", out)
	}
}

// Another path failing says nothing about the binaries that already came back.
// Skipping their ledger commit leaves rep.Fetched calling a path fetched while
// the ledger still carries the pre-run marker for it.
func TestABinaryLedgerEntrySurvivesAnUnrelatedFailure(t *testing.T) {
	dir := t.TempDir()
	st := State{ProjectID: "p1", Files: map[string]FileState{
		"a.png": {Etag: "e1", Binary: true},
	}}
	if err := st.save(dir); err != nil {
		t.Fatal(err)
	}

	// z.css must not fail before a.png's bytes have arrived, or the cancel
	// races the download and the test is about scheduling rather than about
	// the gate. The handshake is the serve hook: it closes served after
	// writing a.png's body, and everything a.png does afterwards — writeAtomic
	// and the record under the mutex — consults no context.
	served := make(chan struct{})
	var f *fakeMCP
	f = newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(
				fileEntry("a.png", "e1", int64(len(pngBytes))),
				fileEntry("z.css", "e9", 5),
			)}
		case "read_file":
			if args["path"] == "a.png" {
				return fakeReply{Text: `read_file: "a.png" is a binary file (stored base64)`, IsError: true}
			}
			<-served
			return fakeReply{Text: "read_file: z.css exploded", IsError: true}
		case "render_preview":
			return fakeReply{Text: f.PreviewReply(args["path"].(string))}
		}
		return fakeReply{Text: "[]"}
	})
	f.PutServe("a.png", pngBytes)
	f.ServeHook(func(_ string, w http.ResponseWriter) bool {
		_, _ = w.Write(pngBytes)
		close(served)
		return true
	})

	_, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4, Binary: true,
	})
	if err == nil {
		t.Fatal("the unrelated failure did not surface")
	}

	after, lErr := LoadState(dir)
	if lErr != nil {
		t.Fatal(lErr)
	}
	e := after.Files["a.png"]
	if e.SHA != SHA256Hex(pngBytes) || e.Binary {
		t.Fatalf("a binary written to disk kept its pre-run marker because another path failed: %+v", e)
	}
}

// When the confirming listing cannot be had at all, the run's error says
// nothing about WHICH paths landed unattributed. Every one of them is named.
func TestAConfirmingListingThatFailsNamesEveryDownloadedPath(t *testing.T) {
	dir := t.TempDir()
	seedTrackedBinary(t, dir, "og.png", "e1")

	listings := 0
	var f *fakeMCP
	f = newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			listings++
			if listings > 1 {
				return fakeReply{Text: "list_files: down", IsError: true}
			}
			return fakeReply{Text: listingFor(fileEntry("og.png", "e1", int64(len(pngBytes))))}
		case "read_file":
			return fakeReply{Text: `read_file: "og.png" is a binary file (stored base64)`, IsError: true}
		case "render_preview":
			return fakeReply{Text: f.PreviewReply(args["path"].(string))}
		}
		return fakeReply{Text: "[]"}
	})
	f.PutServe("og.png", pngBytes)

	rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
		ProjectID: "p1", Dir: dir, Concurrency: 4, Binary: true,
	})
	if err == nil {
		t.Fatal("a failed confirming listing passed as success")
	}
	if !slices.Contains(rep.Raced, "og.png") {
		t.Fatalf("the report does not name the path it could not attribute: %+v", rep)
	}
	st, _ := LoadState(dir)
	if e := st.Files["og.png"]; e.SHA != "" {
		t.Fatalf("an unattributable path was recorded anyway: %+v", e)
	}
}
