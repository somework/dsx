package projects

import (
	"context"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/mcp"
)

var Group = cmd.Group{
	Title: "PROJECTS",
	Cmds: []cmd.Command{
		{Name: "projects", Form: "projects", Desc: "list projects",
			Tool: func(pos []string) (string, map[string]any, error) {
				if err := cmd.NoExtra(pos, "projects"); err != nil {
					return "", nil, err
				}
				return "list_projects", map[string]any{}, nil
			}},
		{Name: "project", Form: "project <id>", Desc: "project detail",
			Tool: func(pos []string) (string, map[string]any, error) {
				id, rest, err := cmd.Need1(pos, "project <id>")
				if err != nil {
					return "", nil, err
				}
				if err := cmd.NoExtra(rest, "project <id>"); err != nil {
					return "", nil, err
				}
				return "get_project", map[string]any{"project_id": id}, nil
			}},
		{Name: "new", Form: "new <name> [--ds <id>]", Desc: "create project",
			Run: cmdNew},
		{Name: "systems", Form: "systems", Desc: "list design systems",
			Tool: func(pos []string) (string, map[string]any, error) {
				if err := cmd.NoExtra(pos, "systems"); err != nil {
					return "", nil, err
				}
				return "list_design_systems", map[string]any{}, nil
			}},
	},
}

func cmdNew(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("new")
	ds := flags.String("ds", "", "design system id to attach")
	asJSON := cmd.JSONFlag(flags)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	name, rest, err := cmd.Need1(pos, "new <name> [--ds <id>]")
	if err != nil {
		return err
	}
	if err := cmd.NoExtra(rest, "new <name> [--ds <id>]"); err != nil {
		return err
	}
	a := map[string]any{"name": name}
	if *ds != "" {
		a["design_system_id"] = *ds
	}
	return cmd.Emit(ctx, c, "create_project", a, *asJSON)
}
