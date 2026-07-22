package projects

import (
	"context"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/mcp"
)

const defaultSupportJS = "support.js"

func cmdSupportJS(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("project support-js")
	var (
		path    = flags.String("path", "", "destination path")
		ifMatch = flags.String("if-match", "", "etag guard")
		plan    = flags.String("plan", "", "plan_token")
		asJSON  = cmd.JSONFlag(flags)
	)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	project, rest, err := cmd.Need1(pos, "project support-js <project> [--path p]")
	if err != nil {
		return err
	}
	if err := cmd.NoExtra(rest, "project support-js <project> [--path p]"); err != nil {
		return err
	}
	a := map[string]any{"project_id": project}
	for k, v := range map[string]string{"path": *path, "if_match": *ifMatch, "plan_token": *plan} {
		if v != "" {
			a[k] = v
		}
	}

	dest := *path
	if dest == "" {
		dest = defaultSupportJS
	}
	return cmd.EmitWrite(ctx, c, "create_support_js", a, project, []string{dest}, *asJSON)
}
