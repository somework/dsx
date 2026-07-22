package cli

import (
	"sort"
	"strings"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/cmd/conv"
	"github.com/somework/dsx/internal/cmd/ds"
	"github.com/somework/dsx/internal/cmd/escape"
	"github.com/somework/dsx/internal/cmd/files"
	"github.com/somework/dsx/internal/cmd/members"
	"github.com/somework/dsx/internal/cmd/plans"
	"github.com/somework/dsx/internal/cmd/projects"
	// dir sync, package synccmd (avoids shadowing stdlib sync)
	"github.com/somework/dsx/internal/cmd/sync"
)

var groups = []cmd.Group{
	synccmd.Group, projects.Group, ds.Group, files.Group, plans.Group,
	conv.Group, members.Group, escape.Group, diagGroup,
}

var (
	commandIndex map[string]cmd.Command
	commandNames []string
	commandSpecs []commandSpec
	// nounIndex answers the only question dispatch asks of the first token:
	// is this a noun, and if so which group does it open?
	nounIndex map[string]cmd.Group
	// topNames is what a shell offers in the first position: every flat
	// command plus every noun, and none of the verbs.
	topNames []string
)

// Derivation stays in init() rather than in var initialisers: completionScript
// reads these, diagGroup holds cmdCompletion, and groups holds diagGroup, so a
// var initialiser closes an initialization cycle the compiler refuses.
func init() {
	commandIndex = indexCommands(groups)
	commandNames = commandNamesOf(groups)
	commandSpecs = commandSpecsOf(groups)
	nounIndex = indexNouns(groups)
	topNames = topNamesOf(groups)
}

// commandSpec is help --json's shape: the registry as a machine reads it,
// so per-command invocation syntax does not survive only as prose.
type commandSpec struct {
	Group   string   `json:"group"`
	Noun    string   `json:"noun,omitempty"`
	Name    string   `json:"name"`
	Form    string   `json:"form"`
	Desc    string   `json:"desc,omitempty"`
	Section string   `json:"section,omitempty"`
	Aliases []string `json:"aliases,omitempty"`
}

func indexCommands(gs []cmd.Group) map[string]cmd.Command {
	out := make(map[string]cmd.Command)
	for _, g := range gs {
		for _, c := range g.Cmds {
			out[c.Name] = c
			for _, a := range c.Aliases {
				out[a] = c
			}
		}
	}
	return out
}

func commandNamesOf(gs []cmd.Group) []string {
	var out []string
	for _, g := range gs {
		for _, c := range g.Cmds {
			out = append(out, c.Name)
		}
	}
	sort.Strings(out)
	return out
}

func commandSpecsOf(gs []cmd.Group) []commandSpec {
	var out []commandSpec
	for _, g := range gs {
		for _, c := range g.Cmds {
			out = append(out, commandSpec{
				Group:   g.Title,
				Noun:    g.Noun,
				Name:    c.Name,
				Form:    c.Form,
				Desc:    c.Desc,
				Section: c.Section,
				Aliases: c.Aliases,
			})
		}
	}
	return out
}

func indexNouns(gs []cmd.Group) map[string]cmd.Group {
	out := make(map[string]cmd.Group)
	for _, g := range gs {
		if g.Noun != "" {
			out[g.Noun] = g
		}
	}
	return out
}

func topNamesOf(gs []cmd.Group) []string {
	var out []string
	for _, g := range gs {
		if g.Noun != "" {
			out = append(out, g.Noun)
			continue
		}
		for _, c := range g.Cmds {
			out = append(out, c.Name)
		}
	}
	sort.Strings(out)
	return out
}

// nounVerbs is the second completion level: the verbs of one noun, spelled
// without it.
func nounVerbs(g cmd.Group) []string {
	var out []string
	for _, c := range g.Cmds {
		out = append(out, strings.TrimPrefix(c.Name, g.Noun+" "))
	}
	return out
}
