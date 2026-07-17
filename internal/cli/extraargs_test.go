package cli

import (
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// A stray trailing positional must fail fast with a usage error, exactly as
// `dsx pull p d extra` already does via resolveSyncTarget. Before the fix every
// command below silently ignored the extra arg and exited 0.
func TestTrailingExtraArgIsRejectedByEveryCommandForm(t *testing.T) {
	t.Setenv("DSX_TOKEN", "sk-ant-oat01-EXTRAARGS")
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: "{}"}
	})
	client := fakeClient(f)

	// conv-put needs --messages pointing at a real JSON array, and member-add
	// needs --role plus --email/--uuid; supplied below so the only thing wrong
	// with each invocation is the trailing extra arg, not a missing flag.
	dir := t.TempDir()
	mkfile(t, dir, "messages.json", "[]")
	msgs := dir + "/messages.json"

	cases := []struct {
		name string
		args []string
	}{
		// zero-positional commands
		{"auth", []string{"extra"}},
		{"help", []string{"extra"}},
		{"doctor", []string{"extra"}},
		{"projects", []string{"extra"}},
		{"systems", []string{"extra"}},
		{"tools", []string{"extra"}},
		{"prompt", []string{"extra"}},
		// one-positional commands
		{"completion", []string{"bash", "extra"}},
		{"project", []string{"id", "extra"}},
		{"new", []string{"name", "extra"}},
		{"tree", []string{"proj", "extra"}},
		{"conv", []string{"proj", "extra"}},
		{"conv-put", []string{"proj", "--messages", msgs, "extra"}},
		{"members", []string{"proj", "extra"}},
		{"member-add", []string{"proj", "--role", "editor", "--email", "x@y.z", "extra"}},
		{"sharing", []string{"proj", "extra"}},
		{"plan", []string{"proj", "extra"}},
		{"support-js", []string{"proj", "extra"}},
		// ls: <project> [path] — third positional is extra
		{"ls", []string{"proj", "path", "extra"}},
		// two-positional commands
		{"cat", []string{"proj", "path", "extra"}},
		{"preview", []string{"proj", "path", "extra"}},
		{"member-rm", []string{"proj", "uuid", "extra"}},
		// put: <project> <path> [file] — fourth positional is extra
		{"put", []string{"proj", "path", "file", "extra"}},
		// raw: <tool> '<json-args>' — third positional is extra
		{"raw", []string{"sometool", "{}", "extra"}},
		// three-positional commands
		{"member-role", []string{"proj", "uuid", "editor", "extra"}},
		{"cp", []string{"proj", "src", "dst", "extra"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, ok := commandIndex[tc.name]
			if !ok {
				t.Fatalf("%q is not a registered command", tc.name)
			}
			var err error
			_, _ = captureStdout(t, func() error {
				err = c.Dispatch(t.Context(), client, tc.args)
				return nil
			})
			if err == nil {
				t.Fatalf("`dsx %s %v` was accepted; a stray trailing arg must be a usage error", tc.name, tc.args)
			}
			if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
				t.Fatalf("`dsx %s %v` returned kind %q, want %q: %v", tc.name, tc.args, got, dsxerr.KindUsage, err)
			}
		})
	}
}
