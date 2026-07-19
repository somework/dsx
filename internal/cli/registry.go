package cli

import (
	"sort"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/cmd/conv"
	"github.com/somework/dsx/internal/cmd/escape"
	"github.com/somework/dsx/internal/cmd/files"
	"github.com/somework/dsx/internal/cmd/members"
	"github.com/somework/dsx/internal/cmd/plans"
	"github.com/somework/dsx/internal/cmd/projects"
	// dir sync, package synccmd (avoids shadowing stdlib sync)
	"github.com/somework/dsx/internal/cmd/sync"
)

var groups = []cmd.Group{
	synccmd.Group, projects.Group, files.Group, plans.Group,
	conv.Group, members.Group, escape.Group, diagGroup,
}

var (
	commandIndex map[string]cmd.Command
	commandNames []string
	commandSpecs []commandSpec
)

func init() {
	commandIndex = indexCommands(groups)
	commandNames = commandNamesOf(groups)
	commandSpecs = commandSpecsOf(groups)
}

// commandSpec is help --json's shape: the registry as a machine reads it,
// so per-command invocation syntax does not survive only as prose.
type commandSpec struct {
	Group   string   `json:"group"`
	Name    string   `json:"name"`
	Form    string   `json:"form"`
	Desc    string   `json:"desc,omitempty"`
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
				Name:    c.Name,
				Form:    c.Form,
				Desc:    c.Desc,
				Aliases: c.Aliases,
			})
		}
	}
	return out
}
