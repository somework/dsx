package cli

import (
	"strings"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// A flag typed before the verb used to reach printNounHelp, which reads only
// args[0] and drops everything after it. `dsx member --json rm p uuid` printed
// the verb list and exited 0: nobody was removed, stderr was empty, and the
// JSON carried no error key. A caller branching on the exit code and on
// dsxerr's token — which is what dsx tells agents to do — reads that as done.
// The root level already refuses the same mistake (`dsx --json pull` is a
// usage error), so the noun branch was strictly the weaker of the two.
func TestAFlagBeforeTheVerbIsRefusedNotSwallowed(t *testing.T) {
	t.Setenv("DSX_TOKEN", "")
	diagPinCredentialStore(t)

	for _, g := range groups {
		if g.Noun == "" {
			continue
		}
		for _, verb := range nounVerbs(g) {
			t.Run(g.Noun+" "+verb, func(t *testing.T) {
				out, err := maincliRun(t, g.Noun, "--json", verb, "p1")
				if err == nil {
					t.Fatalf("dsx %s --json %s p1 succeeded, printing %q — the verb was dropped",
						g.Noun, verb, out)
				}
				if got := maincliKind(t, err); got != dsxerr.KindUsage {
					t.Errorf("kind = %v, want %v", got, dsxerr.KindUsage)
				}
				// Invariant 18: the refusal names a form that parses.
				want := "dsx " + g.Noun + " " + verb
				if msg := err.Error(); !strings.Contains(msg, want) {
					t.Errorf("refusal = %q, want it to name %q", msg, want)
				}
			})
		}
	}
}

// The suggestion is the caller's own line with the verb moved to the front,
// not a bare form: dsx accepts flags before, between and after positionals
// once a verb's FlagSet is reached, so every argument they typed survives.
func TestTheMisplacedFlagRefusalRebuildsTheWholeLine(t *testing.T) {
	t.Setenv("DSX_TOKEN", "")
	diagPinCredentialStore(t)

	_, err := maincliRun(t, "files", "--json", "cat", "p1", "a.css")
	if err == nil {
		t.Fatal("dsx files --json cat p1 a.css succeeded")
	}
	if want := "dsx files cat --json p1 a.css"; !strings.Contains(err.Error(), want) {
		t.Errorf("refusal = %q, want it to name %q", err.Error(), want)
	}
}

// The dash branch still answers the question it was written for. A flag with
// no verb behind it is a question about the noun.
func TestAFlagWithNoVerbBehindItStillAnswersWithTheNounHelp(t *testing.T) {
	t.Setenv("DSX_TOKEN", "")
	diagPinCredentialStore(t)

	for _, argv := range [][]string{
		{"files"},
		{"files", "-h"},
		{"files", "--json"},
	} {
		out, err := maincliRun(t, argv...)
		if err != nil {
			t.Fatalf("dsx %s: %v", strings.Join(argv, " "), err)
		}
		if !strings.Contains(out, "files tree") {
			t.Errorf("dsx %s printed %q, want the verb list", strings.Join(argv, " "), out)
		}
	}
}

// Only a verb this noun declares counts. A positional that merely looks like
// another noun's verb is the command's business, not dispatch's — otherwise
// `dsx files cat p1 new` would be rewritten as a plan.
func TestOnlyThisNounsOwnVerbsCountAsMisplaced(t *testing.T) {
	t.Setenv("DSX_TOKEN", "")
	diagPinCredentialStore(t)

	// `sharing` is a project verb, not a files verb.
	out, err := maincliRun(t, "files", "--json", "sharing")
	if err != nil {
		t.Fatalf("dsx files --json sharing: %v", err)
	}
	if !strings.Contains(out, "files tree") {
		t.Errorf("printed %q, want the files verb list", out)
	}
}
