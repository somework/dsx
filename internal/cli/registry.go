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

	sync "github.com/somework/dsx/internal/cmd/sync"
)

var groups = []cmd.Group{
	sync.Group, projects.Group, files.Group, plans.Group,
	conv.Group, members.Group, escape.Group, diagGroup,
}

var (
	commandIndex map[string]cmd.Command
	commandNames []string
)

func init() {
	commandIndex = indexCommands(groups)
	commandNames = commandNamesOf(groups)
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
