package syncer

import (
	"fmt"

	"github.com/somework/dsx/internal/dsxerr"
)

// PinOpts binds an existing directory to a project. Endpoint is a plain
// string, not an *mcp.Client: pin makes no round trip, so nothing about it
// may ever depend on a server reply, and the type it accepts says so.
type PinOpts struct {
	ProjectID string
	Endpoint  string
	Dir       string
}

// Pin writes an empty ledger binding Dir to ProjectID. Every refusal below
// runs before the write that follows it: checkLedgerHome and LoadState are
// both read-only, and the len(Files)/project/endpoint checks read only what
// LoadState already returned. Pin does not check that Dir exists — that is
// the caller's job (refuseMissingDir in the cmd layer), the same split fetch
// already uses, because checkLedgerHome and save's ensureLedgerHome would
// otherwise silently MkdirAll a typo into a real directory.
func Pin(o PinOpts) error {
	if err := checkLedgerHome(o.Dir); err != nil {
		return err
	}
	st, err := LoadState(o.Dir)
	if err != nil {
		return err
	}
	if len(st.Files) != 0 {
		return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
			"%s already tracks %d files — pin only binds a directory whose ledger is empty; "+
				"run `dsx status %s` to see what it holds", StatePath(o.Dir), len(st.Files), o.Dir)}
	}
	if st.ProjectID != "" && st.ProjectID != o.ProjectID {
		return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
			"%s is bound to project %s; refusing to pin %s to it",
			StatePath(o.Dir), st.ProjectID, o.ProjectID)}
	}
	if st.Endpoint != "" && !sameEndpoint(st.Endpoint, o.Endpoint) {
		return endpointRefusal(o.Dir, st.Endpoint, o.Endpoint, "pin")
	}

	newState := State{ProjectID: o.ProjectID, Endpoint: o.Endpoint, Files: map[string]FileState{}}
	return newState.save(o.Dir)
}
