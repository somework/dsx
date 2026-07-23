package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/fmtutil"
	"github.com/somework/dsx/internal/mcp"
)

type FetchOpts struct {
	ProjectID   string
	Dir         string
	Concurrency int

	// Where the transfer counter draws, nil for silence. See PullOpts.Progress.
	Progress io.Writer
}

type FetchReport struct {
	// Paths newly recorded in the baseline this run.
	Fetched []string `json:"fetched"`

	// Present, untracked, listed paths that were downloaded but NOT recorded:
	// a decoded length disagreeing with the listing's size (invariant 1), or a
	// preview-lane path the server rewrote while this run was reading it.
	// Never a wrong entry, never silently dropped.
	Skipped []string `json:"skipped,omitempty"`

	Bytes int64 `json:"bytes"`

	// See PullReport.Incomplete.
	Incomplete bool `json:"incomplete,omitempty"`
}

// Fetch records what the server holds for the narrow set of paths a fetch
// baseline can ever make useful: present on disk, not tracked by the ledger,
// not irregular, and still in the (ignore-filtered) listing. Nothing outside
// .dsx/ is written — Fetch is the mutating verb whose name suggests
// otherwise.
//
// The rewrite is wholesale: the new baseline holds exactly this run's
// verified entries, dropping anything recorded by an earlier fetch that this
// run did not re-verify (a path now tracked, now absent, now ignored, or
// simply not re-attempted). That is deliberate — see ACCEPTED COSTS in the
// design doc — and it is why an interrupted run must not save at all: a
// partial wholesale rewrite would discard baseline entries this run never
// even looked at (invariant 3).
func Fetch(ctx context.Context, c *mcp.Client, o FetchOpts) (FetchReport, error) {
	var rep FetchReport

	st, err := LoadState(o.Dir)
	if err != nil {
		return rep, err
	}
	if st.ProjectID != "" && st.ProjectID != o.ProjectID {
		return rep, &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
			"%s is bound to project %s; refusing to fetch %s into it",
			StatePath(o.Dir), st.ProjectID, o.ProjectID)}
	}
	if st.Endpoint != "" && !sameEndpoint(st.Endpoint, c.Endpoint()) {
		return rep, endpointRefusal(o.Dir, st.Endpoint, c.Endpoint(), "fetch")
	}
	if err := checkLedgerHome(o.Dir); err != nil {
		return rep, err
	}

	remote, err := WalkTree(ctx, c, o.ProjectID, o.Concurrency)
	if err != nil {
		return rep, err
	}

	remote, local, err := survey(o.Dir, remote)
	if err != nil {
		return rep, err
	}

	// The narrow set is exactly what `proven` (plan.go) can ever consume: a
	// baseline entry for a path not on disk, tracked in the ledger, or
	// irregular can never make it true, so downloading one would be waste.
	var target []string
	for _, path := range SortedPaths(remote) {
		lf, present := local[path]
		if !present || lf.Irregular {
			continue
		}
		if _, tracked := st.Files[path]; tracked {
			continue
		}
		target = append(target, path)
	}

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		sem      = make(chan struct{}, max(o.Concurrency, 1))
		verified = map[string]BaselineEntry{}
		errs     []error

		// Paths the preview lane supplied. read_file hands back an etag for
		// the very bytes it served; the preview host does not, so these need
		// the same post-download listing pull takes before their etag/sha pair
		// may be believed.
		previewed []string

		prog = newProgress(o.Progress, "fetching", len(target))
	)

	// Kept distinct from fetchCtx so a caller-side interrupt can still be told
	// apart from a peer-triggered cancel below (invariant 3), same as Pull.
	parent := ctx
	fetchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	fail := func(err error) {
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
		cancel()
	}

	for _, path := range target {
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
			viaPreview := false
			if err != nil {
				if !isBinaryRefusal(err) {
					fail(err)
					return
				}
				// The preview lane runs here with no flag, unlike pull's. The
				// two costs that keep it opt-in there do not exist here: fetch
				// writes nothing into the tree, and its target set is the
				// untracked paths alone — the exact set no ledger entry can
				// ever speak for, which is what left an untracked asset stuck
				// in `status`' untracked-differs and `pull`'s Unverified with
				// no command able to clear it. It does cost bytes, which is
				// what fetch's own report has always counted.
				raw, berr := c.ReadBinary(fetchCtx, o.ProjectID, path, remote[path].Size)
				if berr != nil {
					// Skipped and reported, not fatal — the opposite of pull's
					// choice, and for a reason. Under `pull --binary` the
					// caller named these bytes and not getting them is the
					// event. Nobody named anything here: fetch records what it
					// can about a whole tree, so one asset the preview host
					// will not serve must not throw away the proof gathered
					// for every other path. Skipped is fetch's existing word
					// for "downloaded, not recorded", and the rendered line
					// names them.
					mu.Lock()
					rep.Skipped = append(rep.Skipped, path)
					mu.Unlock()
					return
				}
				body, etag, viaPreview = string(raw), remote[path].Etag, true
			}

			// A path failing invariant 1's check gets no entry rather than a
			// wrong one: a sha recorded from a decode whose length disagreed
			// with the listing's size would, one run later, declare a
			// different local file identical.
			if want := remote[path].Size; int64(len(body)) != want {
				mu.Lock()
				rep.Skipped = append(rep.Skipped, path)
				mu.Unlock()
				return
			}

			mu.Lock()
			verified[path] = BaselineEntry{Etag: etag, Size: int64(len(body)), SHA: SHA256Hex([]byte(body))}
			if viaPreview {
				previewed = append(previewed, path)
			}
			mu.Unlock()

			prog.step(path)
		}(path)
	}
	wg.Wait()
	prog.clear()

	// The preview lane serves whatever a path holds now and hands back no etag
	// list_files' can be compared with, so the pair this run is about to
	// record — the pre-download listing's etag beside the downloaded sha —
	// is only true if nothing moved under it. One more listing says. A path
	// that moved gets no entry rather than a wrong one, which is invariant 1's
	// rule about this same file applied to a different way of being wrong.
	// Nothing was written to the tree, so dropping the entry is the whole
	// remedy; pull has to say more because pull left bytes on disk.
	if len(previewed) > 0 && len(errs) == 0 && parent.Err() == nil {
		after, walkErr := WalkTree(ctx, c, o.ProjectID, o.Concurrency)
		if walkErr != nil {
			errs = append(errs, walkErr)
		} else {
			for _, path := range previewed {
				if e, still := after[path]; !still || e.Etag != verified[path].Etag {
					delete(verified, path)
					rep.Skipped = append(rep.Skipped, path)
				}
			}
		}
	}

	slices.Sort(rep.Skipped)

	if len(errs) > 0 {
		return rep, errs[0]
	}
	// An interrupted run must not silently discard the baseline it never
	// re-verified: saving a partial wholesale rewrite here would drop entries
	// for paths this run had no chance to look at (invariant 3).
	if err := parent.Err(); err != nil {
		return rep, fmt.Errorf("fetch interrupted: %w", err)
	}

	// The snapshot is the whole ignore-filtered listing, not the narrow set
	// downloaded above: an offline reader has to be able to name a remote-only
	// or a tracked path, and neither can ever appear in Verified.
	//
	// Fetch is the only writer that fills both halves at once. Pull records
	// the listing beside a Verified map it leaves exactly as it found it, so
	// the two halves may date from different runs; nothing reads them as a
	// pair. Verified answers "are these bytes the server's", checked entry by
	// entry against the etag in THIS run's listing, and a snapshot older or
	// newer than the proof beside it cannot make that comparison come out
	// differently.
	bl := Baseline{ProjectID: o.ProjectID, Endpoint: c.Endpoint(), Verified: verified, Listing: snapshotOf(remote)}
	if err := bl.save(o.Dir); err != nil {
		return rep, err
	}

	// Fetched/Bytes name an act — invariant 12 — so they are built from what
	// the durable save actually recorded, not from what the in-memory
	// download loop verified. Every error return above this point leaves
	// them at their zero value, matching the fact that nothing was saved.
	for path, entry := range verified {
		rep.Fetched = append(rep.Fetched, path)
		rep.Bytes += entry.Size
	}
	slices.Sort(rep.Fetched)

	return rep, nil
}

func (r FetchReport) Render(asJSON bool) string {
	if asJSON {
		b, _ := json.Marshal(r)
		return string(b)
	}
	var sb strings.Builder
	if r.Incomplete {
		sb.WriteString("incomplete: ")
	}
	fmt.Fprintf(&sb, "fetched %d (%s)", len(r.Fetched), fmtutil.Bytes(r.Bytes))
	if len(r.Skipped) > 0 {
		// Named, not counted. A skipped path is one `status` will keep calling
		// untracked-differs until something else proves it, and a bare count
		// leaves the reader no way to know which.
		fmt.Fprintf(&sb, ", skipped %d (%s)", len(r.Skipped), strings.Join(r.Skipped, ", "))
	}
	return sb.String()
}
