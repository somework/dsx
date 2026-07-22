package ds

import (
	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/reply"
)

// Design systems are not a project's property: list_design_systems takes no
// project_id at all, so putting the verb under the project noun would promise
// a filter the tool has no argument for.
var Group = cmd.Group{
	Title: "DESIGN SYSTEMS",
	Noun:  "ds",
	Desc:  "design systems available to you",
	Cmds: []cmd.Command{
		{Name: "ds ls", Form: "ds ls", Desc: "list design systems",
			Tool: func(pos []string) (string, map[string]any, error) {
				if err := cmd.NoExtra(pos, "ds ls"); err != nil {
					return "", nil, err
				}
				return "list_design_systems", map[string]any{}, nil
			},
			Human: reply.DesignSystems},
	},
}
