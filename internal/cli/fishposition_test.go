package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// __fish_seen_subcommand_from answers "this word appeared somewhere in the
// line", not "this word is the nth argument", and the two are the same thing
// only while every word is unique to one command. dsx's are not: `get` belongs
// to project and to conv, `put` to files and to conv, `ls` to four nouns. So
// a predicate written with it offers the union of every command sharing either
// token — `dsx project get --` proposed --chat, which belongs to conv — and it
// does so on a line that parses, not on one already broken. bash and zsh key
// off an index and were always right; only the fish generator has to be told
// what position means.
func TestTheFishScriptTestsPositionNotMembership(t *testing.T) {
	t.Parallel()
	script, err := completionScript("fish")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(script, "__fish_seen_subcommand_from") {
		t.Error("the fish script still keys off __fish_seen_subcommand_from, " +
			"which cannot tell an argument from a command name")
	}
}

// fishComplete drives the real shell. The structural test above names the
// mechanism; this one is the only thing that can see whether the replacement
// actually works, and it was what found the defect. It skips where fish is
// absent — CI included — so it is a floor under local work, not under CI.
func fishComplete(t *testing.T, line string) []string {
	t.Helper()
	fish, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish is not installed")
	}
	script, err := completionScript("fish")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "dsx.fish")
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(fish, "--no-config", "-c",
		"source "+path+"; complete -C "+fishQuote(line)).CombinedOutput()
	if err != nil {
		t.Fatalf("fish: %v\n%s", err, out)
	}
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l == "" {
			continue
		}
		got = append(got, strings.SplitN(l, "\t", 2)[0])
	}
	sort.Strings(got)
	return got
}

func fishQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `\'`) + "'"
}

func TestFishOffersEachAddressExactlyItsOwnFlags(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		line string
		want []string
	}{
		// project get takes --json and nothing else; --chat is conv's, and it
		// reached here only because `get` is also a conv verb.
		{"dsx project get --", []string{"--json"}},
		{"dsx files cat --", []string{"--json", "--out"}},
		{"dsx conv get --", []string{"--chat", "--json"}},
		{"dsx member ls --", []string{"--json"}},
		{"dsx ds ls --", []string{"--json"}},
	} {
		t.Run(tc.line, func(t *testing.T) {
			t.Parallel()
			if got := fishComplete(t, tc.line); !equalStrings(got, tc.want) {
				t.Errorf("fish offered %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFishOffersAVerbOnlyWhereAVerbGoes(t *testing.T) {
	t.Parallel()
	// Second position: the verbs.
	got := fishComplete(t, "dsx files ")
	for _, verb := range nounVerbs(nounIndex["files"]) {
		if !contains(got, verb) {
			t.Errorf("dsx files <TAB> offered %v, missing %q", got, verb)
		}
	}
	// Third position, with the noun's own name typed as an argument. A
	// membership test reads that as "saw files" and offers the verb list on
	// top of the path completion cat actually wants.
	if got := fishComplete(t, "dsx files cat files"); contains(got, "tree") {
		t.Errorf("a noun name used as an argument re-triggered the verb list: %v", got)
	}
}

// -C comes off before the command name is read (splitGlobalFlags), so a
// position test that counts raw tokens is two off for every `dsx -C dir` line.
func TestFishCountsPositionsPastTheGlobalFlag(t *testing.T) {
	t.Parallel()
	for _, line := range []string{"dsx -C /tmp files ", "dsx -C=/tmp files "} {
		if got := fishComplete(t, line); !contains(got, "tree") {
			t.Errorf("%q offered %v, want the files verbs", line, got)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
