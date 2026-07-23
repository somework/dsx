package synccmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/clitest"
	"github.com/somework/dsx/internal/syncer"
)

var lanePNG = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0xff, 0xfe}

// laneFake answers list_files with one binary path, read_file with the server's
// refusal, and render_preview with a URL its own preview lane serves.
func laneFake(t *testing.T) *fakeMCP {
	t.Helper()
	var f *fakeMCP
	f = newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("og.png", "e1", int64(len(lanePNG))))}
		case "read_file":
			return fakeReply{Text: `read_file: "og.png" is a binary file (stored base64)`, IsError: true}
		case "render_preview":
			return fakeReply{Text: f.PreviewReply(args["path"].(string))}
		}
		return fakeReply{Text: "[]"}
	})
	f.PutServe("og.png", lanePNG)
	return f
}

func laneTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	clitest.SeedState(t, dir, syncer.State{
		ProjectID: "p1",
		Files:     map[string]syncer.FileState{"og.png": {Etag: "e1", Binary: true}},
	})
	t.Chdir(dir)
	return dir
}

// The flag has to reach PullOpts, and nothing below the command layer can say
// whether it did.
func TestPullBinaryFlagReachesTheLane(t *testing.T) {
	dir := laneTree(t)
	f := laneFake(t)

	out, err := captureStdout(t, func() error {
		return cmdPull(context.Background(), fakeClient(f), []string{"--binary"})
	})
	if err != nil {
		t.Fatalf("pull --binary: %v\n%s", err, out)
	}
	got, rErr := os.ReadFile(filepath.Join(dir, "og.png"))
	if rErr != nil {
		t.Fatalf("pull --binary wrote nothing: %v\n%s", rErr, out)
	}
	if string(got) != string(lanePNG) {
		t.Fatalf("wrote % x, want % x", got, lanePNG)
	}
	if !strings.Contains(out, "pulled 1") {
		t.Errorf("the summary does not report the fetch: %q", out)
	}
}

func TestPullWithoutTheFlagNeverTouchesThePreviewHost(t *testing.T) {
	dir := laneTree(t)
	f := laneFake(t)

	out, err := captureStdout(t, func() error {
		return cmdPull(context.Background(), fakeClient(f), nil)
	})
	if err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	if _, sErr := os.Stat(filepath.Join(dir, "og.png")); sErr == nil {
		t.Fatal("a plain pull wrote a binary")
	}
	if n := f.CountTool("render_preview"); n != 0 {
		t.Fatalf("a plain pull minted %d preview URL(s)", n)
	}
	// And the skip line names the flag that would have fetched it, or the
	// reader has no way to learn the lane exists.
	if !strings.Contains(out, "--binary") {
		t.Errorf("the skip line does not name --binary: %q", out)
	}
}

// The preview URL is a credential (invariant 8's line: it is a capability the
// server minted, but no verb asked for it and nothing downstream needs it). It
// must not reach stdout, an error, or any file dsx writes. Checked on the
// actual output and the actual bytes on disk, not by reading the source.
func TestThePreviewURLReachesNoOutputAndNoFileOnDisk(t *testing.T) {
	dir := laneTree(t)
	f := laneFake(t)

	// Everything about the URL that must never appear: the token, the query
	// that carries it, and the whole string itself.
	secrets := []string{"fake-preview-token", "?t=", f.ServeURL("og.png")}

	out, err := captureStdout(t, func() error {
		return cmdPull(context.Background(), fakeClient(f), []string{"--binary"})
	})
	if err != nil {
		t.Fatalf("pull --binary: %v", err)
	}
	// --json too: it is a separate rendering of the same report.
	outJSON, err := captureStdout(t, func() error {
		return cmdPull(context.Background(), fakeClient(f), []string{"--binary", "--json"})
	})
	if err != nil {
		t.Fatalf("pull --binary --json: %v", err)
	}

	haystacks := map[string]string{"stdout": out, "stdout --json": outJSON}
	for _, rel := range []string{".dsx/state.json", ".dsx/baseline.json"} {
		if b, rErr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel))); rErr == nil {
			haystacks[rel] = string(b)
		}
	}
	if _, ok := haystacks[".dsx/state.json"]; !ok {
		t.Fatal("no ledger was written, so the file half of this guard checked nothing")
	}
	for where, hay := range haystacks {
		for _, secret := range secrets {
			if strings.Contains(hay, secret) {
				t.Errorf("%s carries %q of the preview URL:\n%s", where, secret, hay)
			}
		}
	}
}

// The refusal path separately, in its own tree: it is the one a hostile reply
// takes, and it is where a URL is most tempting to print. A second fake in the
// tree above would never have got this far — the endpoint guard refuses a
// ledger synced against another host first, which is how this check was green
// for the wrong reason once already.
func TestTheRefusedPreviewHostIsNamedWithoutItsToken(t *testing.T) {
	laneTree(t)
	bad := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("og.png", "e1", int64(len(lanePNG))))}
		case "read_file":
			return fakeReply{Text: `read_file: "og.png" is a binary file (stored base64)`, IsError: true}
		case "render_preview":
			return fakeReply{Text: `{"serve_url":"https://evil.test/serve/og.png?t=fake-preview-token"}`}
		}
		return fakeReply{Text: "[]"}
	})

	out, refusal := captureStdout(t, func() error {
		return cmdPull(context.Background(), fakeClient(bad), []string{"--binary"})
	})
	if refusal == nil {
		t.Fatal("dsx fetched from a host render_preview should not have named")
	}
	if !strings.Contains(refusal.Error(), "evil.test") {
		t.Fatalf("the refusal does not name the host it refused: %v", refusal)
	}
	for where, hay := range map[string]string{"the refusal": refusal.Error(), "stdout": out} {
		for _, secret := range []string{"fake-preview-token", "?t="} {
			if strings.Contains(hay, secret) {
				t.Errorf("%s carries %q of the preview URL: %s", where, secret, hay)
			}
		}
	}
}

// A dry run previews the lane and spends no preview request: -n asks what a
// run would do, and asking the preview host is already doing it.
func TestPullBinaryDryRunFetchesNothing(t *testing.T) {
	dir := laneTree(t)
	f := laneFake(t)

	if _, err := captureStdout(t, func() error {
		return cmdPull(context.Background(), fakeClient(f), []string{"--binary", "-n"})
	}); err != nil {
		t.Fatalf("pull --binary -n: %v", err)
	}
	if _, sErr := os.Stat(filepath.Join(dir, "og.png")); sErr == nil {
		t.Fatal("a dry run wrote a file")
	}
	if n := f.CountTool("render_preview"); n != 0 {
		t.Fatalf("a dry run minted %d preview URL(s)", n)
	}
}
