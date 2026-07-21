package syncer

import (
	"fmt"
	"os"

	"github.com/somework/dsx/internal/dsxerr"
)

// UnpinOpts releases a directory's binding. Like PinOpts it names no client:
// unpin makes no round trip either, and the type it accepts says so.
type UnpinOpts struct {
	Dir string
}

// Unpin removes the ledger of a directory that has synced nothing. It is the
// escape pin does not offer: pin refuses to rebind, so without this a mistyped
// project id would be repairable only by deleting .dsx by hand.
//
// It may only ever release a binding that tracks no files. Invariant 13 says
// neither bind guard's refusal may suggest deleting the ledger, because
// clearing one makes every path untracked and planPush then leaves IfMatch
// empty under --force. Unpin IS that deletion, so the empty-ledger check is
// not a convenience; it is the whole boundary, and it is the same line Pin
// draws from the other side.
//
// Every refusal runs before the removal, and both reads are read-only, so a
// refused unpin leaves no trace (invariant 16). checkLedgerHome is first for a
// reason the create path does not have: it refuses a symlinked .dsx, and where
// MkdirAll through one would merely create elsewhere, os.Remove through one
// deletes a ledger outside the tree.
//
// The fetch baseline is deliberately left where it is. It carries its own
// (project, endpoint) binding and Baseline.bound refuses to answer for another
// project, so a baseline stranded by an unpin costs a re-fetch at worst, and
// stays valid if the original project is pinned back.
func Unpin(o UnpinOpts) error {
	if err := checkLedgerHome(o.Dir); err != nil {
		return err
	}
	st, err := LoadState(o.Dir)
	if err != nil {
		return err
	}
	if st.ProjectID == "" {
		return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
			"%s is not bound to a project, so there is nothing to unpin", o.Dir)}
	}
	if n := len(st.Files); n != 0 {
		return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
			"%s tracks %d files — unpin only releases a binding that has synced nothing, "+
				"because dropping the ledger makes every one of them untracked and the next "+
				"`push --force` would write with no etag precondition at all",
			StatePath(o.Dir), n)}
	}
	return os.Remove(StatePath(o.Dir))
}
