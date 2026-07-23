package syncer

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
)

// The Diverged twin of TestStaleProofOnlyMovesAPathFromUnverifiedToStaleProof
// and TestBaselineOnlyEverMovesAPathFromConflictsToVerified: injecting a
// diverged-shaped baseline entry (etag equal to the listing's, sha unequal to
// the local one) may only ever move that exact path out of Unverified and into
// Diverged, and must leave every other slice identical.
func TestDivergedOnlyMovesAPathFromUnverifiedToDiverged(t *testing.T) {
	etags := []string{"e1", "e2"}
	shas := []string{"s0", "s1", "s2"}
	rng := rand.New(rand.NewPCG(7, 13))

	const numPaths = 16
	const trials = 30

	for trial := range trials {
		remote := map[string]RemoteEntry{}
		local := map[string]localFile{}
		files := map[string]FileState{}
		baselineBase := map[string]BaselineEntry{}
		remoteEtagOf := map[string]string{}
		onDiskSHAOf := map[string]string{}
		candidates := map[string]bool{}

		for i := range numPaths {
			p := fmt.Sprintf("p%02d.css", i)

			onServer := rng.IntN(2) == 0
			if onServer {
				et := etags[rng.IntN(len(etags))]
				remote[p] = RemoteEntry{Path: p, Type: "file", Etag: et, Size: 1}
				remoteEtagOf[p] = et
			}

			var present, irregular bool
			switch rng.IntN(3) {
			case 0:
				// stays absent locally
			case 1:
				present, irregular = true, true
				local[p] = localFile{Path: p, Irregular: true}
			default:
				present = true
				sha := shas[rng.IntN(len(shas))]
				local[p] = localFile{Path: p, SHA: sha, Size: 1}
				onDiskSHAOf[p] = sha
			}

			tracked := rng.IntN(2) == 0
			if tracked {
				files[p] = FileState{
					Etag:   etags[rng.IntN(len(etags))],
					SHA:    shas[rng.IntN(len(shas))],
					Binary: rng.IntN(8) == 0,
				}
			}

			switch rng.IntN(4) {
			case 0:
				// no base entry
			case 1:
				if present {
					baselineBase[p] = BaselineEntry{Etag: remoteEtagOf[p], SHA: onDiskSHAOf[p], Size: 1}
				}
			case 2:
				if present {
					baselineBase[p] = BaselineEntry{Etag: "old-" + remoteEtagOf[p], SHA: onDiskSHAOf[p], Size: 1}
				}
			default:
				baselineBase[p] = BaselineEntry{Etag: "zzz", SHA: "zzz-sha", Size: 1}
			}

			if _, hasBase := baselineBase[p]; !hasBase && present && !irregular && !tracked && onServer && rng.IntN(2) == 0 {
				candidates[p] = true
			}
		}

		baselineWithout := map[string]BaselineEntry{}
		for p, e := range baselineBase {
			baselineWithout[p] = e
		}
		baselineWith := map[string]BaselineEntry{}
		for p, e := range baselineBase {
			baselineWith[p] = e
		}
		for p := range candidates {
			// Etag equal to the listing's, sha never equal to the local one.
			baselineWith[p] = BaselineEntry{Etag: remoteEtagOf[p], SHA: "x-" + onDiskSHAOf[p], Size: 1}
		}

		st := State{ProjectID: "p", Files: files}

		for _, force := range []bool{false, true} {
			for _, prune := range []bool{false, true} {
				name := fmt.Sprintf("trial=%d/force=%v/prune=%v", trial, force, prune)

				wantMoved := map[string]bool{}
				if !force {
					for p := range candidates {
						wantMoved[p] = true
					}
				}

				t.Run(name+"/pull", func(t *testing.T) {
					without := planPull(remote, local, st, baselineWithout, force, prune, false)
					with := planPull(remote, local, st, baselineWith, force, prune, false)

					if len(without.Diverged) != 0 {
						t.Fatalf("Diverged without the injected entries is non-empty: %v", without.Diverged)
					}
					gotMoved := map[string]bool{}
					for _, p := range with.Diverged {
						gotMoved[p] = true
					}
					if !slices.Equal(sortedCopy(mapKeys(gotMoved)), sortedCopy(mapKeys(wantMoved))) {
						t.Fatalf("Diverged = %v, want exactly %v", with.Diverged, mapKeys(wantMoved))
					}

					wantUnverified := except(without.Unverified, mapKeys(wantMoved))
					if !slices.Equal(sortedCopy(with.Unverified), sortedCopy(wantUnverified)) {
						t.Errorf("Unverified = %v, want %v (without's Unverified minus the moved paths)", with.Unverified, wantUnverified)
					}

					if !slices.Equal(without.StaleProof, with.StaleProof) {
						t.Errorf("StaleProof changed: without=%v with=%v", without.StaleProof, with.StaleProof)
					}
					if !slices.Equal(without.Fetch, with.Fetch) {
						t.Errorf("Fetch changed: without=%v with=%v", without.Fetch, with.Fetch)
					}
					if without.Verified != with.Verified {
						t.Errorf("Verified changed: without=%d with=%d", without.Verified, with.Verified)
					}
					if !slices.Equal(without.Conflicts, with.Conflicts) {
						t.Errorf("Conflicts changed: without=%v with=%v", without.Conflicts, with.Conflicts)
					}
					if without.Unchanged != with.Unchanged {
						t.Errorf("Unchanged changed: without=%d with=%d", without.Unchanged, with.Unchanged)
					}
					if !slices.Equal(without.Binary, with.Binary) {
						t.Errorf("Binary changed: without=%v with=%v", without.Binary, with.Binary)
					}
					if !slices.Equal(without.Irregular, with.Irregular) {
						t.Errorf("Irregular changed: without=%v with=%v", without.Irregular, with.Irregular)
					}
					if !slices.Equal(without.Delete, with.Delete) {
						t.Errorf("Delete changed: without=%v with=%v", without.Delete, with.Delete)
					}
					if !slices.Equal(without.PruneConflicts, with.PruneConflicts) {
						t.Errorf("PruneConflicts changed: without=%v with=%v", without.PruneConflicts, with.PruneConflicts)
					}
					if !slices.Equal(without.PruneBinary, with.PruneBinary) {
						t.Errorf("PruneBinary changed: without=%v with=%v", without.PruneBinary, with.PruneBinary)
					}
				})

				t.Run(name+"/push", func(t *testing.T) {
					without := planPush(remote, local, st, baselineWithout, nil, pushMode(force), prune)
					with := planPush(remote, local, st, baselineWith, nil, pushMode(force), prune)

					if len(without.Diverged) != 0 {
						t.Fatalf("Diverged without the injected entries is non-empty: %v", without.Diverged)
					}
					gotMoved := map[string]bool{}
					for _, p := range with.Diverged {
						gotMoved[p] = true
					}
					if !slices.Equal(sortedCopy(mapKeys(gotMoved)), sortedCopy(mapKeys(wantMoved))) {
						t.Fatalf("Diverged = %v, want exactly %v", with.Diverged, mapKeys(wantMoved))
					}

					wantUnverified := except(without.Unverified, mapKeys(wantMoved))
					if !slices.Equal(sortedCopy(with.Unverified), sortedCopy(wantUnverified)) {
						t.Errorf("Unverified = %v, want %v (without's Unverified minus the moved paths)", with.Unverified, wantUnverified)
					}

					if !slices.Equal(without.StaleProof, with.StaleProof) {
						t.Errorf("StaleProof changed: without=%v with=%v", without.StaleProof, with.StaleProof)
					}
					if !slices.Equal(writtenPaths(without), writtenPaths(with)) {
						t.Errorf("Write changed: without=%v with=%v", writtenPaths(without), writtenPaths(with))
					}
					if without.Verified != with.Verified {
						t.Errorf("Verified changed: without=%d with=%d", without.Verified, with.Verified)
					}
					if !slices.Equal(without.Conflicts, with.Conflicts) {
						t.Errorf("Conflicts changed: without=%v with=%v", without.Conflicts, with.Conflicts)
					}
					if without.Unchanged != with.Unchanged {
						t.Errorf("Unchanged changed: without=%d with=%d", without.Unchanged, with.Unchanged)
					}
					if !slices.Equal(without.Irregular, with.Irregular) {
						t.Errorf("Irregular changed: without=%v with=%v", without.Irregular, with.Irregular)
					}
					if !slices.Equal(without.BinaryConflicts, with.BinaryConflicts) {
						t.Errorf("BinaryConflicts changed: without=%v with=%v", without.BinaryConflicts, with.BinaryConflicts)
					}
					if !slices.Equal(without.BinaryGone, with.BinaryGone) {
						t.Errorf("BinaryGone changed: without=%v with=%v", without.BinaryGone, with.BinaryGone)
					}
					if !slices.Equal(without.Delete, with.Delete) {
						t.Errorf("Delete changed: without=%v with=%v", without.Delete, with.Delete)
					}
					if !slices.Equal(without.PruneConflicts, with.PruneConflicts) {
						t.Errorf("PruneConflicts changed: without=%v with=%v", without.PruneConflicts, with.PruneConflicts)
					}
				})
			}
		}
	}
}
