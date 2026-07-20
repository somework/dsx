package cli

import (
	"slices"
	"strings"

	"github.com/somework/dsx/internal/cmd"
)

// commandFlags is every flag `dsx <name>` accepts, spelled as the user types
// it. Two homes, per the Form/usageFooter convention: a command-unique flag
// lives in the Form, a shared one in a footer scope naming its commands.
//
// The footer is parsed here rather than shared with registry_test.go's reader
// on purpose — two independent readers that must agree is a check, one shared
// reader is a tautology. TestCompletionAndTheFooterAgreeOnEveryCommand holds
// them together.
func commandFlags(c cmd.Command) []string {
	seen := map[string]bool{}
	var out []string
	add := func(tok string) {
		if tok == "" || seen[tok] {
			return
		}
		seen[tok] = true
		out = append(out, tok)
	}

	for _, f := range formFlagTokens(c.Form) {
		add(spellFlag(f))
	}
	for flag, commands := range footerScopes() {
		if commands["*"] || commands[c.Name] {
			add(spellFlag(flag))
		}
	}
	slices.Sort(out)
	return out
}

// spellFlag restores the dashes a user types: Go's flag package accepts one or
// two for either, and dsx documents single-letter flags with one.
func spellFlag(name string) string {
	if len(name) == 1 {
		return "-" + name
	}
	return "--" + name
}

func formFlagTokens(form string) []string {
	var out []string
	for _, f := range strings.Fields(form) {
		f = strings.Trim(f, "[]()|,'\"`")
		if len(f) < 2 || f[0] != '-' {
			continue
		}
		out = append(out, strings.TrimLeft(f, "-"))
	}
	return out
}

// footerScopes reads usageFooter as data: a flag line names its flags, then a
// description, then the commands it reaches after an em dash.
func footerScopes() map[string]map[string]bool {
	scopes := map[string]map[string]bool{}
	for _, line := range strings.Split(usageFooter, "\n") {
		flagPart, commandPart, ok := strings.Cut(line, "—")
		if !ok {
			continue
		}
		flags := formFlagTokens(flagPart)
		if len(flags) == 0 {
			continue
		}
		commands := map[string]bool{}
		if strings.Contains(commandPart, "every command") {
			commands["*"] = true
		}
		for _, c := range strings.Split(commandPart, ",") {
			if n := strings.TrimSpace(c); n != "" {
				commands[n] = true
			}
		}
		for _, f := range flags {
			scopes[f] = commands
		}
	}
	return scopes
}
