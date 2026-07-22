package plans

import (
	"context"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/mcp"
)

var Group = cmd.Group{
	Title: "PLANS",
	Noun:  "plan",
	Desc:  "authorise writes before making them",
	Cmds: []cmd.Command{
		{Name: "plan new", Form: "plan new <project> [--writes a,b] [--deletes c,d] [--scope project]", Desc: "mint a plan_token", Run: cmdPlan},
	},
}

func cmdPlan(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("plan new")
	var (
		writes = flags.String("writes", "", "paths to authorise for writing, comma-separated;\n"+
			"\ta path containing a comma cannot be expressed — use --scope project")
		deletes = flags.String("deletes", "", "paths to authorise for deletion, comma-separated;\n"+
			"\ta path containing a comma cannot be expressed, and there is no workaround here")
		scope  = flags.String("scope", "", `"paths" (default) or "project"`)
		asJSON = cmd.JSONFlag(flags)
	)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	project, rest, err := cmd.Need1(pos, "plan new <project> [--writes a,b] [--deletes c,d] [--scope project]")
	if err != nil {
		return err
	}
	if err := cmd.NoExtra(rest, "plan new <project> [--writes a,b] [--deletes c,d] [--scope project]"); err != nil {
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
