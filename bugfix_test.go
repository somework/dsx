package main

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/somework/dsx/internal/dsxerr"
)

// Defects the adversarial test pass turned up. Each is written red first, and
// each names what it would have cost, because that is the part a later reader
// needs in order to not undo it.

func TestPutIsOfferedByTheShells(t *testing.T) {
	// `put` was dispatched by run() and documented in usage, but missing from
	// commandNames, so no shell ever offered it. Two independent checks found
	// it: an AST walk of run()'s switch, and a word-set extraction per shell.
	// A substring test would not have: "conv-put" contains "put".
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

func TestHumanBytesSurvivesASizeItCannotName(t *testing.T) {
	// humanBytes indexed "KMGT" with an exponent it never clamped, so anything
	// at or past 1 PiB panicked. rep.Bytes flows through here on every summary
	// line: a sync that moved a petabyte would take the process down while
	// printing how well it had gone.
	for _, n := range []int64{1 << 50, 1 << 60, 1<<63 - 1} {
		got := humanBytes(n) // must not panic
		if got == "" {
			t.Errorf("humanBytes(%d) = %q", n, got)
		}
	}
	// The units it does have must keep their old spelling exactly.
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{0, "0 B"}, {512, "512 B"}, {1024, "1.0 KB"},
		{1 << 20, "1.0 MB"}, {1 << 30, "1.0 GB"}, {1 << 40, "1.0 TB"},
	} {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAuthHonoursDSXTokenLikeEveryOtherCommand(t *testing.T) {
	// usage promises "DSX_TOKEN overrides the stored credential", and every
	// command honoured it except this one: cmdAuth went straight to the stored
	// credential. Someone running `dsx auth` to explain a 401 was shown the
	// metadata of a credential the next request would not use.
	t.Setenv("DSX_TOKEN", "sk-ant-oat01-OVERRIDE")
	// Point the credential lookup at a config dir with no login, so a fall
	// through to the store would fail loudly rather than read the real one.
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
	// run() loaded the token before it reached the dispatch switch, so a typo on
	// a machine with no login was reported as an auth failure: exit 5, "no
	// Claude Code login found". dsx blamed the user's credentials for their
	// typo, and exit 5 invites a re-authentication that cannot possibly help.
	if isKnownCommand("pulll") {
		t.Fatal("`pulll` is not a command")
	}
	for _, name := range commandNames {
		if !isKnownCommand(name) {
			t.Errorf("%q is in commandNames but isKnownCommand says otherwise", name)
		}
	}
}

func TestRawRefusesANullArgumentInsteadOfSendingIt(t *testing.T) {
	// json.Unmarshal("null", &map) succeeds and leaves the map nil, so the
	// "arguments must be a JSON object" guard let `null` through and dsx sent
	// "arguments": null. Every other non-object was refused, so this was an
	// inconsistent boundary rather than a decision.
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		t.Errorf("the tool was called despite a malformed argument: %s %v", name, args)
		return fakeReply{Text: "{}"}
	})

	for _, bad := range []string{"null", "[1,2]", `"a string"`, "7"} {
		err := cmdRaw(t.Context(), f.client(), []string{"some_tool", bad})
		if err == nil {
			t.Errorf("raw accepted %s as arguments", bad)
			continue
		}
		if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
			t.Errorf("raw %s classified %q, want %q", bad, got, dsxerr.KindUsage)
		}
	}
}

func TestMalformedToolResultIsAProtocolErrorLikeItsSibling(t *testing.T) {
	// A body dsx cannot parse was dsxerr.KindProtocol; a *result* dsx cannot parse
	// degraded to the generic dsxerr.KindFailure. Both are "the server sent a shape we
	// do not model", and dsxerr.Kind is the stable token an agent matches on, so
	// the two must not disagree.
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{RawBody: `{"jsonrpc":"2.0","id":1,"result":"a bare string, not a tool result"}`}
	})

	_, err := f.client().callTool(t.Context(), "list_files", map[string]any{})
	if err == nil {
		t.Fatal("a malformed tool result was accepted")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindProtocol {
		t.Errorf("malformed tool result classified %q, want %q", got, dsxerr.KindProtocol)
	}
}

func TestTruncateNeverEmitsAHalfRune(t *testing.T) {
	// truncate cuts server text for display. Slicing by byte can land inside a
	// multi-byte rune, and the endpoint's own error prose is full of them
	// (it uses — and …). The cut must stay on a rune boundary.
	s := strings.Repeat("é", 50) // 100 bytes, 50 runes
	for n := 1; n < 100; n++ {
		got := truncate(s, n)
		if !utf8.ValidString(got) {
			t.Fatalf("truncate(%d) produced invalid UTF-8: %q", n, got)
		}
	}
	if got := truncate("abc", 10); got != "abc" {
		t.Errorf("truncate must leave a short string alone, got %q", got)
	}
	if got := truncate("abc", 3); got != "abc" {
		t.Errorf("truncate at exactly n must not cut, got %q", got)
	}
}

func TestPushReportsAPartialWriteInsteadOfUnderCountingIt(t *testing.T) {
	// writeBatch only complained when the etag map was entirely empty. A reply
	// naming a subset left the missing paths out of both the ledger and the
	// report, with no error.
	//
	// That is not merely a miscount. A file on the server with no ledger entry
	// is untracked, so the next pull sees bytes it has no record of and calls
	// its own work a conflict — which pushes the user toward --force, the exact
	// spiral invariant 5 exists to prevent.
	dir := t.TempDir()
	mkfile(t, dir, "a.css", "a")
	mkfile(t, dir, "b.css", "b")

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor()}
		case "write_files":
			// The server took both and acknowledged one.
			return fakeReply{Text: `{"etags":{"a.css":"e1"},"written":2,"url":"https://x"}`}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	_, err := runPush(t.Context(), f.client(), pushOpts{projectID: "p1", dir: dir, concurrency: 2})
	if err == nil {
		t.Fatal("a partial write reply was accepted silently")
	}
	if !strings.Contains(err.Error(), "b.css") {
		t.Errorf("the error must name the path the server did not acknowledge: %v", err)
	}
	if !strings.Contains(err.Error(), "pull") {
		t.Errorf("the error must say what to do next: %v", err)
	}

	// Whatever WAS acknowledged still has to reach the ledger: invariant 5.
	st, loadErr := loadState(dir)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if st.Files["a.css"].Etag != "e1" {
		t.Errorf("the acknowledged write was not recorded: %+v", st.Files)
	}
}

func TestCommandsRefuseArgumentsTheyDoNotTake(t *testing.T) {
	// `dsx prompt <project>` — the spelling every other command uses — silently
	// returned the generic prompt, because parseArgs discarded the positional.
	// A plausible wrong answer with exit 0 is worse than an error: the caller
	// never learns it asked the wrong question.
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		t.Errorf("the tool was called with an argument the command does not take: %s %v", name, args)
		return fakeReply{Text: "{}"}
	})

	cases := []struct {
		name string
		run  func() error
	}{
		{"prompt", func() error { return cmdPrompt(t.Context(), f.client(), []string{"bbbbbbbb-bbbb"}) }},
		{"tools", func() error { return cmdTools(t.Context(), f.client(), []string{"list_files"}) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatalf("dsx %s accepted a positional it does not take", tc.name)
			}
			if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
				t.Errorf("classified %q, want %q so the caller does not retry it", got, dsxerr.KindUsage)
			}
		})
	}
}
