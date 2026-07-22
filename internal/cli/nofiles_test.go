package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Every address fell through to file completion, including the ones that take
// no argument at all: `dsx pull <TAB>` offered the whole directory, and every
// one of those names is a thing the binary then refuses with exit 2. The shell
// was teaching an invocation dsx does not accept.
//
// The rule is deliberately narrower than "takes no positional": a Form
// mentioning any argument at all keeps file completion, because a flag's value
// is one — `dsx diff --out <TAB>` wants directories, and diff's only argument
// lives inside a flag group. Read the other way round it would be a
// regression, which is why TestFileCompletionSurvivesAFlagsOnlyValue is a
// control rather than an afterthought.
func TestNoFileCompletionWhereTheFormTakesNoArgument(t *testing.T) {
	t.Parallel()
	want := map[string]bool{}
	for _, g := range groups {
		for _, c := range g.Cmds {
			if !takesAnArgument(c) {
				want[c.Name] = true
			}
		}
	}
	for _, name := range []string{"pull", "push", "status", "fetch", "help", "auth", "doctor", "version", "tools", "project ls", "ds ls"} {
		if !want[name] {
			t.Errorf("%q was expected to take no argument; the Form reader disagrees", name)
		}
	}
	for _, name := range []string{"clone", "pin", "unpin", "diff", "prompt", "files cat", "raw", "completion"} {
		if want[name] {
			t.Errorf("%q takes an argument; removing its file completion is a regression", name)
		}
	}
}

// bashComplete drives the real shell. bash is present wherever this suite
// runs, so unlike the fish sibling this one is a floor under CI too.
func bashComplete(t *testing.T, words ...string) []string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	script, err := completionScript("bash")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "dsx.bash")
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	// A file to be offered, so "nothing came back" cannot pass by an empty
	// directory.
	if err := os.WriteFile(filepath.Join(dir, "decoy.css"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	quoted := make([]string, 0, len(words))
	for _, w := range words {
		quoted = append(quoted, "'"+w+"'")
	}
	drive := "source " + path + "\n" +
		"COMP_WORDS=(dsx " + strings.Join(quoted, " ") + " '')\n" +
		"COMP_CWORD=" + itoa(len(words)+1) + "\n" +
		"_dsx\n" +
		`printf '%s\n' "${COMPREPLY[@]}"`
	cmd := exec.Command(bash, "--noprofile", "--norc", "-c", drive)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash: %v\n%s", err, out)
	}
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			got = append(got, l)
		}
	}
	return got
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestBashOffersNothingAfterAnArgumentlessVerb(t *testing.T) {
	t.Parallel()
	for _, words := range [][]string{{"pull"}, {"push"}, {"status"}, {"fetch"}, {"version"}} {
		if got := bashComplete(t, words...); len(got) > 0 {
			t.Errorf("dsx %s <TAB> offered %v; the command takes no argument", words[0], got)
		}
	}
}

// The controls: a command that takes a path still gets one, and so does a flag
// value. Without these the change could pass by removing file completion
// everywhere.
func TestFileCompletionSurvivesAFlagsOnlyValue(t *testing.T) {
	t.Parallel()
	for _, words := range [][]string{{"clone"}, {"unpin"}, {"diff", "--out"}} {
		got := bashComplete(t, words...)
		if !contains(got, "decoy.css") {
			t.Errorf("dsx %s <TAB> offered %v, want the directory", strings.Join(words, " "), got)
		}
	}
}
