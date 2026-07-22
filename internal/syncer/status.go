package syncer

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/fmtutil"
)

// statusDecision is what an offline status concluded. The first four fields
// come from the ledger against the disk and are exact. The rest come from the
// last fetch's snapshot and are only as fresh as it is — which is why they
// are kept apart rather than merged into one verdict per path.
type statusDecision struct {
	Modified  []string
	Deleted   []string
	Untracked []string
	Irregular []string

	ServerAhead    []string
	GoneFromServer []string
	RemoteOnly     []string

	UntrackedSame    []string
	UntrackedDiffers []string
}

// classifyStatus answers both halves without a network call. It is pure over
// its four inputs for the reason planPull and planPush are: the decisions
// worth testing are the ones testable without an endpoint.
//
// It classifies and nothing more. Unlike planPull/planPush no arm here
// authorises a write or a delete, so the proof below can only ever keep an
// adopted directory from reading as a wall of conflicts — it can never widen
// what a later act is allowed to touch.
func classifyStatus(snapshot map[string]SnapshotEntry, local map[string]localFile, st State, verified map[string]BaselineEntry) statusDecision {
	var d statusDecision

	// Ledger against disk. Computed now, so it is exact.
	for path, prev := range st.Files {
		lf, present := local[path]
		switch {
		case !present:
			d.Deleted = append(d.Deleted, path)
		case lf.Irregular:
			// Invariant 4's "not proof of anything": an irregular entry
			// carries an empty sha, which a plain comparison against the
			// ledger reads as an edit on every socket and symlink.
			d.Irregular = append(d.Irregular, path)
		case lf.SHA != prev.SHA:
			d.Modified = append(d.Modified, path)
		}
	}

	for path, lf := range local {
		if _, isTracked := st.Files[path]; isTracked {
			continue
		}
		if lf.Irregular {
			d.Irregular = append(d.Irregular, path)
			continue
		}
		s, listed := snapshot[path]
		if !listed {
			d.Untracked = append(d.Untracked, path)
			continue
		}
		// planPull's proof minus the tracked check the branch above already
		// made: both etags non-empty and equal, both shas non-empty and
		// equal. A missing map key yields a zero entry, so dropping either
		// non-empty half would let one prove a path the snapshot never held.
		b := verified[path]
		proven := b.Etag != "" && s.Etag != "" && b.Etag == s.Etag &&
			b.SHA != "" && b.SHA == lf.SHA
		if proven {
			d.UntrackedSame = append(d.UntrackedSame, path)
			continue
		}
		d.UntrackedDiffers = append(d.UntrackedDiffers, path)
	}

	// Snapshot against ledger. Remembered, not observed.
	for path, prev := range st.Files {
		s, listed := snapshot[path]
		if !listed {
			// Absent, not edited. Folded into ServerAhead this would tell the
			// reader to pull bytes that are not there.
			d.GoneFromServer = append(d.GoneFromServer, path)
			continue
		}
		if s.Etag != prev.Etag {
			d.ServerAhead = append(d.ServerAhead, path)
		}
	}

	for path := range snapshot {
		if _, isTracked := st.Files[path]; isTracked {
			continue
		}
		if _, present := local[path]; present {
			continue
		}
		d.RemoteOnly = append(d.RemoteOnly, path)
	}

	for _, s := range [][]string{
		d.Modified, d.Deleted, d.Untracked, d.Irregular,
		d.ServerAhead, d.GoneFromServer, d.RemoteOnly,
		d.UntrackedSame, d.UntrackedDiffers,
	} {
		slices.Sort(s)
	}
	return d
}

// StatusOpts takes no *mcp.Client and no context: status makes no network
// call, and the type it accepts says so, the way PinOpts does.
type StatusOpts struct {
	ProjectID string
	Dir       string
}

// StatusReport is the offline answer. The first four fields are exact — the
// ledger read against the disk just now. The rest are as fresh as the last
// `dsx fetch` and Render labels them so.
type StatusReport struct {
	Modified  []string `json:"modified"`
	Deleted   []string `json:"deleted"`
	Untracked []string `json:"untracked"`
	Irregular []string `json:"irregular,omitempty"`

	ServerAhead    []string `json:"server_ahead"`
	GoneFromServer []string `json:"gone_from_server,omitempty"`
	RemoteOnly     []string `json:"remote_only"`

	UntrackedSame    []string `json:"untracked_same,omitempty"`
	UntrackedDiffers []string `json:"untracked_differs,omitempty"`
}

// Status answers from disk alone: the ledger, the last fetch's snapshot, and
// a local scan. A stale snapshot is the price, and the report says which half
// is remembered rather than observed — the same bargain `git status` strikes
// against refs/remotes, and the reason neither needs a network call.
//
// A missing snapshot is refused, not shaded. nil is "no fetch ever ran here",
// which is not "the server holds nothing", and a report that quietly answered
// only the local half would be indistinguishable from one that answered both.
func Status(o StatusOpts) (StatusReport, error) {
	var rep StatusReport

	st, err := LoadState(o.Dir)
	if err != nil {
		return rep, err
	}
	if st.ProjectID != "" && st.ProjectID != o.ProjectID {
		return rep, &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
			"%s is bound to project %s; refusing to report %s against it",
			StatePath(o.Dir), st.ProjectID, o.ProjectID)}
	}
	if err := checkLedgerHome(o.Dir); err != nil {
		return rep, err
	}

	bl, err := loadBaseline(o.Dir)
	if err != nil {
		return rep, err
	}
	// bound() discards a baseline recorded for another project or endpoint,
	// so a foreign snapshot lands here exactly as an absent one does. It is
	// the same state: nothing on disk describes this binding's server.
	if bl.Listing == nil || !bl.bound(o.ProjectID, st.Endpoint) {
		return rep, &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
			"no dsx fetch has run in %s, so nothing here knows what the server holds — "+
				"run `dsx fetch` to record it, or `dsx pull -n` to ask the server now",
			o.Dir)}
	}

	// The empty listing is honest: the snapshot was already filtered when
	// fetch recorded it (TestTheSnapshotIsRecordedAlreadyFiltered), so both
	// sides are filtered by the same machinery, just at different moments —
	// which is what "as of the last fetch" means. There is no second listing
	// here to filter, and filterRemote could not take one anyway: it wants
	// RemoteEntry, and a snapshot is deliberately not that.
	_, local, err := survey(o.Dir, map[string]RemoteEntry{})
	if err != nil {
		return rep, err
	}

	// A plain conversion, not a field-by-field copy: statusDecision and
	// StatusReport differ only in their json tags, so the compiler catches a
	// field added to one and not the other. Unlike BaselineEntry against
	// FileState (invariant 17) the duplication here guards nothing — neither
	// type reaches a prune loop, or any act at all.
	return StatusReport(classifyStatus(bl.Listing, local, st, bl.Verified)), nil
}

type statusLine struct {
	path    string
	verdict string
}

func (r StatusReport) remembered() []statusLine {
	var out []statusLine
	for _, p := range r.ServerAhead {
		out = append(out, statusLine{p, "server moved ahead"})
	}
	for _, p := range r.GoneFromServer {
		out = append(out, statusLine{p, "gone from the server"})
	}
	for _, p := range r.RemoteOnly {
		out = append(out, statusLine{p, "remote-only"})
	}
	for _, p := range r.UntrackedDiffers {
		out = append(out, statusLine{p, "untracked, differs"})
	}
	for _, p := range r.UntrackedSame {
		out = append(out, statusLine{p, "untracked, matches"})
	}
	slices.SortFunc(out, func(a, b statusLine) int { return strings.Compare(a.path, b.path) })
	return out
}

func (r StatusReport) Render(asJSON bool) string {
	if asJSON {
		b, _ := json.Marshal(r)
		return string(b)
	}

	var sb strings.Builder
	local := [][2]any{
		{"modified", r.Modified}, {"deleted", r.Deleted},
		{"untracked", r.Untracked}, {"irregular", r.Irregular},
	}
	for _, sec := range local {
		paths := sec[1].([]string)
		if len(paths) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "%s locally:\n", sec[0])
		for _, p := range paths {
			fmt.Fprintf(&sb, "  %s\n", fmtutil.Printable(p))
		}
		sb.WriteString("\n")
	}

	remote := r.remembered()
	if len(remote) > 0 {
		sb.WriteString("as of the last dsx fetch:\n")
		width := 0
		for _, l := range remote {
			width = max(width, len(fmtutil.Printable(l.path)))
		}
		for _, l := range remote {
			fmt.Fprintf(&sb, "  %-*s  %s\n", width, fmtutil.Printable(l.path), l.verdict)
		}
		sb.WriteString("\n")
	}

	if sb.Len() == 0 {
		return "clean — nothing changed locally, nothing new on the server as of the last dsx fetch"
	}
	sb.WriteString("  (dsx fetch to refresh, dsx pull -n to ask the server now)")
	return sb.String()
}
