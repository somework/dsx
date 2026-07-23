package cli

import (
	"slices"
	"strings"
	"testing"
)

func flagsOf(t *testing.T, name string) []string {
	t.Helper()
	c, ok := commandIndex[name]
	if !ok {
		t.Fatalf("%s is not a registered command", name)
	}
	return commandFlags(c)
}

// completion.go used to offer one hardcoded list to every command, so
// `dsx files cat --<TAB>` proposed --prune, a flag cat rejects as unknown.
func TestCommandFlagsAreThePerCommandTruth(t *testing.T) {
	for _, tc := range []struct {
		name    string
		want    []string
		mustNot []string
	}{
		{"pull", []string{"--binary", "--force", "--json", "--prune", "-j", "-n", "-q"}, nil},
		{"files cat", []string{"--json", "--out"}, []string{"--prune", "--force", "-n"}},
		{"files tree", []string{"--json", "-j"}, []string{"--prune", "--force"}},
		{"files put", []string{"--if-match", "--json", "--plan"}, []string{"--prune", "--force"}},
		{"project ls", []string{"--json"}, []string{"--prune", "--force", "-j"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := flagsOf(t, tc.name)
			if !slices.Equal(got, tc.want) {
				t.Errorf("flags=%v, want %v", got, tc.want)
			}
			for _, bad := range tc.mustNot {
				if slices.Contains(got, bad) {
					t.Errorf("%s was offered %s, which it rejects as unknown", tc.name, bad)
				}
			}
		})
	}
}

// Every command takes --json; the footer says so with "every command".
func TestEveryCommandIsOfferedJSON(t *testing.T) {
	for _, name := range commandNames {
		if !slices.Contains(flagsOf(t, name), "--json") {
			t.Errorf("%s was not offered --json", name)
		}
	}
}

// A single-letter flag is spelled with one dash, as usageFooter spells it; a
// completion offering --j would propose a spelling dsx documents nowhere.
func TestSingleLetterFlagsKeepOneDash(t *testing.T) {
	for _, f := range flagsOf(t, "pull") {
		trimmed := strings.TrimLeft(f, "-")
		dashes := len(f) - len(trimmed)
		if len(trimmed) == 1 && dashes != 1 {
			t.Errorf("%s is spelled with %d dashes, want 1", f, dashes)
		}
		if len(trimmed) > 1 && dashes != 2 {
			t.Errorf("%s is spelled with %d dashes, want 2", f, dashes)
		}
	}
}

// The generated script must offer each command exactly its own flags, in every
// shell — that is the whole point of the change.
func TestCompletionOffersPerCommandFlags(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			script, err := completionScript(shell)
			if err != nil {
				t.Fatal(err)
			}
			// fish spells a long option as `-l out`, the others as `--out`.
			needle := "--out"
			if shell == "fish" {
				needle = "-l out"
			}
			if !strings.Contains(script, needle) {
				t.Errorf("script never offers %q, so cat's own flag is uncompletable", needle)
			}
			for _, bad := range []string{
				"cat --prune", "cat --force", "projects --prune",
			} {
				if strings.Contains(script, bad) {
					t.Errorf("script offers %q", bad)
				}
			}
		})
	}
}

// fish disables file completion globally (complete -c dsx -f), so a path
// positional gets nothing at all unless the script asks for it back.
func TestFishStillCompletesPaths(t *testing.T) {
	script, err := completionScript("fish")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(script, "complete -c dsx -f\n") && !strings.Contains(script, "-F") {
		t.Error("fish turns off file completion and never turns it back on for path arguments")
	}
}
