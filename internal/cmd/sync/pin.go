package synccmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/mcp"
	"github.com/somework/dsx/internal/syncer"
)

const pinForm = "pin <project> [<dir>]"

// cmdPin binds an existing directory to a project. It makes no round trip:
// Needs stays the NeedClient zero value solely so c.Endpoint() is available
// to record and to compare against — a pin cannot half-spend what it might
// refuse, so nothing here may ever depend on a server reply.
func cmdPin(ctx context.Context, c *mcp.Client, args []string) error {
	fs := cmd.NewFlagSet("pin")
	asJSON := cmd.JSONFlag(fs)
	pos, err := cmd.ParseArgs(fs, args)
	if err != nil {
		return err
	}
	var project, dir string
	switch len(pos) {
	case 1:
		project, dir = pos[0], "."
	case 2:
		project, dir = pos[0], pos[1]
	default:
		return dsxerr.Usage(pinForm)
	}

	// refuseMissingDir runs before syncer.Pin for the same reason cmdFetch's
	// does — checkLedgerHome and save's ensureLedgerHome would otherwise
	// MkdirAll a typo into a real directory (invariant 16).
	if err := refuseMissingDir(dir); err != nil {
		return err
	}

	if err := syncer.Pin(syncer.PinOpts{ProjectID: project, Endpoint: c.Endpoint(), Dir: dir}); err != nil {
		return err
	}
	if *asJSON {
		b, err := json.Marshal(struct {
			Project string `json:"project"`
			Dir     string `json:"dir"`
		}{Project: project, Dir: dir})
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	fmt.Println("pinned " + dir + " to " + project)
	return nil
}
