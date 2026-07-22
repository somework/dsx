package synccmd

import (
	"fmt"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/syncer"
)

const statusForm = "status"

// cmdStatus reports from disk alone. It is the second sync verb declared
// NeedNothing, and for a sharper reason than unpin's: status makes no network
// call at all, so asking for a credential would be asking for something it
// has no use for — and a caller whose token has expired is exactly the caller
// who wants to know what is on their disk.
//
// Its own FlagSet, named by a literal: flagSetOwners can only read a literal,
// and status no longer shares pull/push's set. It also no longer takes their
// flags — --prune, --force and -n choose what a sync would do, and status
// does nothing; -j sizes a request pool, and status makes no requests.
func cmdStatus(args []string) error {
	fs := cmd.NewFlagSet("status")
	asJSON := cmd.JSONFlag(fs)
	quiet := fs.Bool("q", false, "suppress the report")

	pos, err := cmd.ParseArgs(fs, args)
	if err != nil {
		return err
	}
	project, dir, err := resolveSyncTarget("status", pos, boundProject)
	if err != nil {
		return err
	}

	rep, err := syncer.Status(syncer.StatusOpts{ProjectID: project, Dir: dir})
	if err != nil {
		return err
	}
	if *quiet {
		return nil
	}
	fmt.Println(rep.Render(*asJSON))
	return nil
}
