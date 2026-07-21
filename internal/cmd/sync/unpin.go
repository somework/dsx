package synccmd

import (
	"encoding/json"
	"fmt"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/syncer"
)

const unpinForm = "unpin [<dir>]"

// cmdUnpin releases a directory bound to a project. It is the only sync verb
// declared NeedNothing: pin holds a client solely for c.Endpoint(), which
// unpin has nothing to record and nothing to compare against, so unpin asks
// for no credential at all — and therefore still works when the stored token
// has expired, which is one of the states a caller may be in when they want
// out of a binding.
func cmdUnpin(args []string) error {
	fs := cmd.NewFlagSet("unpin")
	asJSON := cmd.JSONFlag(fs)
	pos, err := cmd.ParseArgs(fs, args)
	if err != nil {
		return err
	}
	dir := "."
	switch len(pos) {
	case 0:
	case 1:
		dir = pos[0]
	default:
		return dsxerr.Usage(unpinForm)
	}

	// refuseMissingDir before syncer.Unpin, for the reason pin and fetch both
	// have it: a typo must be named as a typo, not reported as a refusal about
	// a directory that was never there (invariant 16).
	if err := refuseMissingDir(dir); err != nil {
		return err
	}

	if err := syncer.Unpin(syncer.UnpinOpts{Dir: dir}); err != nil {
		return err
	}
	if *asJSON {
		b, err := json.Marshal(struct {
			Dir string `json:"dir"`
		}{Dir: dir})
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	fmt.Println("unpinned " + dir)
	return nil
}
