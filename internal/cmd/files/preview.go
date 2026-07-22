package files

import (
	"context"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/mcp"
)

func cmdPreview(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("files preview")
	var (
		render     = flags.Bool("render", false, "render the preview")
		validators = flags.String("validators", "", "comma-separated validators")
		asJSON     = cmd.JSONFlag(flags)
	)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	project, path, rest, err := cmd.Need2(pos, "files preview <project> <path>")
	if err != nil {
		return err
	}
	if err := cmd.NoExtra(rest, "files preview <project> <path>"); err != nil {
		return err
	}
	a := map[string]any{"project_id": project, "path": path}
	if *render {
		a["render"] = true
	}
	if v := cmd.SplitList(*validators); len(v) > 0 {
		a["validators"] = v
	}
	return cmd.Emit(ctx, c, "render_preview", a, *asJSON, nil)
}
