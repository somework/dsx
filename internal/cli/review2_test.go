package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHelpAndCompletionHonourJSONLikeEveryOtherCommand(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"help", func() error { return cmdHelp([]string{"--json"}) }},
		{"completion", func() error { return cmdCompletion([]string{"bash", "--json"}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := captureStdout(t, tc.run)
			if err != nil {
				t.Fatal(err)
			}
			if !json.Valid([]byte(out)) {
				t.Fatalf("%s --json is not JSON: %q", tc.name, out[:min(len(out), 120)])
			}
		})
	}

	out, err := captureStdout(t, func() error { return cmdCompletion([]string{"bash"}) })
	if err != nil {
		t.Fatal(err)
	}
	if json.Valid([]byte(out)) || !strings.Contains(out, "complete -F _dsx dsx") {
		t.Errorf("prose completion is no longer a shell script: %q", out[:min(len(out), 120)])
	}
}

// help --json is the registry, not the prose: every command declared in `groups`
// must arrive with its own invocation syntax and its group's title.
func TestHelpJSONCarriesPerCommandInvocationSyntax(t *testing.T) {
	out, err := captureStdout(t, func() error { return cmdHelp([]string{"--json"}) })
	if err != nil {
		t.Fatal(err)
	}

	var got struct {
		Commands []struct {
			Group   string   `json:"group"`
			Name    string   `json:"name"`
			Form    string   `json:"form"`
			Desc    string   `json:"desc"`
			Aliases []string `json:"aliases"`
		} `json:"commands"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("help --json does not carry a command registry: %v", err)
	}

	byName := make(map[string]string, len(got.Commands))
	groupOf := make(map[string]string, len(got.Commands))
	for _, c := range got.Commands {
		byName[c.Name] = c.Form
		groupOf[c.Name] = c.Group
	}

	for _, g := range groups {
		for _, c := range g.Cmds {
			form, ok := byName[c.Name]
			if !ok {
				t.Errorf("%s is declared in groups but missing from help --json", c.Name)
				continue
			}
			if form != c.Form {
				t.Errorf("%s: form %q, want %q", c.Name, form, c.Form)
			}
			if groupOf[c.Name] != g.Title {
				t.Errorf("%s: group %q, want %q", c.Name, groupOf[c.Name], g.Title)
			}
		}
	}
}

// The machine channel must carry the flag block too. A command's Form spells only
// its positionals and its command-unique flags, so --if-match — the blind-overwrite
// guard — lives nowhere but usageFooter: not in put's Form, not in the README. An
// agent driving writes through help --json could not discover it. Pinning the whole
// block rather than a sample inherits TestEveryDeclaredFlagIsDocumented's guarantee:
// every declared flag reaches the machine channel, for free.
func TestHelpJSONCarriesTheFlagBlock(t *testing.T) {
	out, err := captureStdout(t, func() error { return cmdHelp([]string{"--json"}) })
	if err != nil {
		t.Fatal(err)
	}

	var got struct {
		Flags string `json:"flags"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("help --json is not an object: %v", err)
	}
	if got.Flags != usageFooter {
		t.Errorf("help --json flags block:\n%q\nwant:\n%q", got.Flags, usageFooter)
	}
	for _, flag := range []string{"--if-match", "--plan"} {
		if !strings.Contains(got.Flags, flag) {
			t.Errorf("%s reaches no machine-readable channel", flag)
		}
	}
}
