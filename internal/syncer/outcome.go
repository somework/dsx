package syncer

import (
	"fmt"
	"strings"

	"github.com/somework/dsx/internal/dsxerr"
)

func ConflictOutcome(conflicts []string, dryRun bool, hint string) error {
	if dryRun || len(conflicts) == 0 {
		return nil
	}
	return dsxerr.Conflict(conflicts, hint)
}

// except returns the members of all not present in any of the excluded sets,
// order preserved.
func except(all []string, excluded ...[]string) []string {
	drop := map[string]bool{}
	for _, set := range excluded {
		for _, p := range set {
			drop[p] = true
		}
	}
	out := make([]string, 0, len(all))
	for _, p := range all {
		if !drop[p] {
			out = append(out, p)
		}
	}
	return out
}

// Outcome is the conflict error the command emits. It keeps each conflict class
// distinct in the machine-facing hint so an agent is never told to do the
// impossible: a binary conflict cannot be pulled (read_file serves text only),
// and a pull prune-conflict resolved with --force DELETES the only copy.
func (r PushReport) Outcome(dryRun bool) error {
	if dryRun || len(r.Conflicts) == 0 {
		return nil
	}

	plain := except(r.Conflicts, r.BinaryConflicts, r.PruneConflicts)

	var parts []string
	if len(plain) > 0 {
		parts = append(parts, "server moved ahead; `dsx pull` first, or --force")
	}
	if len(r.BinaryConflicts) > 0 {
		parts = append(parts, fmt.Sprintf(
			"binary conflicts (%s) cannot be pulled — dsx cannot read the server's copy to merge; "+
				"--force overwrites it and the only copy is gone",
			strings.Join(r.BinaryConflicts, ", ")))
	}
	if len(r.PruneConflicts) > 0 {
		parts = append(parts, fmt.Sprintf(
			"prune conflicts (%s) moved ahead on the server; --force DELETES the server's newer copy",
			strings.Join(r.PruneConflicts, ", ")))
	}
	return dsxerr.Conflict(r.Conflicts, strings.Join(parts, "; "))
}

func (r PullReport) Outcome(dryRun bool) error {
	if dryRun || len(r.Conflicts) == 0 {
		return nil
	}

	plain := except(r.Conflicts, r.PruneConflicts)

	var parts []string
	if len(plain) > 0 {
		parts = append(parts, "local differs from the server; --force overwrites")
	}
	if len(r.PruneConflicts) > 0 {
		parts = append(parts, fmt.Sprintf(
			"prune conflicts (%s) were deleted on the server but edited here — --force DELETES your only copy",
			strings.Join(r.PruneConflicts, ", ")))
	}
	return dsxerr.Conflict(r.Conflicts, strings.Join(parts, "; "))
}
