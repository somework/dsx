package synccmd

import (
	"context"
	"fmt"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/mcp"
	"github.com/somework/dsx/internal/syncer"
)

const fetchForm = "fetch [-j N]"

// cmdFetch records what the server holds without touching the tree — see
// syncer.Fetch's doc comment for the narrow-set rationale and the wholesale
// rewrite it performs on .dsx/baseline.json.
func cmdFetch(ctx context.Context, c *mcp.Client, args []string) error {
	fs := cmd.NewFlagSet("fetch")
	var (
		jobs   = fs.Int("j", 8, "concurrency")
		asJSON = cmd.JSONFlag(fs)
	)
	pos, err := cmd.ParseArgs(fs, args)
	if err != nil {
		return err
	}
	project, dir, err := resolveSyncTarget("fetch", pos, boundProject)
	if err != nil {
		return err
	}
	if err := refuseMissingDir(dir); err != nil {
		return err
	}

	rep, err := syncer.Fetch(ctx, c, syncer.FetchOpts{
		ProjectID: project, Dir: dir, Concurrency: *jobs, Progress: cmd.Progress,
	})
	if err != nil {
		rep.Incomplete = true
		fmt.Println(rep.Render(*asJSON))
		return err
	}
	fmt.Println(rep.Render(*asJSON))
	return nil
}
