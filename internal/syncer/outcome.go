package syncer

import (
	"fmt"
	"strings"

	"github.com/somework/dsx/internal/dsxerr"
)

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
// distinct in the machine-facing hint.
//
// A dry run gets the same answer as the run it previews, and takes no parameter
// saying otherwise. Exit 3 states something about the TREE — both sides hold
// work, a human must choose — not about whether this invocation moved bytes,
// which is why a real pull under -q prints nothing and still exits 3. A dry run
// computes that fact from the same plan; suppressing it told an agent 0 and
// then met it with 3 on the very next command.
func (r PushReport) Outcome() error {
	if len(r.Conflicts) == 0 {
		return nil
	}

	plain := except(r.Conflicts, r.BinaryConflicts, r.BinaryGone, r.PruneConflicts, r.Unverified, r.Diverged, r.StaleProof, r.LeaseBroken, r.PruneLeaseBroken)

	var parts []string
	if len(plain) > 0 {
		parts = append(parts, "server moved ahead; `dsx pull` first, or --force")
	}
	if len(r.Unverified) > 0 {
		parts = append(parts, fmt.Sprintf(
			"never verified against the server (%s) — `dsx fetch` checks without writing, or --force overwrites",
			strings.Join(r.Unverified, ", ")))
	}
	if len(r.Diverged) > 0 {
		parts = append(parts, fmt.Sprintf(
			"differs from the server, confirmed by the last `dsx fetch` (%s) — --force overwrites the server's copy",
			strings.Join(r.Diverged, ", ")))
	}
	if len(r.StaleProof) > 0 {
		parts = append(parts, fmt.Sprintf(
			"verified, but against an earlier revision of the server (%s) — `dsx fetch` re-checks the current one, or --force overwrites the server's copy",
			strings.Join(r.StaleProof, ", ")))
	}
	if len(r.LeaseBroken) > 0 {
		// Names dsx fetch, never --force. The lease was broken by someone
		// else's write, and answering that with a blind overwrite is exactly
		// the destruction the flag was asked for to avoid.
		parts = append(parts, fmt.Sprintf(
			"the server moved after the last `dsx fetch` (%s) — the lease does not cover it; "+
				"run `dsx fetch` to see what landed, then decide",
			strings.Join(r.LeaseBroken, ", ")))
	}
	if len(r.PruneLeaseBroken) > 0 {
		// Says DELETE out loud. The generic lease wording says "not written",
		// which is true here and tells the reader nothing about what --prune
		// was about to remove. Names dsx fetch, never --force, for the same
		// reason the write lane does.
		parts = append(parts, fmt.Sprintf(
			"--prune would delete paths the server moved after the last `dsx fetch` (%s) — "+
				"the lease does not cover deleting them; run `dsx fetch` to see what landed, then decide",
			strings.Join(r.PruneLeaseBroken, ", ")))
	}
	if len(r.BinaryConflicts) > 0 {
		parts = append(parts, fmt.Sprintf(
			"binary conflicts (%s) — dsx has never held these bytes, so it cannot tell your copy from the server's; "+
				"`dsx pull --binary` fetches the server's copy, or --force overwrites it and the only copy is gone",
			strings.Join(r.BinaryConflicts, ", ")))
	}
	if len(r.BinaryGone) > 0 {
		parts = append(parts, fmt.Sprintf(
			"binary paths (%s) are gone from the server and dsx did not re-create them — "+
				"delete them here, or --force to re-upload",
			strings.Join(r.BinaryGone, ", ")))
	}
	if len(r.PruneConflicts) > 0 {
		parts = append(parts, fmt.Sprintf(
			"prune conflicts (%s) moved ahead on the server; --force DELETES the server's newer copy",
			strings.Join(r.PruneConflicts, ", ")))
	}
	return dsxerr.Conflict(r.Conflicts, strings.Join(parts, "; "))
}

func (r PullReport) Outcome() error {
	if len(r.Conflicts) == 0 {
		return nil
	}

	plain := except(r.Conflicts, r.PruneConflicts, r.PruneBinary, r.Unverified, r.Diverged, r.StaleProof, r.BinaryDiverged, r.Raced)

	var parts []string
	if len(plain) > 0 {
		parts = append(parts, "local differs from the server; --force overwrites")
	}
	if len(r.Unverified) > 0 {
		parts = append(parts, fmt.Sprintf(
			"never verified against the server (%s) — `dsx fetch` checks without writing, or --force overwrites",
			strings.Join(r.Unverified, ", ")))
	}
	if len(r.Diverged) > 0 {
		parts = append(parts, fmt.Sprintf(
			"differs from the server, confirmed by the last `dsx fetch` (%s) — --force overwrites",
			strings.Join(r.Diverged, ", ")))
	}
	if len(r.StaleProof) > 0 {
		parts = append(parts, fmt.Sprintf(
			"verified, but against an earlier revision of the server (%s) — `dsx fetch` re-checks the current one, or --force overwrites",
			strings.Join(r.StaleProof, ", ")))
	}
	if len(r.BinaryDiverged) > 0 {
		// Names no `dsx fetch`: fetch cannot help a tracked path, and this
		// divergence is already proven on the content itself — the strongest
		// evidence any conflict class here carries.
		parts = append(parts, fmt.Sprintf(
			"differs from the server's copy, compared byte for byte (%s) — --force overwrites",
			strings.Join(r.BinaryDiverged, ", ")))
	}
	if len(r.Raced) > 0 {
		// Names the retry and never --force: the server moved under the
		// download, which is nobody's disagreement to overrule.
		parts = append(parts, fmt.Sprintf(
			"the server moved these while dsx was reading them, so nothing was recorded (%s) — "+
				"run `dsx pull --binary` again",
			strings.Join(r.Raced, ", ")))
	}
	if len(r.PruneConflicts) > 0 {
		parts = append(parts, fmt.Sprintf(
			"prune conflicts (%s) were deleted on the server but edited here — --force DELETES your only copy",
			strings.Join(r.PruneConflicts, ", ")))
	}
	if len(r.PruneBinary) > 0 {
		parts = append(parts, fmt.Sprintf(
			"unprunable binary paths (%s) are gone from the server and dsx cannot re-fetch them, "+
				"so --prune kept them and --force will not delete them either — remove them yourself",
			strings.Join(r.PruneBinary, ", ")))
	}
	return dsxerr.Conflict(r.Conflicts, strings.Join(parts, "; "))
}
