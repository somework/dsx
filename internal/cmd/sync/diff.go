package synccmd

import (
	"context"
	"fmt"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/mcp"
	"github.com/somework/dsx/internal/syncer"
)

const diffForm = "diff [<dir>] [--out <dir>] [-j N]"

// cmdDiff classifies each path as same, local-only, remote-only or differs —
// see syncer.Diff's doc comment for the download-skipping rule and why it
// never relies on the baseline. It prints no hunks: dsx exists so bytes do
// not pass through a model's context.
func cmdDiff(ctx context.Context, c *mcp.Client, args []string) error {
	fs := cmd.NewFlagSet("diff")
	var (
		out    = fs.String("out", "", "materialise the remote side of differing paths into this (empty) directory")
		jobs   = fs.Int("j", 8, "concurrency")
		asJSON = cmd.JSONFlag(fs)
	)
	pos, err := cmd.ParseArgs(fs, args)
	if err != nil {
		return err
	}
	project, dir, err := resolveSyncTarget("diff", pos, boundProject)
	if err != nil {
		return err
	}
	if err := refuseMissingDir(dir); err != nil {
		return err
	}

	rep, err := syncer.Diff(ctx, c, syncer.DiffOpts{
		ProjectID: project, Dir: dir, Out: *out, Concurrency: *jobs, Progress: cmd.Progress,
	})
	if err != nil {
		rep.Incomplete = true
		fmt.Println(rep.Render(*asJSON))
		return err
	}
	fmt.Println(rep.Render(*asJSON))
	return nil
}
