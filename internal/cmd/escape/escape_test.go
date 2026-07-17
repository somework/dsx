package escape

import (
	"testing"

	"github.com/somework/dsx/internal/clitest"
	"github.com/somework/dsx/internal/dsxerr"
)

type fakeReply = clitest.Reply

var (
	newFakeMCP = clitest.New
	fakeClient = clitest.Client
)

func TestRawRefusesANullArgumentInsteadOfSendingIt(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		t.Errorf("the tool was called despite a malformed argument: %s %v", name, args)
		return fakeReply{Text: "{}"}
	})

	for _, bad := range []string{"null", "[1,2]", `"a string"`, "7"} {
		err := cmdRaw(t.Context(), fakeClient(f), []string{"some_tool", bad})
		if err == nil {
			t.Errorf("raw accepted %s as arguments", bad)
			continue
		}
		if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
			t.Errorf("raw %s classified %q, want %q", bad, got, dsxerr.KindUsage)
		}
	}
}

func TestCommandsRefuseArgumentsTheyDoNotTake(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		t.Errorf("the tool was called with an argument the command does not take: %s %v", name, args)
		return fakeReply{Text: "{}"}
	})

	cases := []struct {
		name string
		run  func() error
	}{
		{"prompt", func() error { return cmdPrompt(t.Context(), fakeClient(f), []string{"bbbbbbbb-bbbb"}) }},
		{"tools", func() error { return cmdTools(t.Context(), fakeClient(f), []string{"list_files"}) }},
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
