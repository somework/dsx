package files

import (
	"context"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/mcp"
)

func cmdPreview(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("files preview")
	// No --render. The server dropped `render` from render_preview's schema and
	// a probe confirmed it changes not one key of the reply, so the flag was
	// accepted, documented in `dsx help`, and did nothing. --validators stays
	// because it is a different case: the server still declares it, as
	// "Reserved … Ignored today" — a slot it means to honour, not one it removed.
	var (
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
	if v := cmd.SplitList(*validators); len(v) > 0 {
		a["validators"] = v
	}
	return cmd.Emit(ctx, c, "render_preview", a, *asJSON, nil)
}
