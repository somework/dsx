package cli

import (
	"context"

	"github.com/somework/dsx/internal/mcp"
)

var plansGroup = group{
	Title: "PLANS / PREVIEW",
	Cmds: []command{
		{Name: "plan", Form: "plan <project> [--writes a,b] [--deletes c,d] [--scope project]", Run: cmdPlan},
		{Name: "preview", Form: "preview <project> <path> [--render] [--validators a,b]", Run: cmdPreview},
		{Name: "support-js", Form: "support-js <project> [--path p]", Run: cmdSupportJS},
	},
}

func cmdPlan(ctx context.Context, c *mcp.Client, args []string) error {
	flags := newFlagSet("plan")
	var (
		writes  = flags.String("writes", "", "comma-separated paths to authorise for writing")
		deletes = flags.String("deletes", "", "comma-separated paths to authorise for deletion")
		scope   = flags.String("scope", "", `"paths" (default) or "project"`)
		asJSON  = jsonFlag(flags)
	)
	pos, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	project, _, err := need1(pos, "plan <project> [--writes a,b] [--deletes c,d] [--scope project]")
	if err != nil {
		return err
	}
	a := map[string]any{"project_id": project}
	if v := splitList(*writes); len(v) > 0 {
		a["writes"] = v
	}
	if v := splitList(*deletes); len(v) > 0 {
		a["deletes"] = v
	}
	if *scope != "" {
		a["scope"] = *scope
	}
	return emit(ctx, c, "finalize_plan", a, *asJSON)
}

func cmdPreview(ctx context.Context, c *mcp.Client, args []string) error {
	flags := newFlagSet("preview")
	var (
		render     = flags.Bool("render", false, "render the preview")
		validators = flags.String("validators", "", "comma-separated validators")
		asJSON     = jsonFlag(flags)
	)
	pos, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	project, path, _, err := need2(pos, "preview <project> <path>")
	if err != nil {
		return err
	}
	a := map[string]any{"project_id": project, "path": path}
	if *render {
		a["render"] = true
	}
	if v := splitList(*validators); len(v) > 0 {
		a["validators"] = v
	}
	return emit(ctx, c, "render_preview", a, *asJSON)
}

// defaultSupportJS is where create_support_js writes when `path` is omitted.
// reference/mcp-tools.json: "defaults to \"support.js\" at the project root".

// defaultSupportJS is where create_support_js writes when `path` is omitted.
// reference/mcp-tools.json: "defaults to \"support.js\" at the project root".
const defaultSupportJS = "support.js"

func cmdSupportJS(ctx context.Context, c *mcp.Client, args []string) error {
	flags := newFlagSet("support-js")
	var (
		path    = flags.String("path", "", "destination path")
		ifMatch = flags.String("if-match", "", "etag guard")
		plan    = flags.String("plan", "", "plan_token")
		asJSON  = jsonFlag(flags)
	)
	pos, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	project, _, err := need1(pos, "support-js <project> [--path p]")
	if err != nil {
		return err
	}
	a := map[string]any{"project_id": project}
	for k, v := range map[string]string{"path": *path, "if_match": *ifMatch, "plan_token": *plan} {
		if v != "" {
			a[k] = v
		}
	}
	// The server's own schema says path "defaults to support.js at the project
	// root", so there is always something to name in a plan. Believing otherwise
	// left the documented form -- `dsx support-js <project>` -- unable to
	// self-authorise, and it exited 1 on a project with no standing grant, which
	// is the default.
	dest := *path
	if dest == "" {
		dest = defaultSupportJS
	}
	return emitWrite(ctx, c, "create_support_js", a, project, []string{dest}, *asJSON)
}
