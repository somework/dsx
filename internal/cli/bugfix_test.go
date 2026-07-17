package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/syncer"
)

func TestPutIsOfferedByTheShells(t *testing.T) {
	found := false
	for _, n := range commandNames {
		if n == "put" {
			found = true
		}
	}
	if !found {
		t.Fatal("`put` is dispatched but not in commandNames; it is invisible to every shell")
	}
}

func TestAuthHonoursDSXTokenLikeEveryOtherCommand(t *testing.T) {
	t.Setenv("DSX_TOKEN", "sk-ant-oat01-OVERRIDE")

	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	out, err := captureStdout(t, func() error { return cmdAuth(nil) })
	if err != nil {
		t.Fatalf("cmdAuth with DSX_TOKEN set: %v", err)
	}
	if strings.Contains(out, "OVERRIDE") {
		t.Fatalf("cmdAuth printed the token: %q", out)
	}
	if !strings.Contains(out, "DSX_TOKEN") {
		t.Errorf("cmdAuth did not say the token came from DSX_TOKEN: %q", out)
	}
}

func TestAuthJSONStaysParseableWhenTheTokenComesFromTheEnvironment(t *testing.T) {
	t.Setenv("DSX_TOKEN", "sk-ant-oat01-OVERRIDE")
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	out, err := captureStdout(t, func() error { return cmdAuth([]string{"--json"}) })
	if err != nil {
		t.Fatalf("cmdAuth --json: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("--json output is not JSON: %v\n%s", err, out)
	}
	if strings.Contains(out, "OVERRIDE") {
		t.Fatalf("cmdAuth --json printed the token: %q", out)
	}
	if got["source"] != "DSX_TOKEN" {
		t.Errorf("source = %v, want DSX_TOKEN", got["source"])
	}
}

func TestUnknownCommandIsAUsageErrorEvenWithNoLoginAvailable(t *testing.T) {
	if _, ok := commandIndex["pulll"]; ok {
		t.Fatal("`pulll` is not a command")
	}
	for _, name := range commandNames {
		if _, ok := commandIndex[name]; !ok {
			t.Errorf("%q is in commandNames but commandIndex does not resolve it", name)
		}
	}
}

func TestMalformedToolResultIsAProtocolErrorLikeItsSibling(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{RawBody: `{"jsonrpc":"2.0","id":1,"result":"a bare string, not a tool result"}`}
	})

	_, err := fakeClient(f).CallTool(t.Context(), "list_files", map[string]any{})
	if err == nil {
		t.Fatal("a malformed tool result was accepted")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindProtocol {
		t.Errorf("malformed tool result classified %q, want %q", got, dsxerr.KindProtocol)
	}
}

func TestPushReportsAPartialWriteInsteadOfUnderCountingIt(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "a.css", "a")
	mkfile(t, dir, "b.css", "b")

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor()}
		case "write_files":
			return fakeReply{Text: `{"etags":{"a.css":"e1"},"written":2,"url":"https://x"}`}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	_, err := syncer.Push(t.Context(), fakeClient(f), syncer.PushOpts{ProjectID: "p1", Dir: dir, Concurrency: 2})
	if err == nil {
		t.Fatal("a partial write reply was accepted silently")
	}
	if !strings.Contains(err.Error(), "b.css") {
		t.Errorf("the error must name the path the server did not acknowledge: %v", err)
	}
	if !strings.Contains(err.Error(), "pull") {
		t.Errorf("the error must say what to do next: %v", err)
	}

	st, loadErr := syncer.LoadState(dir)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if st.Files["a.css"].Etag != "e1" {
		t.Errorf("the acknowledged write was not recorded: %+v", st.Files)
	}
}
