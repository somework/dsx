package projects

import (
	"context"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/mcp"
)

func cmdSharing(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("project sharing")
	var (
		scope  = flags.String("scope", "", "sharing scope")
		link   = flags.String("link-permission", "", "link permission")
		asJSON = cmd.JSONFlag(flags)
	)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	project, rest, err := cmd.Need1(pos, "project sharing <project> [--scope s] [--link-permission p]")
	if err != nil {
		return err
	}
	if err := cmd.NoExtra(rest, "project sharing <project> [--scope s] [--link-permission p]"); err != nil {
		return err
	}
	a := map[string]any{"project_id": project}
	if *scope != "" {
		a["scope"] = *scope
	}
	if *link != "" {
		a["link_permission"] = *link
	}
	return cmd.Emit(ctx, c, "update_sharing", a, *asJSON, nil)
}
