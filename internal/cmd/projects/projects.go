package projects

import (
	"context"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/mcp"
	"github.com/somework/dsx/internal/reply"
)

var Group = cmd.Group{
	Title: "PROJECTS",
	Noun:  "project",
	Desc:  "design projects on the server",
	Cmds: []cmd.Command{
		{Name: "project ls", Form: "project ls", Desc: "list projects", Run: cmdProjects},
		{Name: "project get", Form: "project get <id>", Desc: "project detail",
			Tool: func(pos []string) (string, map[string]any, error) {
				id, rest, err := cmd.Need1(pos, "project get <id>")
				if err != nil {
					return "", nil, err
				}
				if err := cmd.NoExtra(rest, "project get <id>"); err != nil {
					return "", nil, err
				}
				return "get_project", map[string]any{"project_id": id}, nil
			},
			Human: reply.Project},
		{Name: "project new", Form: "project new <name> [--ds <id>]", Desc: "create project",
			Run: cmdNew},
		{Name: "project support-js", Form: "project support-js <project> [--path p]", Desc: "write the Design Components runtime", Run: cmdSupportJS},
		{Name: "project sharing", Form: "project sharing <project> [--scope s] [--link-permission p]", Desc: "scope and link permission", Run: cmdSharing},
	},
}

func cmdProjects(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("project ls")
	asJSON := cmd.JSONFlag(flags)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	if err := cmd.NoExtra(pos, "project ls"); err != nil {
		return err
	}
	return cmd.Emit(ctx, c, "list_projects", map[string]any{}, *asJSON, reply.Projects)
}

func cmdNew(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("project new")
	ds := flags.String("ds", "", "design system id to attach")
	asJSON := cmd.JSONFlag(flags)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	name, rest, err := cmd.Need1(pos, "project new <name> [--ds <id>]")
	if err != nil {
		return err
	}
	if err := cmd.NoExtra(rest, "project new <name> [--ds <id>]"); err != nil {
		return err
	}
	a := map[string]any{"name": name}
	if *ds != "" {
		a["design_system_id"] = *ds
	}
	return cmd.Emit(ctx, c, "create_project", a, *asJSON, nil)
}
