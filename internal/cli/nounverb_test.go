package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/dsxerr"
)

// nounFixture is a group shaped like a real noun group, used where a test must
// exercise the mechanism rather than today's registry. The production
// assertions below read `groups`; these fixtures exist so a mechanism defect
// cannot hide behind whichever groups happen to carry a Noun today.
func nounFixture() cmd.Group {
	return cmd.Group{
		Title: "FIXTURE",
		Noun:  "fixture",
		Desc:  "a group that exists only in tests",
		Cmds: []cmd.Command{
			{Name: "fixture one", Form: "fixture one <arg>", Desc: "the first verb"},
			{Name: "fixture two", Form: "fixture two", Desc: "the second verb"},
			{Name: "fixture three", Form: "fixture three", Desc: "the third verb"},
		},
	}
}

func TestANounResolvesItsVerbToTheRightCommand(t *testing.T) {
	t.Parallel()
	for _, want := range []string{"conv get", "conv put"} {
		if _, ok := commandIndex[want]; !ok {
			t.Errorf("commandIndex has no %q; `dsx %s` cannot reach a command", want, want)
		}
	}
}

// The flat spellings are removed outright — no alias, no deprecation. A caller
// typing the old name must land in the unknown-command refusal, not in a
// command that quietly still answers.
func TestTheFlatSpellingsOfAMigratedNounAreGone(t *testing.T) {
	t.Parallel()
	for _, gone := range []string{"conv-put"} {
		if _, ok := commandIndex[gone]; ok {
			t.Errorf("commandIndex still answers to %q; the flat form was meant to be removed", gone)
		}
	}
	if c, ok := commandIndex["conv"]; ok {
		t.Errorf("`dsx conv` still resolves to command %q; conv is a noun now, not a verb", c.Name)
	}
}

func TestAnUnknownVerbUnderAKnownNounNamesAFormThatParses(t *testing.T) {
	t.Setenv("DSX_TOKEN", "")
	diagPinCredentialStore(t)

	_, err := maincliRun(t, "conv", "meow")
	if got := maincliKind(t, err); got != dsxerr.KindUsage {
		t.Errorf("kind = %v, want %v", got, dsxerr.KindUsage)
	}
	msg := err.Error()
	if !strings.Contains(msg, `"meow"`) {
		t.Errorf("refusal does not quote the verb it rejected: %q", msg)
	}
	if !strings.Contains(msg, "dsx conv -h") {
		t.Errorf("refusal = %q, want it to name `dsx conv -h`", msg)
	}
	// The form the refusal names must itself parse. Run it.
	if _, err := maincliRun(t, "conv", "-h"); err != nil {
		t.Errorf("the refusal names `dsx conv -h`, which fails: %v", err)
	}
}

// Every noun verb must be reachable through run(), which is the only place the
// two tokens are joined back into an address. Reading commandIndex proves the
// command was declared, not that dispatch finds it: with the noun dropped from
// the key, every lookup misses and the miss is reported in the same words as a
// genuinely unknown verb.
//
// -h is what makes this affordable for the whole registry — it answers before
// auth and before any call, so the assertion is about resolution alone.
func TestEveryNounVerbIsReachedThroughDispatch(t *testing.T) {
	t.Setenv("DSX_TOKEN", "")
	diagPinCredentialStore(t)

	for _, g := range groups {
		if g.Noun == "" {
			continue
		}
		for _, c := range g.Cmds {
			verb := strings.TrimPrefix(c.Name, g.Noun+" ")
			t.Run(c.Name, func(t *testing.T) {
				out, err := maincliRun(t, g.Noun, verb, "-h")
				if err != nil {
					t.Fatalf("dsx %s %s -h: %v", g.Noun, verb, err)
				}
				if !strings.Contains(out, c.Form) {
					t.Errorf("dsx %s %s -h printed %q, want the form of %q", g.Noun, verb, out, c.Name)
				}
			})
		}
	}
}

func TestAnUnknownNounIsJustAnUnknownCommand(t *testing.T) {
	t.Setenv("DSX_TOKEN", "")
	diagPinCredentialStore(t)

	// A removed flat spelling arrives here too: at the first token, "meant a
	// noun" and "meant a verb" are indistinguishable.
	for _, name := range []string{"convv", "conv-put"} {
		_, err := maincliRun(t, name, "x")
		if got := maincliKind(t, err); got != dsxerr.KindUsage {
			t.Errorf("dsx %s: kind = %v, want %v", name, got, dsxerr.KindUsage)
		}
		if msg := err.Error(); !strings.Contains(msg, "dsx help") {
			t.Errorf("dsx %s: refusal = %q, want it to name `dsx help`", name, msg)
		}
	}
	if _, err := maincliRun(t, "help"); err != nil {
		t.Errorf("the refusal names `dsx help`, which fails: %v", err)
	}
}

// A bare noun answers like a bare `dsx` does: it lists what it holds and exits
// zero. TestRunWithNoArgumentsPrintsUsageAndSucceeds is the precedent one level
// up; the same question one level down deserves the same answer.
func TestABareNounListsItsVerbsAndSucceeds(t *testing.T) {
	t.Setenv("DSX_TOKEN", "")
	diagPinCredentialStore(t)

	for _, argv := range [][]string{{"conv"}, {"conv", "-h"}, {"conv", "--help"}} {
		out, err := maincliRun(t, argv...)
		if err != nil {
			t.Fatalf("dsx %s: %v", strings.Join(argv, " "), err)
		}
		for _, want := range []string{"conv get", "conv put"} {
			if !strings.Contains(out, want) {
				t.Errorf("dsx %s did not list %q:\n%s", strings.Join(argv, " "), want, out)
			}
		}
	}
}

func TestABareNounAnswersInJSONToo(t *testing.T) {
	t.Setenv("DSX_TOKEN", "")
	diagPinCredentialStore(t)

	out, err := maincliRun(t, "conv", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Noun  string `json:"noun"`
		Verbs []struct {
			Name string `json:"name"`
			Form string `json:"form"`
			Desc string `json:"desc"`
		} `json:"verbs"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("dsx conv --json is not the verb list: %v (%q)", err, out)
	}
	if got.Noun != "conv" {
		t.Errorf("noun = %q, want conv", got.Noun)
	}
	if len(got.Verbs) != 2 {
		t.Fatalf("verbs = %v, want the two conv verbs", got.Verbs)
	}
	for _, v := range got.Verbs {
		if !strings.HasPrefix(v.Name, "conv ") {
			t.Errorf("verb %q is not addressed under its noun", v.Name)
		}
		if !strings.HasPrefix(v.Form, v.Name) {
			t.Errorf("verb %q carries form %q, which documents another invocation", v.Name, v.Form)
		}
	}
}

// A noun sharing a name with a flat command makes `dsx <that word>` mean two
// things, and which one wins is a property of lookup order rather than of any
// decision anyone made.
func TestNoNounSharesANameWithAFlatCommand(t *testing.T) {
	t.Parallel()
	flat := map[string]bool{}
	for _, g := range groups {
		if g.Noun != "" {
			continue
		}
		for _, c := range g.Cmds {
			flat[c.Name] = true
			for _, a := range c.Aliases {
				flat[a] = true
			}
		}
	}
	for _, g := range groups {
		if g.Noun != "" && flat[g.Noun] {
			t.Errorf("noun %q is also a command name; `dsx %s` means two things", g.Noun, g.Noun)
		}
	}
}

// Every command a noun group declares must be addressable through that noun.
// The name and the noun are declared separately, so nothing but this stops them
// drifting apart — and a command whose name does not start with its group's
// noun is unreachable, since dispatch builds the key from the noun it matched.
func TestEveryNounCommandIsAddressedByItsNoun(t *testing.T) {
	t.Parallel()
	for _, g := range groups {
		if g.Noun == "" {
			continue
		}
		for _, c := range g.Cmds {
			if !strings.HasPrefix(c.Name, g.Noun+" ") {
				t.Errorf("group %q declares %q, which `dsx %s <verb>` cannot reach", g.Noun, c.Name, g.Noun)
			}
			if verb := strings.TrimPrefix(c.Name, g.Noun+" "); strings.Contains(verb, " ") {
				t.Errorf("%q addresses three tokens; dispatch reads exactly two", c.Name)
			}
		}
	}
}

// Sections are all-or-nothing within a group. One verb left without one renders
// under the previous verb's heading, or under no heading at all when it comes
// first — a silent miscategorisation rather than a missing line.
func TestAGroupDeclaresSectionsForEveryVerbOrForNone(t *testing.T) {
	t.Parallel()
	for _, g := range groups {
		withSection := 0
		for _, c := range g.Cmds {
			if c.Section != "" {
				withSection++
			}
		}
		if withSection != 0 && withSection != len(g.Cmds) {
			t.Errorf("group %q gives %d of %d verbs a section; the rest render under whatever heading precedes them",
				g.Title, withSection, len(g.Cmds))
		}
	}
}

// A verb spelled like a flag would be unreachable: a leading dash in the verb
// position is read as a help request, which is what keeps `dsx conv --json`
// from refusing a flag by calling it an unknown verb.
func TestNoVerbIsSpelledLikeAFlag(t *testing.T) {
	t.Parallel()
	for _, g := range groups {
		if g.Noun == "" {
			continue
		}
		for _, c := range g.Cmds {
			verb := strings.TrimPrefix(c.Name, g.Noun+" ")
			if strings.HasPrefix(verb, "-") {
				t.Errorf("%q spells its verb like a flag; the help branch swallows it", c.Name)
			}
		}
	}
}

// Every declared name must survive indexing. Two commands sharing a key means
// one of them is simply absent from the binary, and nothing else notices.
func TestEveryDeclaredCommandSurvivesIndexing(t *testing.T) {
	t.Parallel()
	declared := 0
	for _, g := range groups {
		declared += len(g.Cmds)
	}
	indexed := map[string]bool{}
	for _, g := range groups {
		for _, c := range g.Cmds {
			indexed[c.Name] = true
		}
	}
	if len(indexed) != declared {
		t.Errorf("commandIndex holds %d of %d declared names; two commands share a key", len(indexed), declared)
	}
}

// The fixture assertions below exercise the renderer directly, so a defect in
// it cannot hide behind whichever groups carry a Noun in the registry today.
func TestTheRootUsageGivesANounOneLine(t *testing.T) {
	t.Parallel()
	out := renderUsage([]cmd.Group{nounFixture()})
	if strings.Contains(out, "fixture one <arg>") {
		t.Errorf("the root usage spells a noun's verbs; it must give the noun one line:\n%s", out)
	}
	if !strings.Contains(out, "dsx fixture <verb>") {
		t.Errorf("the root usage does not offer the noun:\n%s", out)
	}
	if !strings.Contains(out, "a group that exists only in tests") {
		t.Errorf("the root usage drops the group's own summary:\n%s", out)
	}
}

func TestANounHelpSpellsEveryVerbInFull(t *testing.T) {
	t.Parallel()
	out := renderNounHelp(nounFixture())
	for _, want := range []string{"dsx fixture one <arg>", "the first verb", "dsx fixture two", "the second verb"} {
		if !strings.Contains(out, want) {
			t.Errorf("the noun help drops %q:\n%s", want, out)
		}
	}
}

// Sections are presentation: a group that declares none renders one flat list,
// and one that declares them renders each under its own heading, in declaration
// order.
func TestSectionsSplitANounHelpAndTheirAbsenceDoesNot(t *testing.T) {
	t.Parallel()
	flat := renderNounHelp(nounFixture())
	if strings.Count(flat, "\n\n") != 0 {
		t.Errorf("a group declaring no section rendered a blank line, so it is being split anyway:\n%s", flat)
	}

	// Two verbs share the first section on purpose: a heading printed per verb
	// rather than per change of section renders in the right order and passes
	// every ordering check, so only a repeat count can see it.
	g := nounFixture()
	g.Cmds[0].Section, g.Cmds[1].Section, g.Cmds[2].Section = "READ", "READ", "WRITE"
	sectioned := renderNounHelp(g)
	read, write := strings.Index(sectioned, "READ"), strings.Index(sectioned, "WRITE")
	if read < 0 || write < 0 {
		t.Fatalf("the section headings are missing:\n%s", sectioned)
	}
	if read > write {
		t.Errorf("sections rendered out of declaration order:\n%s", sectioned)
	}
	if n := strings.Count(sectioned, "READ"); n != 1 {
		t.Errorf("READ heading appears %d times; a section is one heading, not one per verb:\n%s", n, sectioned)
	}
}

// wantFilesHelp is hand-written for the same reason wantUsage is: regenerating
// it would prove the renderer equals itself. It is here because `dsx files -h`
// is the only place READ and WRITE are stated, and which verb sits under which
// is a claim about what the command does to the server. Without these bytes,
// moving `files rm` into READ passes every test in the repo.
const wantFilesHelp = `usage: dsx files <verb>
one project's files, read and written

READ
  dsx files tree [<project>]                                        every file, recursive, with etags
  dsx files cat [<project>] <path> [--out f]                        read a file (stdout by default)
  dsx files preview <project> <path> [--render] [--validators a,b]  preview links for one file
  dsx files ls <project> [path]                                     list one directory

WRITE
  dsx files put <project> <path> [file]                             write a file (stdin when file is omitted)
  dsx files rm <project> <path...>                                  delete files
  dsx files cp <project> <src> <dst> [--from <project>]

  tree and cat fall back to the directory's project when none is named; a named
  one still wins. ls and every write always name theirs: a lone positional would
  mean the project or the path, and the working directory must not choose the
  target of a destructive act.
`

func TestTheFilesHelpIsWhatWeWrote(t *testing.T) {
	t.Parallel()
	g, ok := nounIndex["files"]
	if !ok {
		t.Fatal("files is no longer a noun")
	}
	got := renderNounHelp(g)
	if got == wantFilesHelp {
		return
	}
	gl, wl := strings.Split(got, "\n"), strings.Split(wantFilesHelp, "\n")
	for i := 0; i < len(gl) && i < len(wl); i++ {
		if gl[i] != wl[i] {
			t.Fatalf("dsx files -h line %d differs\n got %q\nwant %q", i+1, gl[i], wl[i])
		}
	}
	t.Fatalf("dsx files -h has %d lines, want %d", len(gl), len(wl))
}

// One very long form must not push every other description in the group out to
// meet it. conv put is that form today — 100-odd characters — and the group's
// other verb would otherwise carry its description far past the terminal's
// width. The overflowing line itself is accepted; dragging its neighbours is
// not.
func TestOneOverlongFormDoesNotDragTheColumn(t *testing.T) {
	t.Parallel()
	g := nounFixture()
	g.Cmds[2].Form = "fixture three " + strings.Repeat("<verylongargument> ", 8)

	for _, line := range strings.Split(renderNounHelp(g), "\n") {
		if !strings.Contains(line, "the first verb") {
			continue
		}
		if col := strings.Index(line, "the first verb"); col > usageDescColMax {
			t.Errorf("a short form's description starts at column %d, past the %d cap: %q",
				col, usageDescColMax, line)
		}
		return
	}
	t.Fatal("the short verb's description vanished from the rendered help")
}

func TestHelpJSONCarriesTheNounAndTheFullAddress(t *testing.T) {
	out, err := captureStdout(t, func() error { return cmdHelp([]string{"--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Commands []struct {
			Name string `json:"name"`
			Noun string `json:"noun"`
		} `json:"commands"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, c := range got.Commands {
		seen[c.Name] = c.Noun
	}
	if noun, ok := seen["conv get"]; !ok {
		t.Error("help --json does not carry `conv get`; the machine channel cannot discover the address")
	} else if noun != "conv" {
		t.Errorf("`conv get` carries noun %q, want conv", noun)
	}
	if noun, ok := seen["pull"]; !ok {
		t.Error("help --json lost a flat command")
	} else if noun != "" {
		t.Errorf("flat command pull carries noun %q, want empty", noun)
	}
}

// The shells must offer both levels. The flat half is the positive control: it
// proves the walk still finds names at all, so the noun half cannot pass by
// finding nothing anywhere.
func TestEveryShellOffersBothLevels(t *testing.T) {
	t.Parallel()
	for _, shell := range []string{"bash", "zsh", "fish"} {
		script, err := completionScript(shell)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(script, "pull") {
			t.Errorf("%s completion offers no flat command", shell)
		}
		if !strings.Contains(script, "conv") {
			t.Errorf("%s completion offers no noun", shell)
		}
		for _, verb := range []string{"get", "put"} {
			if !strings.Contains(script, verb) {
				t.Errorf("%s completion offers no %q under its noun", shell, verb)
			}
		}
		if strings.Contains(script, "conv-put") {
			t.Errorf("%s completion still offers the removed flat spelling", shell)
		}
	}
}
