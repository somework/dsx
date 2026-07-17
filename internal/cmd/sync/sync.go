// Package synccmd holds the pull/push/status commands.
//
// The directory is internal/cmd/sync, but the package is synccmd, not sync:
// pull.go and tree.go over in internal/syncer use the stdlib's sync (Mutex,
// WaitGroup), and a package named sync sitting next to `import "sync"` compiles
// while quietly shadowing it — the exact collision that forced the engine to be
// internal/syncer rather than internal/sync. Naming this leaf `sync` would set
// the same trap for the first file here that reaches for a Mutex.
package synccmd

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/mcp"
	"github.com/somework/dsx/internal/syncer"
)

// Group is the SYNC section of `dsx help`.
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

// syncMode binds cmdSync's mode, which is the only thing that separates the
// three. The mode restates the command's own name because Run is handed args
// and nothing else; a signature carrying the name would be paid for by the
// twenty-eight commands that do not want it.
func syncMode(mode string) func(context.Context, *mcp.Client, []string) error {
	return func(ctx context.Context, c *mcp.Client, args []string) error {
		return cmdSync(ctx, c, mode, args)
	}
}

// boundProject reports the project a directory is already pinned to, or "" if
// the directory carries no ledger yet.
func boundProject(dir string) (string, error) {
	st, err := syncer.LoadState(dir)
	if err != nil {
		return "", err
	}
	return st.ProjectID, nil
}

// resolveSyncTarget works out which project and directory a sync command means.
//
// The ledger already records the project id, so retyping a UUID on every sync
// is pure friction. Two positional arguments keep their old meaning exactly;
// fewer fall back to the ledger, which is the only place the binding is known.
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
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
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

	// `status` is the only mode that reports both directions. Neither side
	// moves bytes, so the two dry runs cannot interfere.
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
