package syncer

import (
	"github.com/somework/dsx/internal/dsxerr"
)

func ConflictOutcome(conflicts []string, dryRun bool, hint string) error {
	if dryRun || len(conflicts) == 0 {
		return nil
	}
	return dsxerr.Conflict(conflicts, hint)
}
