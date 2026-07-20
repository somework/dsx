// Package synccmd is named synccmd, not sync: a package named sync beside the
// stdlib's `import "sync"` compiles while silently shadowing it.
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

var Group = cmd.Group{
	Title: "SYNC (etag-aware; unchanged files cost no request at all)",
	Note: `  The project id is optional once <dir> holds a ledger; <dir> defaults to "."
  .dsxignore excludes paths from the sync, in both directions.
  status accepts pull/push's flags and previews them: --force hides conflicts.
  clone is the first pull: both arguments, and <dir> must be empty.`,
	Cmds: []cmd.Command{
		{Name: "clone", Form: cloneForm,
			Desc: "first pull into a new directory", Run: cmdClone},
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
		if looksLikeProjectID(dir) {
			return "", "", &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
				"%s looks like a project id, not a directory — a lone positional is <dir>, "+
					"so run `dsx %s %s <dir>` and name where the files should live",
				dir, mode, dir)}
		}
		return "", "", &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
			"%s carries no dsx ledger, so its project is unknown — run `dsx %s <project> %s` once and it is remembered",
			dir, mode, dir)}
	}
	return p, dir, nil
}

// looksLikeProjectID chooses which refusal to print, never what to do: both
// branches refuse and neither rebinds a positional, so a wrong guess costs the
// reader the other message and nothing else.
func looksLikeProjectID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}

func cmdSync(ctx context.Context, c *mcp.Client, mode string, args []string) error {
	// Three literals, not cmd.NewFlagSet(mode): flagSetOwners can only read a
	// literal, and an expression makes it fall back to attributing these flags
	// to every command the package declares.
	var fs *flag.FlagSet
	switch mode {
	case "pull":
		fs = cmd.NewFlagSet("pull")
	case "push":
		fs = cmd.NewFlagSet("push")
	default:
		fs = cmd.NewFlagSet("status")
	}
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
	switch {
	case dryRun:
		// A command that transfers nothing leaves no trace. Refusing rather
		// than tolerating a missing directory in syncer is deliberate: an empty
		// local scan is what makes `push --prune` read the whole server tree as
		// user deletions, so "the directory is not there" must never reach a
		// plan at all.
		if _, err := os.Stat(dir); err != nil {
			return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
				"%s does not exist, so there is nothing to compare — name a synced "+
					"directory, or run `dsx pull <project> %s` to create it", dir, dir)}
		}
	case mode != "push":
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	if mode == "push" {
		emit := func(r syncer.PushReport) {
			if !*quiet {
				fmt.Println(r.Render(*asJSON))
			}
		}
		rep, err := syncer.Push(ctx, c, syncer.PushOpts{
			ProjectID: project, Dir: dir, Concurrency: *jobs,
			Prune: *prune, Force: *force, DryRun: dryRun, Progress: cmd.Progress,
		})
		if err != nil {
			rep.Incomplete = true
			emit(rep)
			return err
		}
		emit(rep)
		return rep.Outcome(dryRun)
	}

	emit := func(r syncer.PullReport) {
		if !*quiet {
			fmt.Println(r.Render(*asJSON))
		}
	}
	pullRep, err := syncer.Pull(ctx, c, syncer.PullOpts{
		ProjectID: project, Dir: dir, Concurrency: *jobs,
		Prune: *prune, Force: *force, DryRun: dryRun, Progress: cmd.Progress,
	})
	if err != nil {
		// status is the two-key {pull,push} envelope; rendering one half alone
		// breaks it here for exactly the reason spelled out at the push half's
		// error return below.
		if mode != "status" {
			pullRep.Incomplete = true
			emit(pullRep)
		}
		return err
	}

	if mode == "pull" {
		emit(pullRep)
		return pullRep.Outcome(dryRun)
	}

	pushRep, err := syncer.Push(ctx, c, syncer.PushOpts{
		ProjectID: project, Dir: dir, Concurrency: *jobs,
		Prune: *prune, Force: *force, DryRun: true,
	})
	if err != nil {
		// status is a read-only DryRun query — no bytes moved, so unlike
		// push/pull there is no single report to render here. Rendering
		// pushRep alone would violate the {pull,push} two-key JSON envelope
		// TestStatusJSONIsOneDocumentHoldingBothReports expects; bare err
		// return is deliberate, not a missed render.
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
