// Package synccmd is named synccmd, not sync: a package named sync beside the
// stdlib's `import "sync"` compiles while silently shadowing it.
package synccmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/mcp"
	"github.com/somework/dsx/internal/syncer"
)

var Group = cmd.Group{
	Title: "SYNC (etag-aware; unchanged files cost no request at all)",
	Note: `  The project id is optional once <dir> holds a ledger; <dir> defaults to "."
  .dsxignore excludes paths from the sync, in both directions.`,
	Cmds: []cmd.Command{
		{Name: "pull", Form: "pull  [<project>] [<dir>] [--prune] [--force] [-n] [-j N]",
			Run: syncMode("pull")},
		{Name: "push", Form: "push  [<project>] [<dir>] [--prune] [--force] [-n] [-j N]",
			Run: syncMode("push")},
		{Name: "status", Form: "status [<project>] [<dir>]",
			Desc: "what a sync would do; transfers nothing",
			Run:  syncMode("status")},
	},
}

func syncMode(mode string) func(context.Context, *mcp.Client, []string) error {
	return func(ctx context.Context, c *mcp.Client, args []string) error {
		return cmdSync(ctx, c, mode, args)
	}
}

func boundProject(dir string) (string, error) {
	st, err := syncer.LoadState(dir)
	if err != nil {
		return "", err
	}
	return st.ProjectID, nil
}

func resolveSyncTarget(mode string, pos []string, bound func(string) (string, error)) (project, dir string, err error) {
	switch len(pos) {
	case 0:
		dir = "."
	case 1:
		dir = pos[0]
	case 2:
		return pos[0], pos[1], nil
	default:
		return "", "", dsxerr.Usage(mode + " [<project>] [<dir>]")
	}

	p, err := bound(dir)
	if err != nil {
		return "", "", err
	}
	if p == "" {
		return "", "", &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
			"%s carries no dsx ledger, so its project is unknown — run `dsx %s <project> %s` once and it is remembered",
			dir, mode, dir)}
	}
	return p, dir, nil
}

func cmdSync(ctx context.Context, c *mcp.Client, mode string, args []string) error {
	fs := cmd.NewFlagSet(mode)
	var (
		prune  = fs.Bool("prune", false, "remove files absent on the other side")
		force  = fs.Bool("force", false, "overwrite conflicts")
		dry    = fs.Bool("n", false, "dry run")
		jobs   = fs.Int("j", 8, "concurrency")
		asJSON = fs.Bool("json", false, "JSON output")
		quiet  = fs.Bool("q", false, "suppress summary")
	)
	pos, err := cmd.ParseArgs(fs, args)
	if err != nil {
		return err
	}
	project, dir, err := resolveSyncTarget(mode, pos, boundProject)
	if err != nil {
		return err
	}

	dryRun := *dry || mode == "status"
	if mode != "push" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	if mode == "push" {
		rep, err := syncer.Push(ctx, c, syncer.PushOpts{
			ProjectID: project, Dir: dir, Concurrency: *jobs,
			Prune: *prune, Force: *force, DryRun: dryRun,
		})
		if err != nil {
			return err
		}
		if !*quiet {
			fmt.Println(rep.Render(*asJSON))
		}
		return syncer.ConflictOutcome(rep.Conflicts, dryRun,
			"server moved ahead; `dsx pull` first, or --force")
	}

	pullRep, err := syncer.Pull(ctx, c, syncer.PullOpts{
		ProjectID: project, Dir: dir, Concurrency: *jobs,
		Prune: *prune, Force: *force, DryRun: dryRun,
	})
	if err != nil {
		return err
	}

	if mode == "pull" {
		if !*quiet {
			fmt.Println(pullRep.Render(*asJSON))
		}
		return syncer.ConflictOutcome(pullRep.Conflicts, dryRun,
			"local differs from the server, or was deleted there and edited here")
	}

	pushRep, err := syncer.Push(ctx, c, syncer.PushOpts{
		ProjectID: project, Dir: dir, Concurrency: *jobs,
		Prune: *prune, Force: *force, DryRun: true,
	})
	if err != nil {
		return err
	}
	if *quiet {
		return nil
	}
	if *asJSON {
		b, err := json.Marshal(map[string]any{"pull": pullRep, "push": pushRep})
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	fmt.Println("pull: " + pullRep.Render(false))
	fmt.Println("push: " + pushRep.Render(false))
	return nil
}
