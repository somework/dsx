package cli

import (
	"regexp"
	"strings"
	"testing"
)

// The migration removed ~18 flat spellings and sent every one of them to the
// same refusal, which names only `dsx help`. The mechanism for a better one
// already existed and was already firing one token along: when the dead token
// happens to still be a noun, `dsx conv abc` answers with an unknown-conv-verb
// refusal that names `dsx conv -h`. So the generic bucket was not a decision
// about these tokens, only the place they fell through to. For `cat`,
// `sharing`, `support-js` and the rest there is no ambiguity at all: each is
// an exact and unique second-token match against one command.
func TestADeadFlatFormNamesItsReplacement(t *testing.T) {
	t.Setenv("DSX_TOKEN", "")
	diagPinCredentialStore(t)

	for _, tc := range []struct {
		typed string
		want  []string
	}{
		{"cat", []string{"dsx files cat"}},
		{"tree", []string{"dsx files tree"}},
		{"preview", []string{"dsx files preview"}},
		{"sharing", []string{"dsx project sharing"}},
		{"support-js", []string{"dsx project support-js"}},
		{"role", []string{"dsx member role"}},
		// A verb several nouns share names all of them: dsx cannot know which
		// was meant, and listing beats guessing.
		{"ls", []string{"dsx ds ls", "dsx files ls", "dsx member ls", "dsx project ls"}},
		{"put", []string{"dsx conv put", "dsx files put"}},
		{"rm", []string{"dsx files rm", "dsx member rm"}},
		{"new", []string{"dsx plan new", "dsx project new"}},
		// A pluralised noun is the other half of the same mistake, and the
		// bare noun is a form that parses.
		{"members", []string{"dsx member"}},
		{"projects", []string{"dsx project"}},
		{"plans", []string{"dsx plan"}},
		{"file", []string{"dsx files"}},
	} {
		t.Run(tc.typed, func(t *testing.T) {
			_, err := maincliRun(t, tc.typed, "x")
			if err == nil {
				t.Fatalf("dsx %s x succeeded", tc.typed)
			}
			got := err.Error()
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("refusal = %q, want it to name %q", got, w)
				}
			}
			if !strings.Contains(got, "dsx help") {
				t.Errorf("refusal = %q, want it to keep naming `dsx help`", got)
			}
		})
	}
}

// A token that resembles nothing gets the message it always got. A suggestion
// invented for every typo is a suggestion worth nothing.
func TestATokenLikeNothingElseGetsNoSuggestion(t *testing.T) {
	t.Setenv("DSX_TOKEN", "")
	diagPinCredentialStore(t)

	for _, typed := range []string{"zzz", "qqqq", "xyzzy"} {
		_, err := maincliRun(t, typed)
		if err == nil {
			t.Fatalf("dsx %s succeeded", typed)
		}
		if strings.Contains(err.Error(), "did you mean") {
			t.Errorf("dsx %s: refusal = %q, want no suggestion", typed, err.Error())
		}
	}
}

// Invariant 18 as data, one level tighter than the source-prose guard: every
// form the refusal itself builds at runtime must resolve in the registry.
func TestEverySuggestedFormParses(t *testing.T) {
	t.Setenv("DSX_TOKEN", "")
	diagPinCredentialStore(t)

	quoted := regexp.MustCompile("`dsx ([^`]+)`")
	checked := 0
	for _, g := range groups {
		for _, c := range g.Cmds {
			// Feed dispatch the verb alone — the spelling the flat surface used.
			typed := c.Name
			if g.Noun != "" {
				typed = strings.TrimPrefix(c.Name, g.Noun+" ")
			}
			if _, isNoun := nounIndex[typed]; isNoun {
				continue
			}
			if _, isCommand := commandIndex[typed]; isCommand {
				continue
			}
			_, err := maincliRun(t, typed)
			if err == nil {
				continue
			}
			for _, m := range quoted.FindAllStringSubmatch(err.Error(), -1) {
				form := m[1]
				if form == "help" {
					continue
				}
				if _, ok := commandIndex[form]; ok {
					checked++
					continue
				}
				if _, ok := nounIndex[form]; ok {
					checked++
					continue
				}
				t.Errorf("dsx %s suggested `dsx %s`, which resolves to nothing", typed, form)
			}
		}
	}
	if checked < 10 {
		t.Fatalf("only %d suggestions were checked; the walk found nothing to guard", checked)
	}
}
