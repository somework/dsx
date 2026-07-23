// Package synccmd is named synccmd, not sync: a package named sync beside the
// stdlib's `import "sync"` compiles while silently shadowing it.
package synccmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/mcp"
	"github.com/somework/dsx/internal/syncer"
)

var Group = cmd.Group{
	Title: "SYNC (etag-aware; unchanged files cost no request at all)",
	Note: `  Only clone and pin name a project; unpin may name a directory. Every other verb
  acts on the tree it stands in, finding its ledger by walking up; dsx -C <dir> moves first.
  .dsxignore excludes paths from the sync, in both directions.
  status answers from disk alone and makes no network call: it reads the ledger
  against your files, and the last dsx fetch against both. Use pull -n or push -n
  to ask the server what a sync would do right now.
  clone is the first pull: both arguments, <dir> must be empty, and it fetches
  binaries too — pull needs --binary for those. For a text-only tree: pin, then pull.`,
	Cmds: []cmd.Command{
		{Name: "clone", Form: cloneForm,
			Desc: "first pull into a new directory", Run: cmdClone},
		{Name: "pull", Form: "pull  [--prune] [--force] [--binary] [-n]",
			Run: cmdPull},
		{Name: "push", Form: "push  [--prune] [--force | --force-with-lease] [-n]",
			Run: cmdPush},
		{Name: "status", Form: statusForm,
			Desc:  "what changed here, from disk alone; makes no network call",
			Needs: cmd.NeedNothing, Run: cmd.NoClient(cmdStatus)},
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

// boundProject answers which project a directory syncs to and which directory
// is actually the root of that sync — the way `git status` answers from
// anywhere inside a repository rather than only at the top.
//
// The caller's own spelling is kept whenever it already names the root:
// FindRoot works in absolute paths, and every refusal below prints this
// string, so rewriting `design` into `/Users/…/design` on the common path
// would be a cosmetic regression paid on every message.
func boundProject(dir string) (project, root string, err error) {
	root, err = syncer.FindRoot(dir)
	if err != nil || root == "" {
		return "", "", err
	}
	if abs, aErr := filepath.Abs(dir); aErr == nil && abs == root {
		root = dir
	}
	st, err := syncer.LoadState(root)
	if err != nil {
		return "", "", err
	}
	return st.ProjectID, root, nil
}

func resolveSyncTarget(mode string, pos []string, bound func(string) (string, string, error)) (project, dir string, err error) {
	// No positional at all. Inside a synced tree the ledger is found by walking
	// up; outside one, `dsx -C <dir>` is the way in. Between them nothing is
	// left for an argument to say, and dropping it takes the last ambiguity out
	// of this function: there is no arity at which a token could have been the
	// project instead of the directory. The mismatch arm of the project guard
	// in Pull/Push/Fetch/Diff stays unconstructible from the CLI for the same
	// reason it already was — a project reaches them only through the ledger it
	// is compared against.
	//
	// A positional is therefore always a mistake, and which mistake it is
	// decides which repair to name. An id-shaped one is the old habit and must
	// not be answered with advice that would create a directory named after a
	// project id, which is the defect uuidasdir_test.go has now caught twice.
	if len(pos) > 0 {
		if looksLikeProjectID(pos[0]) {
			return "", "", &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
				"%s looks like a project id — %s takes no arguments; run "+
					"`dsx pin %s <dir>` to bind a directory to it, or "+
					"`dsx clone %s <dir>` to start a new one",
				pos[0], mode, pos[0], pos[0])}
		}
		return "", "", &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
			"%s takes no arguments — it acts on the tree you are standing in; "+
				"run `dsx -C %s %s` to act on that one instead",
			mode, pos[0], mode)}
	}

	dir = "."
	p, root, err := bound(dir)
	if err != nil {
		return "", "", err
	}
	if p != "" {
		// The root, not the working directory: a verb acts on the tree it is
		// standing in, so a subdirectory resolves upward and the whole sync
		// happens at the top. bound keeps "." when the working directory
		// already IS the root, so the common report is unchanged.
		return p, root, nil
	}
	// "nor in any directory above" is not decoration: the search walked up, so
	// a reader standing in a subdirectory has to be told the whole chain came
	// back empty rather than left wondering whether dsx looked in the wrong
	// place. No path is printed because there is no longer one the caller
	// typed — naming "." would read as a claim about a directory rather than
	// about the search.
	return "", "", &dsxerr.Error{Kind: dsxerr.KindUsage,
		Msg: "no dsx ledger here, nor in any directory above — run " +
			"`dsx pin <project>` to bind this one, or `dsx clone <project> <dir>` " +
			"to start a fresh directory"}
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
// server tree as user deletions.
//
// Its only callers are pin and unpin, the two verbs that still take a directory
// and resolve no project — nothing else would catch a typo in it. The sync
// verbs stopped needing it when they stopped taking a directory: they act on
// the working directory, which exists by standing in it.
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

// cmdPull and cmdPush declare their flags separately, and the repetition is
// load-bearing rather than sloppy: flagSetOwners reads a NewFlagSet name only
// when it is a string literal, so a shared helper taking `fs *flag.FlagSet`
// would leave every flag unattributed and fall back to attributing it to
// every command the package declares. One cmdSync with one set had the
// mirrored problem — a flag only push accepts belonged to pull too, so
// documenting it made `dsx help` promise pull a flag the binary rejects.
// Splitting is what buys --force-with-lease a home; the earlier note that
// splitting made things worse predates the union that fixed the reader.
func cmdPull(ctx context.Context, c *mcp.Client, args []string) error {
	fs := cmd.NewFlagSet("pull")
	var (
		prune  = fs.Bool("prune", false, "remove files absent on the other side")
		force  = fs.Bool("force", false, "overwrite conflicts")
		binary = fs.Bool("binary", false, "also fetch the files read_file refuses, over the preview lane")
		dry    = fs.Bool("n", false, "dry run")
		jobs   = fs.Int("j", cmd.DefaultConcurrency, "concurrency")
		asJSON = cmd.JSONFlag(fs)
		quiet  = fs.Bool("q", false, "suppress summary")
	)
	pos, err := cmd.ParseArgs(fs, args)
	if err != nil {
		return err
	}
	project, dir, err := resolveSyncTarget("pull", pos, boundProject)
	if err != nil {
		return err
	}

	// No MkdirAll: resolveSyncTarget refuses a directory that is not there. A
	// real pull used to create its target, reachable only by naming the
	// project as a second positional — with that gone, a directory pull could
	// create is one pull cannot resolve a project for. Starting a directory is
	// clone's job. What the old branch protected still holds one layer up:
	// "not there" never reaches a plan, so an empty local scan can never make
	// `push --prune` read the whole server tree as user deletions.
	emit := func(r syncer.PullReport) {
		if !*quiet {
			fmt.Println(r.Render(*asJSON))
		}
	}
	rep, err := syncer.Pull(ctx, c, syncer.PullOpts{
		ProjectID: project, Dir: dir, Concurrency: *jobs,
		Prune: *prune, Force: *force, Binary: *binary, DryRun: *dry, Progress: cmd.Progress,
	})
	if err != nil {
		rep.Incomplete = true
		emit(rep)
		return err
	}
	emit(rep)
	return rep.Outcome()
}

func cmdPush(ctx context.Context, c *mcp.Client, args []string) error {
	fs := cmd.NewFlagSet("push")
	var (
		prune  = fs.Bool("prune", false, "remove files absent on the other side")
		force  = fs.Bool("force", false, "overwrite conflicts")
		lease  = fs.Bool("force-with-lease", false, "overwrite only what the last fetch still accounts for")
		dry    = fs.Bool("n", false, "dry run")
		jobs   = fs.Int("j", cmd.DefaultConcurrency, "concurrency")
		asJSON = cmd.JSONFlag(fs)
		quiet  = fs.Bool("q", false, "suppress summary")
	)
	pos, err := cmd.ParseArgs(fs, args)
	if err != nil {
		return err
	}
	// Refused together rather than ranked: --force sends no precondition at
	// all and --force-with-lease sends the etag the last fetch recorded, so a
	// caller who wrote both asked for two different writes, and silently
	// keeping either is a guess about which.
	if *force && *lease {
		return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: "--force and --force-with-lease ask for " +
			"different writes — --force sends no precondition at all, --force-with-lease sends the " +
			"etag the last `dsx fetch` recorded; name one"}
	}
	project, dir, err := resolveSyncTarget("push", pos, boundProject)
	if err != nil {
		return err
	}

	emit := func(r syncer.PushReport) {
		if !*quiet {
			fmt.Println(r.Render(*asJSON))
		}
	}
	rep, err := syncer.Push(ctx, c, syncer.PushOpts{
		ProjectID: project, Dir: dir, Concurrency: *jobs,
		Prune: *prune, Force: *force, Lease: *lease, DryRun: *dry, Progress: cmd.Progress,
	})
	if err != nil {
		rep.Incomplete = true
		emit(rep)
		return err
	}
	emit(rep)
	return rep.Outcome()
}
