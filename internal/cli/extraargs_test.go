package cli

import (
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// A stray trailing positional must fail fast with a usage error, exactly as
// `dsx pull p d extra` already does via resolveSyncTarget. Before fix #10 every
// command silently ignored the extra arg and exited 0.
//
// The guard used to be a hand-written list of command forms. A literal list has
// a hole invariant 11 warns about: a NEW command added to a group without its
// own cmd.NoExtra call regresses silently, because the list does not know the
// new command exists. So the coverage set is asserted against the live command
// registry (commandIndex / commandNames) instead — every registered command
// must be reachable through either baseInvocations or variadicCommands, and a
// gap FAILS naming the uncovered command (mirrors invariant 11's
// TestEveryDeclaredGroupIsRegistered, which guards group registration the same
// structural way).

// variadicCommands names every command whose grammar accepts an unbounded tail
// of positionals, so a trailing arg is legitimate input rather than a mistake
// and cannot be exercised by the "rejects an extra arg" assertion. Membership
// is a deliberate opt-out: leaving a command out of BOTH this set and
// baseInvocations makes TestEveryRegisteredCommandIsCoveredByTheExtraArgGuard
// fail, so adding a new variadic command is a conscious choice, never a silent
// gap.
var variadicCommands = map[string]string{
	"rm": "rm <project> <path...> — each extra positional is another path to delete, not a usage error",
}

// baseInvocations gives, for every non-variadic command, a VALID invocation
// (required flags and minimal positionals) whose only defect once a trailing
// positional is appended is that extra arg. conv-put needs --messages pointing
// at a real JSON array and member-add needs --role plus --email, supplied here
// so the surfaced error is the stray arg, not a missing flag.
func baseInvocations(t *testing.T) map[string][]string {
	t.Helper()
	dir := t.TempDir()
	mkfile(t, dir, "messages.json", "[]")
	msgs := dir + "/messages.json"

	return map[string][]string{
		// SYNC — resolveSyncTarget rejects a 3rd positional before any network;
		// clone's own NoExtra rejects it before its target checks.
		"clone":  {"proj", "dir"},
		"pull":   {"proj", "dir"},
		"push":   {"proj", "dir"},
		"status": {"proj", "dir"},
		"fetch":  {"proj", "dir"},
		"pin":    {"proj", "dir"},
		"diff":   {"proj", "dir"},
		// PROJECTS
		"projects": {},
		"project":  {"id"},
		"new":      {"name"},
		"systems":  {},
		// FILES (rm is variadic — see variadicCommands)
		"ls":   {"proj", "path"}, // ls <project> [path] — 3rd is extra
		"tree": {"proj"},
		"cat":  {"proj", "path"},
		"put":  {"proj", "path", "file"}, // put <project> <path> [file] — 4th is extra
		"cp":   {"proj", "src", "dst"},
		// PLANS
		"plan":       {"proj"},
		"preview":    {"proj", "path"},
		"support-js": {"proj"},
		// CONVERSATION
		"conv":     {"proj"},
		"conv-put": {"proj", "--messages", msgs},
		// MEMBERS
		"members":     {"proj"},
		"member-add":  {"proj", "--role", "editor", "--email", "x@y.z"},
		"member-rm":   {"proj", "uuid"},
		"member-role": {"proj", "uuid", "editor"},
		"sharing":     {"proj"},
		// ESCAPE HATCH
		"prompt": {},
		"tools":  {},
		"raw":    {"sometool", "{}"}, // raw <tool> '<json-args>' — 3rd is extra
		// DIAGNOSTICS
		"help":       {},
		"auth":       {},
		"doctor":     {},
		"version":    {},
		"completion": {"bash"},
	}
}

// TestEveryRegisteredCommandIsCoveredByTheExtraArgGuard is the anti-rot half:
// it walks the live registry and fails naming any command that is neither
// fixtured nor explicitly marked variadic. Because it reads commandIndex, a
// command added to any group later is caught here even though this test never
// mentions it by name. It also refuses stale fixtures/exemptions that name a
// command the registry no longer has, and refuses a command listed in both.
func TestEveryRegisteredCommandIsCoveredByTheExtraArgGuard(t *testing.T) {
	t.Parallel()
	fixtures := baseInvocations(t)

	covered := map[string]bool{}
	for name := range commandIndex {
		// commandIndex keys include aliases (-h, --help, …); the canonical
		// name carries the coverage assertion, so skip alias entries.
		if commandIndex[name].Name != name {
			continue
		}
		covered[name] = true

		_, fixtured := fixtures[name]
		_, variadic := variadicCommands[name]
		switch {
		case fixtured && variadic:
			t.Errorf("%q is listed in BOTH baseInvocations and variadicCommands; it must be exactly one", name)
		case !fixtured && !variadic:
			t.Errorf("%q is a registered command but has no extra-arg coverage: add a valid base "+
				"invocation to baseInvocations, or, if it takes an unbounded positional tail, list it "+
				"in variadicCommands with a reason", name)
		}
	}

	for name := range fixtures {
		if !covered[name] {
			t.Errorf("baseInvocations names %q, which is not a registered command — stale fixture", name)
		}
	}
	for name := range variadicCommands {
		if !covered[name] {
			t.Errorf("variadicCommands names %q, which is not a registered command — stale exemption", name)
		}
	}
}

// TestEveryFixturedCommandRejectsAStrayTrailingArg is the behavioural half:
// for each valid base invocation, append one trailing positional and assert the
// command fails fast with a KindUsage (exit-2) error rather than accepting it.
func TestEveryFixturedCommandRejectsAStrayTrailingArg(t *testing.T) {
	t.Setenv("DSX_TOKEN", "sk-ant-oat01-EXTRAARGS")
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: "{}"}
	})
	client := fakeClient(f)

	for name, base := range baseInvocations(t) {
		t.Run(name, func(t *testing.T) {
			c, ok := commandIndex[name]
			if !ok {
				t.Fatalf("%q is not a registered command", name)
			}
			args := append(append([]string(nil), base...), "extra")

			var err error
			_, _ = captureStdout(t, func() error {
				err = c.Dispatch(t.Context(), client, args)
				return nil
			})
			if err == nil {
				t.Fatalf("`dsx %s %v` was accepted; a stray trailing arg must be a usage error", name, args)
			}
			if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
				t.Fatalf("`dsx %s %v` returned kind %q, want %q: %v", name, args, got, dsxerr.KindUsage, err)
			}
		})
	}
}
