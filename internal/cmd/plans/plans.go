package plans

import (
	"context"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/mcp"
)

var Group = cmd.Group{
	Title: "PLANS / PREVIEW",
	Cmds: []cmd.Command{
		{Name: "plan", Form: "plan <project> [--writes a,b] [--deletes c,d] [--scope project]", Run: cmdPlan},
		{Name: "preview", Form: "preview <project> <path> [--render] [--validators a,b]", Run: cmdPreview},
		{Name: "support-js", Form: "support-js <project> [--path p]", Run: cmdSupportJS},
	},
}

func cmdPlan(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("plan")
	var (
		writes  = flags.String("writes", "", "comma-separated paths to authorise for writing")
		deletes = flags.String("deletes", "", "comma-separated paths to authorise for deletion")
		scope   = flags.String("scope", "", `"paths" (default) or "project"`)
		asJSON  = cmd.JSONFlag(flags)
	)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	project, rest, err := cmd.Need1(pos, "plan <project> [--writes a,b] [--deletes c,d] [--scope project]")
	if err != nil {
		return err
	}
	if err := cmd.NoExtra(rest, "plan <project> [--writes a,b] [--deletes c,d] [--scope project]"); err != nil {
		return err
	}
	// A project-scoped plan already authorises every path; naming paths as well is a
	// contradiction, and one the client can settle without a round trip. Only this one
	// scope value is judged — the domain of --scope stays the server's to define.
	if *scope == "project" && (len(cmd.SplitList(*writes)) > 0 || len(cmd.SplitList(*deletes)) > 0) {
		return &dsxerr.Error{
			Kind: dsxerr.KindUsage,
			Msg:  "--scope project authorises any path; drop --writes/--deletes",
		}
	}
	a := map[string]any{"project_id": project}
	if v := cmd.SplitList(*writes); len(v) > 0 {
		a["writes"] = v
	}
	if v := cmd.SplitList(*deletes); len(v) > 0 {
		a["deletes"] = v
	}
	if *scope != "" {
		a["scope"] = *scope
	}
	return cmd.Emit(ctx, c, "finalize_plan", a, *asJSON)
}

func cmdPreview(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("preview")
	var (
		render     = flags.Bool("render", false, "render the preview")
		validators = flags.String("validators", "", "comma-separated validators")
		asJSON     = cmd.JSONFlag(flags)
	)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	project, path, rest, err := cmd.Need2(pos, "preview <project> <path>")
	if err != nil {
		return err
	}
	if err := cmd.NoExtra(rest, "preview <project> <path>"); err != nil {
		return err
	}
	a := map[string]any{"project_id": project, "path": path}
	if *render {
		a["render"] = true
	}
	if v := cmd.SplitList(*validators); len(v) > 0 {
		a["validators"] = v
	}
	return cmd.Emit(ctx, c, "render_preview", a, *asJSON)
}

const defaultSupportJS = "support.js"

func cmdSupportJS(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("support-js")
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
	project, rest, err := cmd.Need1(pos, "support-js <project> [--path p]")
	if err != nil {
		return err
	}
	if err := cmd.NoExtra(rest, "support-js <project> [--path p]"); err != nil {
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
