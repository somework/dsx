package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/fmtutil"
	"github.com/somework/dsx/internal/mcp"
)

type PullOpts struct {
	ProjectID   string
	Dir         string
	Concurrency int
	Prune       bool
	Force       bool
	DryRun      bool

	// Binary opts into the preview lane: the paths read_file refuses are
	// fetched over render_preview's serve_url instead of skipped. Off by
	// default because it moves bytes nothing asked for — a design project's
	// assets can be far larger than its text, and dsx's whole point is not
	// spending what it was not asked to spend.
	Binary bool

	// Where the transfer counter draws, nil for silence. The caller decides;
	// syncer never asks the terminal itself.
	Progress io.Writer
}

type PullReport struct {
	Fetched   []string `json:"fetched"`
	Unchanged int      `json:"unchanged"`
	Deleted   []string `json:"deleted"`

	// A proven byte match (invariant 17): a determination, not an act, so it
	// is filled from the plan on every path, DryRun included.
	Verified int `json:"verified"`

	Conflicts []string `json:"conflicts"`

	// Untracked and never proven equal to the server (invariant 17): a
	// subset of Conflicts, disjoint from PruneConflicts/PruneBinary. `dsx
	// fetch` is the only thing that can ever clear one.
	Unverified []string `json:"unverified,omitempty"`

	// Untracked, but a fresh baseline proved these bytes differ from the
	// server: a subset of Conflicts, disjoint from Unverified.
	Diverged []string `json:"diverged,omitempty"`

	// Untracked; a baseline proved these bytes once, but against a server
	// revision the listing no longer shows — a subset of Conflicts, disjoint
	// from both Unverified and Diverged. `dsx fetch` re-checks the current
	// revision.
	StaleProof []string `json:"stale_proof,omitempty"`

	PruneConflicts []string `json:"prune_conflicts,omitempty"`

	// Gone from the server but not prunable at any force level.
	PruneBinary []string `json:"prune_binary,omitempty"`

	Irregular []string `json:"irregular,omitempty"`
	Binary    []string `json:"binary"`

	// Bytes WRITTEN, which for the text lane has always also been the bytes
	// downloaded. --binary breaks that equivalence: an adopted path is
	// downloaded in full and written not at all, so a run reporting 0 B may
	// still have moved every asset in the project over the wire. Invariant 12
	// keeps this field naming the act it always named; the count that would
	// answer "what did this cost the network" is Adopted's, not this one.
	Bytes int64 `json:"bytes"`

	// --binary only. Held here already, byte-identical to the server's copy,
	// so nothing was written — the act these name is the ledger entry, which
	// is why they are filled after the post-download listing agrees and not
	// when the download returned (invariant 12).
	Adopted []string `json:"adopted,omitempty"`

	// --binary only. The local copy differs from the bytes the server served.
	// A subset of Conflicts, and the one conflict class proven on the content
	// itself rather than on an etag or a baseline.
	BinaryDiverged []string `json:"binary_diverged,omitempty"`

	// --binary only. The listing moved between the plan and the download, so
	// the bytes on disk cannot be attributed to the revision this run planned
	// against. Written, deliberately not recorded: a ledger entry pairing the
	// old etag with the new bytes is the wrong answer that outlives the run.
	Raced []string `json:"raced,omitempty"`

	// Set by the caller when the run ended in an error. The report goes to
	// stdout and the error to stderr, so a redirected stdout otherwise keeps
	// only the reassuring half. omitempty keeps success bytes unchanged.
	Incomplete bool `json:"incomplete,omitempty"`
}

// binaryResult is one preview-lane download, held back from the ledger until
// the post-download listing has had its say. adopted means the bytes matched
// the local copy and nothing was written.
type binaryResult struct {
	path    string
	etag    string
	size    int64
	sha     string
	adopted bool
}

// isBinaryRefusal keeps the name syncer's three readers use; the judgment
// lives in mcp, beside the lane that answers it.
func isBinaryRefusal(err error) bool { return mcp.IsBinaryRefusal(err) }

func Pull(ctx context.Context, c *mcp.Client, o PullOpts) (PullReport, error) {
	var rep PullReport

	st, err := LoadState(o.Dir)
	if err != nil {
		return rep, err
	}
	if st.ProjectID != "" && st.ProjectID != o.ProjectID {
		return rep, &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
			"%s is bound to project %s; refusing to pull %s into it",
			StatePath(o.Dir), st.ProjectID, o.ProjectID)}
	}
	if st.Endpoint != "" && !sameEndpoint(st.Endpoint, c.Endpoint()) {
		return rep, endpointRefusal(o.Dir, st.Endpoint, c.Endpoint(), "pull")
	}
	if err := checkLedgerHome(o.Dir); err != nil {
		return rep, err
	}

	bl, err := loadBaseline(o.Dir)
	if err != nil {
		return rep, err
	}
	baseline := map[string]BaselineEntry{}
	if bl.bound(o.ProjectID, c.Endpoint()) {
		baseline = bl.Verified
	}

	remote, err := WalkTree(ctx, c, o.ProjectID, o.Concurrency)
	if err != nil {
		return rep, err
	}

	remote, local, err := survey(o.Dir, remote)
	if err != nil {
		return rep, err
	}

	if err := checkPathCollisions(remote, local, o.Dir); err != nil {
		return rep, err
	}

	d := planPull(remote, local, st, baseline, o.Force, o.Prune, o.Binary)

	// The compensating check for the binary-lane arm keys off the ROUTE, not
	// off what the download turned out to be: that arm jumps over every
	// conflict arm, and a path whose server copy has since become valid UTF-8
	// takes it and then reads back through read_file, with `binary` false.
	// Gated on the refusal instead, such a path fell straight to writeAtomic
	// and overwrote a modified local file with no --force and no conflict.
	viaBinaryLane := make(map[string]bool, len(d.BinaryFetch))
	for _, p := range d.BinaryFetch {
		viaBinaryLane[p] = true
	}
	rep.Unchanged = d.Unchanged
	rep.Verified = d.Verified
	rep.Binary = d.Binary

	rep.Conflicts = append(append([]string(nil), d.Conflicts...), d.PruneConflicts...)
	rep.Conflicts = append(rep.Conflicts, d.PruneBinary...)
	rep.Conflicts = append(rep.Conflicts, d.Unverified...)
	rep.Conflicts = append(rep.Conflicts, d.Diverged...)
	rep.Conflicts = append(rep.Conflicts, d.StaleProof...)
	slices.Sort(rep.Conflicts)
	rep.Unverified = d.Unverified
	rep.Diverged = d.Diverged
	rep.StaleProof = d.StaleProof
	rep.PruneConflicts = d.PruneConflicts
	rep.PruneBinary = d.PruneBinary
	rep.Irregular = d.Irregular

	if o.DryRun {
		// DryRun: the plan is the requested outcome (invariant 12).
		rep.Deleted = d.Delete
		for _, path := range d.Fetch {
			rep.Fetched = append(rep.Fetched, path)
			rep.Bytes += remote[path].Size
		}
		return rep, nil
	}

	// First contact into a directory that already disagrees: write nothing.
	// With an empty ledger no path here was ever asked for, so writing the
	// non-conflicting ones leaves a half-foreign tree the caller never agreed
	// to and cannot tell from their own work. An established sync keeps the
	// opposite behaviour on purpose — there, a conflict on one path must not
	// stop the others, which is what makes conflicts recoverable one file at a
	// time. Nothing is written and nothing is saved, so the report carries the
	// conflicts and Outcome still supplies the exit code.
	if len(st.Files) == 0 && len(rep.Conflicts) > 0 {
		return rep, nil
	}

	for _, path := range append(append([]string{}, d.Fetch...), d.Delete...) {
		if err := checkRemotePath(path); err != nil {
			return rep, err
		}
	}

	// Record the listing this run walked. `status` answers from the snapshot
	// alone (invariant 19), and a pull that discarded its walk left `status`
	// and `push --force-with-lease` refusing — measured, exit 2 both — in a
	// directory `clone` had just written every byte of. `diff` does not
	// refuse there; it pays instead, re-downloading what it cannot prove. The
	// listing is already in hand, so recording it costs no request.
	//
	// Only the listing: Verified is written back exactly as loaded — as the
	// discarded empty map when the baseline was another binding's. Pull tracks
	// everything it writes and `proven` requires !tracked, so an entry pull
	// added would be dead by construction; one it invented would be a proof
	// about bytes nothing compared.
	//
	// Push must never do this. A lease means "I went and looked, and the
	// server has not moved since"; refreshed by the pushing side it would hold
	// always, which is a blind --force under the safe flag's name (invariant
	// 20). Pull earns it by being the side that reconciles.
	//
	// Placed here, below every refusal and above the first act: a dry run
	// returned above and leaves no trace, and so do the collision, remote-path
	// and first-contact gates (invariant 16). Below this line an error means
	// the act failed partway, which does not make the observation less true.
	//
	// The error is dropped on the way to a SUCCESS, which the other `_ = save`
	// sites are not — those drop a ledger error while already returning a
	// different one, which is invariant 5's rule, not this one.
	//
	// baseline.json is a cache: loadBaseline already refuses to let
	// an unreadable, undecodable or directory-shaped one block a sync
	// (TestACorruptBaselineDoesNotBlockASync), because blocking sends the user
	// toward `rm -rf .dsx`, which takes state.json with it. The write side has
	// to answer the same way for the same on-disk damage, and pull's product is
	// BYTES — the snapshot is a by-product. Fetch propagates its save error
	// instead, and the asymmetry is the point: the baseline is fetch's only
	// product, so a fetch that could not write one did nothing at all.
	//
	// Nor is this silent. Failing to record leaves exactly the state that
	// existed before pull recorded anything, and that state has its own loud
	// refusal downstream: `status` says "no dsx fetch has run here", a lease
	// reads a snapshot older than this run and breaks — both conservative,
	// neither quiet.
	snapshot := Baseline{
		ProjectID: o.ProjectID,
		Endpoint:  c.Endpoint(),
		Verified:  baseline,
		Listing:   snapshotOf(remote),
	}
	_ = snapshot.save(o.Dir)

	var (
		mu sync.Mutex
		wg sync.WaitGroup

		sem  = make(chan struct{}, max(o.Concurrency, 1))
		errs []error

		// What the preview lane brought back, held until the post-download
		// listing has a chance to disagree. Nothing here reaches the ledger
		// before then.
		binaries []binaryResult

		// Adopted paths, held back one step further than that. Every other
		// report field naming an act names one already durable on disk by the
		// time it is appended — a write, a rename, a delete. Adopted's act is
		// the LEDGER ENTRY and nothing else: an adopted path had its bytes
		// already, and dsx wrote nothing for it. So it may only be claimed
		// once st.save has actually returned nil (invariant 12).
		adopted []string

		prog = newProgress(o.Progress, "pulling", len(d.Fetch))
	)

	// Kept distinct from fetchCtx so a caller-side interrupt (parent.Err())
	// can still be told apart from a peer-triggered cancel below (invariant 3).
	parent := ctx
	fetchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Record under the lock, then cancel. The append-then-cancel ordering is
	// load-bearing (invariant 3).
	fail := func(err error) {
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
		cancel()
	}

	for _, path := range d.Fetch {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-fetchCtx.Done():
				return
			}
			defer func() { <-sem }()

			body, etag, err := c.ReadFull(fetchCtx, o.ProjectID, path)
			binary := false
			if err != nil {
				if !isBinaryRefusal(err) {
					fail(err)
					return
				}
				if !o.Binary {
					mu.Lock()
					rep.Binary = append(rep.Binary, path)
					st = st.withFile(path, FileState{Etag: remote[path].Etag, Binary: true})
					mu.Unlock()
					return
				}
				raw, berr := c.ReadBinary(fetchCtx, o.ProjectID, path, remote[path].Size)
				if berr != nil {
					// A failure here is not a quieter skip. --binary is the
					// caller asking for exactly these bytes, so not getting
					// them is the same kind of event as read_file failing.
					fail(berr)
					return
				}
				// The listing's etag is the only one there is: the preview
				// host returns an ETag of its own, but in a namespace
				// list_files' etag cannot be compared with. What gives it any
				// weight is the second listing after the downloads.
				body, etag, binary = string(raw), remote[path].Etag, true
			}

			// A decoded length must agree with list_files' size (invariant 1).
			// It is also what rejects the ~16 KiB preview harness the server
			// prepends to an .html served through this lane.
			if want := remote[path].Size; int64(len(body)) != want {
				fail(fmt.Errorf(
					"%s: decoded %d bytes, server reports %d — refusing to write",
					path, len(body), want))
				return
			}

			sha := SHA256Hex([]byte(body))

			// The plan could not decide an UNTRACKED binary — read_file's
			// refusal is what discovers one, and it arrives here, after
			// planning — and it deliberately did not decide a tracked one,
			// whose sha is either absent or (after a forced push) a record of
			// bytes dsx SENT, so localDirty said nothing true about it either
			// way. The bytes just downloaded do, and they say it better than
			// any etag.
			if binary || viaBinaryLane[path] {
				if lf, ok := local[path]; ok && !lf.Irregular {
					switch {
					case lf.SHA == sha:
						mu.Lock()
						if binary {
							binaries = append(binaries, binaryResult{
								path: path, etag: etag, size: int64(len(body)), sha: sha, adopted: true})
						} else {
							// read_file served it, so etag describes exactly
							// these bytes and no second listing can add to it.
							adopted = append(adopted, path)
							st = st.withFile(path, FileState{
								Etag: etag, Size: int64(len(body)), SHA: sha, Binary: true})
						}
						mu.Unlock()
						prog.step(path)
						return
					case !o.Force:
						mu.Lock()
						rep.BinaryDiverged = append(rep.BinaryDiverged, path)
						mu.Unlock()
						return
					}
				}
			}

			full, err := safeJoin(o.Dir, path)
			if err != nil {
				fail(err)
				return
			}
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				fail(err)
				return
			}
			if err := writeAtomic(full, []byte(body)); err != nil {
				fail(err)
				return
			}

			mu.Lock()
			rep.Fetched = append(rep.Fetched, path)
			rep.Bytes += int64(len(body))
			if binary {
				binaries = append(binaries, binaryResult{
					path: path, etag: etag, size: int64(len(body)), sha: sha})
			} else {
				st = st.withFile(path, FileState{Etag: etag, Size: int64(len(body)), SHA: sha})
			}
			mu.Unlock()

			prog.step(path)
		}(path)
	}
	wg.Wait()
	prog.clear()

	// The preview lane serves whatever a path holds NOW — a serve_url minted
	// before two writes was measured returning the second one's bytes — and it
	// hands back no etag that list_files' can be compared with. So nothing in
	// the download itself ties the bytes to the revision this run planned
	// against. One more listing does: a path whose etag has not moved was not
	// rewritten while dsx was reading it. Whole tree, one call, and only when
	// the lane actually ran. A path that did move keeps its bytes on disk and
	// gets no ledger entry — recording the planned etag against the new bytes
	// is the wrong answer that outlives the run, and the ledger left as it was
	// makes the next `dsx pull --binary` simply do it again.
	//
	// Deliberately NOT gated on len(errs): another path failing says nothing
	// about these, and skipping the commit for them leaves a file on disk that
	// rep.Fetched calls fetched while the ledger still carries the pre-run
	// binary marker — the report and the ledger disagreeing about the same
	// path, which is invariant 5's failure read from the other side. And when
	// the confirming listing cannot be had at all — it failed, or the caller
	// interrupted the run — every downloaded path is named as raced rather
	// than left to the run's error to stand in for the fact, because the run's
	// error says nothing about WHICH paths landed unattributed.
	if len(binaries) > 0 {
		var after map[string]RemoteEntry
		if parent.Err() == nil {
			listed, walkErr := WalkTree(ctx, c, o.ProjectID, o.Concurrency)
			if walkErr != nil {
				errs = append(errs, walkErr)
			} else {
				after = listed
			}
		}
		{
			for _, b := range binaries {
				e, still := after[b.path]
				if after == nil || !still || e.Etag != b.etag {
					rep.Raced = append(rep.Raced, b.path)
					continue
				}
				// The marker is cleared for a path dsx WROTE and kept for one
				// it merely adopted, and the difference is invariant 17's
				// whole argument. Both prune loops read Binary:true as "not
				// ours"; an adopted file is the user's own, matching by
				// coincidence of content, so clearing it there would make
				// deleting their copy delete the server's under a plain
				// `push --prune`, and a teammate's deletion delete theirs
				// under a plain `pull --prune` — both unforced, which is the
				// failure invariant 17 names verbatim. A written one dsx did
				// put there, so it becomes an ordinary entry. The sha rides
				// beside the marker either way, which is what stops push
				// calling an adopted path a binary conflict.
				st = st.withFile(b.path, FileState{
					Etag: b.etag, Size: b.size, SHA: b.sha, Binary: b.adopted})
				if b.adopted {
					adopted = append(adopted, b.path)
				}
			}
		}
	}

	// Discovered after planning, so merged after it (invariant 12 leaves a
	// determination free to arrive late; what it forbids is claiming an act early).
	//
	// Raced joins them. It is not a choice between two versions, but neither is
	// PruneBinary, which has always been a conflict: Conflicts is the set of
	// paths a caller has to act on, and under -q the exit code is the only
	// channel that says so at all. A run that wrote bytes and recorded nothing
	// for them has not done what --binary asked, and reporting that as success
	// is the short success invariant 3 forbids.
	rep.Conflicts = append(rep.Conflicts, rep.BinaryDiverged...)
	rep.Conflicts = append(rep.Conflicts, rep.Raced...)

	// Sorted above the error returns below, which are a machine surface too.
	slices.Sort(rep.Fetched)
	slices.Sort(rep.Binary)
	slices.Sort(rep.Raced)
	slices.Sort(rep.BinaryDiverged)
	slices.Sort(rep.Conflicts)

	st.ProjectID = o.ProjectID
	st.Endpoint = c.Endpoint()

	if len(errs) > 0 {
		_ = st.save(o.Dir)
		return rep, errs[0]
	}
	if err := parent.Err(); err != nil {
		_ = st.save(o.Dir)
		return rep, fmt.Errorf("pull interrupted: %w", err)
	}

	var pruneErr error
	for _, path := range d.Delete {
		full, err := safeJoin(o.Dir, path)
		if err != nil {
			pruneErr = err
			break
		}
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			pruneErr = err
			break
		}
		delete(st.Files, path)
		rep.Deleted = append(rep.Deleted, path)
	}

	// Above the error returns: --json is a machine surface on the failure paths too.
	slices.Sort(rep.Deleted)

	saveErr := st.save(o.Dir)
	// The one field whose act is the save itself — see `adopted` above.
	if saveErr == nil {
		slices.Sort(adopted)
		rep.Adopted = adopted
	}
	if pruneErr != nil {
		return rep, pruneErr
	}
	if saveErr != nil {
		return rep, saveErr
	}

	slices.Sort(rep.Conflicts)
	slices.Sort(rep.Unverified)
	slices.Sort(rep.Diverged)
	slices.Sort(rep.StaleProof)
	slices.Sort(rep.PruneConflicts)
	slices.Sort(rep.PruneBinary)
	slices.Sort(rep.Irregular)
	return rep, nil
}

func (r PullReport) Render(asJSON bool) string {
	if asJSON {
		b, _ := json.Marshal(r)
		return string(b)
	}
	var sb strings.Builder
	if r.Incomplete {
		sb.WriteString("incomplete: ")
	}
	fmt.Fprintf(&sb, "pulled %d, unchanged %d", len(r.Fetched), r.Unchanged)
	if len(r.Deleted) > 0 {
		fmt.Fprintf(&sb, ", deleted %d", len(r.Deleted))
	}
	if r.Verified > 0 {
		fmt.Fprintf(&sb, ", verified %d", r.Verified)
	}
	if len(r.Adopted) > 0 {
		fmt.Fprintf(&sb, ", adopted %d", len(r.Adopted))
	}
	if len(r.Conflicts) > 0 {
		fmt.Fprintf(&sb, ", conflicts %d", len(r.Conflicts))
	}
	if len(r.Binary) > 0 {
		fmt.Fprintf(&sb, ", binary %d", len(r.Binary))
	}
	fmt.Fprintf(&sb, " (%s)", fmtutil.Bytes(r.Bytes))

	for _, p := range r.Conflicts {
		if slices.Contains(r.PruneBinary, p) {
			fmt.Fprintf(&sb, "\n  ! %s — gone from the server; dsx cannot re-fetch it (binary), so it was kept — not even --force will prune it; delete it yourself if you meant to", p)
			continue
		}
		if slices.Contains(r.PruneConflicts, p) {
			fmt.Fprintf(&sb, "\n  ! %s — gone from the server, edited here; --force would DELETE your only copy", p)
			continue
		}
		if slices.Contains(r.Unverified, p) {
			fmt.Fprintf(&sb, "\n  ! %s — never verified against the server; `dsx fetch` checks without writing, or --force to overwrite", p)
			continue
		}
		if slices.Contains(r.Diverged, p) {
			fmt.Fprintf(&sb, "\n  ! %s — differs from the server, confirmed by the last `dsx fetch`; --force to overwrite", p)
			continue
		}
		if slices.Contains(r.StaleProof, p) {
			fmt.Fprintf(&sb, "\n  ! %s — verified, but against an earlier revision of the server; `dsx fetch` re-checks the current one, or --force to overwrite", p)
			continue
		}
		if slices.Contains(r.Raced, p) {
			// Neutral about what is on disk: a raced path may have been written
			// or may have been an adopted copy dsx never touched, and the line
			// has to be true of both. No --force is offered — nothing here is a
			// disagreement to overrule.
			fmt.Fprintf(&sb, "\n  ! %s — the server moved it while dsx was reading it, so nothing was recorded; run `dsx pull --binary` again", p)
			continue
		}
		if slices.Contains(r.BinaryDiverged, p) {
			fmt.Fprintf(&sb, "\n  ! %s — differs from the server's copy, compared byte for byte; --force to overwrite", p)
			continue
		}
		fmt.Fprintf(&sb, "\n  ! %s — local differs; --force to overwrite", p)
	}
	for _, p := range r.Irregular {
		fmt.Fprintf(&sb, "\n  ~ %s — not a regular file here; dsx left it alone", p)
	}
	if len(r.Binary) > 0 {
		fmt.Fprintf(&sb, "\n  ~ %d binary file(s) skipped — read_file serves text only; --binary fetches them over the preview lane: %s",
			len(r.Binary), strings.Join(r.Binary, ", "))
	}
	// Last, and only for the plain rung: cat cannot fetch a binary, and a path
	// gone from the server has no copy to fetch. The line makes no claim about
	// --force — Conflicts is merged, and on the destructive rungs --force deletes.
	if len(r.Conflicts)-len(r.PruneConflicts)-len(r.PruneBinary) > 0 {
		sb.WriteString("\n" + conflictHint)
	}
	return sb.String()
}
