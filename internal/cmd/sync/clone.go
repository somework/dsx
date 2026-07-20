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

const cloneForm = "clone <project> <dir> [-j N]"

// cmdClone is the first pull, named. It is a thin wrapper over syncer.Pull
// rather than its own act, which is what earns it invariant 12's reporting
// (Fetched appended only after a write returned nil) and invariant 5's
// error-path saves (an interrupted clone leaves a valid pin) for free.
//
// It cannot promise all-or-nothing: pull saves the ledger on both failure
// exits because invariant 5 requires it, so an interrupted clone leaves the
// files it managed and a live ledger. What clone does offer is the word, a
// first run whose vocabulary contains no --force, and refusals that land
// before the round trip.
func cmdClone(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("clone")
	var (
		jobs   = flags.Int("j", 8, "concurrency")
		asJSON = cmd.JSONFlag(flags)
	)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	project, dir, rest, err := cmd.Need2(pos, cloneForm)
	if err != nil {
		return err
	}
	if err := cmd.NoExtra(rest, cloneForm); err != nil {
		return err
	}
	if err := checkCloneTarget(dir); err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	rep, err := syncer.Pull(ctx, c, syncer.PullOpts{
		ProjectID: project, Dir: dir, Concurrency: *jobs, Progress: cmd.Progress,
	})
	if err != nil {
		rep.Incomplete = true
		fmt.Println(rep.Render(*asJSON))
		return err
	}
	fmt.Println(rep.Render(*asJSON))
	return rep.Outcome(false)
}

// checkCloneTarget refuses before any round trip. Every arm is KindUsage: none
// of them is a failure of the server or the network.
func checkCloneTarget(dir string) error {
	// The shape check gates <dir> and never <project>: pull accepts any project
	// string, and PROTOCOL.md documents the id's shape as measured rather than
	// promised, so a hand-written predicate must not decide what counts as a
	// project. Here it decides only that dsx will not create a directory named
	// like an id — which is the judgement, not a claim about the argument.
	if looksLikeProjectID(dir) {
		return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
			"%s looks like a project id, not a directory — clone takes `%s` in that "+
				"order; `dsx projects` lists the ids", dir, cloneForm)}
	}

	// Lstat, not Stat: a dangling symlink fails Stat and would slip past every
	// check below to die inside the walk with a raw filesystem error.
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil // does not exist yet: the ordinary case
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
			"%s is a symlink — clone needs a real directory, because a link resolves "+
				"elsewhere and the whole project would land where you did not name", dir)}
	}
	if !fi.IsDir() {
		return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
			"%s is not a directory", dir)}
	}

	// The ledger is its own probe: it is a builtin ignore, so the emptiness
	// check below cannot see it and a clone into a fully synced directory would
	// otherwise look like a clone into an empty one.
	st, err := syncer.LoadState(dir)
	if err != nil {
		return err
	}
	if st.ProjectID != "" {
		return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
			"%s already holds a dsx ledger for project %s — run `dsx pull %s` to update it",
			dir, st.ProjectID, dir)}
	}

	// .dsx is a builtin ignore, so LocalIsEmpty (which asks through survey)
	// cannot see a foreign .dsx directory and would read it as empty. A raw
	// Lstat, never survey-mediated, is the only thing that can catch it.
	if _, err := os.Lstat(filepath.Join(dir, ".dsx")); err == nil {
		return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
			"%s already holds a .dsx directory — clone starts a new directory; "+
				"name an empty one", dir)}
	}

	empty, err := syncer.LocalIsEmpty(dir)
	if err != nil {
		return err
	}
	if !empty {
		// Deliberately not "run `dsx pull` instead": that pull reports every
		// colliding path as a conflict and writes nothing, so naming it here
		// would send the reader to a second refusal. A first-contact refusal
		// teaches the first-contact move.
		return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
			"%s is not empty — clone starts a new directory; name an empty one", dir)}
	}
	return nil
}
