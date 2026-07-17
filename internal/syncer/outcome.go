package syncer

import (
	"github.com/somework/dsx/internal/dsxerr"
)

// ConflictOutcome turns reported conflicts into the exit status.
//
// A dry run was asked to move nothing, so refusing to move something is the
// answer it wanted, not a failure. A real run that refused did not do what it
// was told, and a caller that reads exit 0 there would carry on over the top of
// work that exists nowhere else.
func ConflictOutcome(conflicts []string, dryRun bool, hint string) error {
	if dryRun || len(conflicts) == 0 {
		return nil
	}
	return dsxerr.Conflict(conflicts, hint)
}
