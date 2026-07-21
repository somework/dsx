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
	Note: `  Only clone and pin name a project; every other verb reads it from <dir>'s
  ledger, and <dir> defaults to ".". unpin releases a binding, clone starts one.
  .dsxignore excludes paths from the sync, in both directions.
  status accepts pull/push's flags and previews them: --force hides conflicts.
  clone is the first pull: both arguments, and <dir> must be empty.`,
	Cmds: []cmd.Command{
		{Name: "clone", Form: cloneForm,
			Desc: "first pull into a new directory", Run: cmdClone},
		{Name: "pull", Form: "pull  [<dir>] [--prune] [--force] [-n] [-j N]",
			Run: syncMode("pull")},
		{Name: "push", Form: "push  [<dir>] [--prune] [--force] [-n] [-j N]",
			Run: syncMode("push")},
		{Name: "status", Form: "status [<dir>]",
			Desc: "what a sync would do; transfers nothing",
			Run:  syncMode("status")},
		{Name: "fetch", Form: fetchForm,
			Desc: "record what the server holds; writes .dsx/, not the tree", Run: cmdFetch},
		{Name: "pin", Form: pinForm,
			Desc: "bind an existing directory to a project; no round trip", Run: cmdPin},
		{Name: "unpin", Form: unpinForm,
			Desc: "release a binding that has synced nothing", Needs: cmd.NeedNothing,
			Run: cmd.NoClient(cmdUnpin)},
		{Name: "diff", Form: diffForm,
			Desc: "classify each path: same, local-only, remote-only, differs", Run: cmdDiff},
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
	// One positional, and it is always <dir>. A project reaches these verbs
	// only through the ledger, which only clone and pin write — so the whole
	// class of "which of my two positionals was the project" is gone, and with
	// it the mismatch arm of the project guard in Pull/Push/Fetch/Diff, which
	// is now unconstructible from the CLI rather than merely untypical. The
	// guard stays: it still answers for a library caller, and its endpoint half
	// remains reachable through DSX_ENDPOINT.
	switch len(pos) {
	case 0:
		dir = "."
	case 1:
		dir = pos[0]
	default:
		return "", "", dsxerr.Usage(mode + " [<dir>]")
	}

	// The id-shaped refusal outranks both of the others, and must: a lone
	// project id almost never names a directory that exists, so it lands on the
	// missing-path branch — whose advice is `dsx clone <project> <dir>`, i.e.
	// create the directory. Spelled with an id in the <dir> slot that is the
	// very advice that built the population of UUID-named directories, one
	// layer up from where uuidasdir_test.go first caught it.
	if looksLikeProjectID(dir) {
		if p, err := bound(dir); err == nil && p != "" {
			return p, dir, nil // a real, bound directory that happens to be so named
		}
		return "", "", &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
			"%s looks like a project id, not a directory — %s takes only <dir>; "+
				"run `dsx pin %s <dir>` to bind an existing directory, or "+
				"`dsx clone %s <dir>` to start a new one",
			dir, mode, dir, dir)}
	}

	// The directory is checked before its ledger is read, and for every verb
	// rather than only the dry runs. A path that is not there has no ledger, so
	// without this the refusal below would tell the caller to `dsx pin` a
	// directory that does not exist — the accurate message displaced by a
	// misleading one, exactly the shape invariant 16 was written against.
	// Nothing is lost by hoisting it: a directory holding a ledger exists by
	// construction, so the check can only ever fire on the typo it names.
	if err := refuseMissingDir(dir); err != nil {
		return "", "", err
	}

	p, err := bound(dir)
	if err != nil {
		return "", "", err
	}
	if p == "" {
		// Naming the verb the caller typed would be worse than useless now:
		// re-running it with a project in front is exactly what no longer
		// parses. The two repairs that do are the two verbs that bind.
		return "", "", &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
			"%s carries no dsx ledger, so its project is unknown — run "+
				"`dsx pin <project> %s` to bind it, or `dsx clone <project> <dir>` "+
				"to start a fresh directory",
			dir, dir)}
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

// refuseMissingDir refuses a directory that does not exist, before anything
// round-trips: invariant 16 — "the directory is not there" must never reach a
// plan, because an empty local scan is what makes push --prune read the whole
// server tree as user deletions. Reached from resolveSyncTarget (so from every
// sync verb, not only the dry runs) and from pin and unpin, which resolve no
// project and would otherwise have nothing catch a typo at all.
//
// It names clone, not pull: creating the directory was pull's job only while
// pull could be handed a project, and it no longer can be.
func refuseMissingDir(dir string) error {
	if _, err := os.Stat(dir); err != nil {
		return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
			"%s does not exist — name a directory that does, or run "+
				"`dsx clone <project> %s` to create it", dir, dir)}
	}
	return nil
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

	// No MkdirAll, and no dry-run-only missing-directory check: resolveSyncTarget
	// now refuses a directory that is not there, for every mode. A real pull used
	// to create its target, which was reachable only by naming the project as a
	// second positional — with that gone, a directory pull could create is a
	// directory pull cannot resolve a project for. Starting a new directory is
	// clone's job and says so in the Note. What the old dry-run branch protected
	// still holds, one layer up: "the directory is not there" never reaches a
	// plan, so an empty local scan can never make `push --prune` read the whole
	// server tree as user deletions.
	dryRun := *dry || mode == "status"

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
