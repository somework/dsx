package syncer

// BaselineEntry lives here, not in baseline.go, because planPull and
// planPush will read it and plan.go has no import block.
//
// It is field-for-field identical to FileState and is deliberately NOT that
// type. Not an alias, not an embed, not a shared map. The duplication is the
// guard: `st.Files = someBaseline.Verified` does not compile. DO NOT DRY
// THESE — see TestBaselineNeverBecomesTracked.
type BaselineEntry struct {
	Etag string `json:"etag"`
	Size int64  `json:"size"`
	SHA  string `json:"sha256"`
}

func localCovers(local map[string]localFile, path string) bool {
	if _, ok := local[path]; ok {
		return true
	}
	for i, c := range path {
		if c != '/' {
			continue
		}
		if lf, ok := local[path[:i]]; ok && lf.Irregular {
			return true
		}
	}
	return false
}

type pullDecision struct {
	Fetch     []string
	Unchanged int
	Binary    []string
	Conflicts []string
	Delete    []string

	Irregular []string

	PruneConflicts []string

	// Gone from the server, tracked binary:true, still on disk.
	PruneBinary []string

	// A proven byte match (invariant 17): a determination, not an act.
	Verified int

	// Present, differs from the ledger's idea of unchanged, but untracked —
	// no ledger entry has ever confirmed these are dsx's own bytes, so
	// "local differs" is not a claim dsx can make. Disjoint from Conflicts,
	// which is the tracked, genuinely-proven-divergent case. `dsx fetch` is
	// the only thing that can ever move a path out of here.
	Unverified []string

	// Untracked, but a fresh baseline (etag matches the current listing) DID
	// verify the bytes and found them to differ — a genuine, proven
	// divergence, unlike Unverified. Disjoint from both Conflicts (that's
	// the tracked case) and Unverified (that's never-checked-or-stale).
	Diverged []string

	// Untracked; a baseline once proved these exact bytes against the
	// server, and the local file has not changed since — but the listing
	// etag has moved on, so that proof covers a revision the server no
	// longer serves. Every write rotates the etag, content-identical or
	// not, so this is not a rare shape: a same-content resave by anyone
	// lands every unedited path here at once. Disjoint from Unverified (no
	// baseline ever proved anything here) and from Diverged (there the
	// baseline is fresh — b.Etag == r.Etag — and proved a difference against
	// the CURRENT revision, not a past one). `dsx fetch` is what re-checks
	// against the current revision.
	StaleProof []string
}

func planPull(remote map[string]RemoteEntry, local map[string]localFile, st State, baseline map[string]BaselineEntry, force, prune bool) pullDecision {
	var d pullDecision

	for _, path := range SortedPaths(remote) {
		r := remote[path]
		prev, tracked := st.Files[path]
		onDisk, present := local[path]
		b := baseline[path]

		// localDirty compares bytes (SHA), not etag (invariant 2).
		localDirty := present && (!tracked || onDisk.SHA != prev.SHA)

		// A real ledger entry always wins (!tracked), so invariant 2's
		// both-sides-changed case is untouched. An empty etag can never
		// prove freshness — a missing map key yields the zero BaselineEntry,
		// which must not be read as proof that a gone-from-the-server path
		// is identical.
		proven := present && !tracked && !onDisk.Irregular &&
			b.Etag != "" && r.Etag != "" && b.Etag == r.Etag &&
			b.SHA != "" && b.SHA == onDisk.SHA

		// A fresh baseline (same conjuncts as proven) that disagrees on sha
		// instead of matching: the last `dsx fetch` checked exactly this
		// server revision and found these bytes to differ.
		diverged := present && !tracked && !onDisk.Irregular &&
			b.Etag != "" && r.Etag != "" && b.Etag == r.Etag &&
			b.SHA != "" && b.SHA != onDisk.SHA

		// Same conjuncts as diverged, but b.Etag disagrees with r.Etag
		// instead of agreeing with it: the baseline proved these bytes once,
		// against a revision the server has since moved past.
		staleProof := present && !tracked && !onDisk.Irregular &&
			b.Etag != "" && r.Etag != "" && b.Etag != r.Etag &&
			b.SHA != "" && b.SHA == onDisk.SHA

		switch {
		case present && onDisk.Irregular:
			d.Irregular = append(d.Irregular, path)
		case tracked && prev.Etag == r.Etag && prev.Binary:
			d.Binary = append(d.Binary, path)
		case tracked && prev.Etag == r.Etag && present && !localDirty:
			d.Unchanged++
		case proven:
			d.Verified++
		case localDirty && !force && tracked:
			d.Conflicts = append(d.Conflicts, path)
		case diverged && !force:
			d.Diverged = append(d.Diverged, path)
		case staleProof && !force:
			d.StaleProof = append(d.StaleProof, path)
		case localDirty && !force:
			d.Unverified = append(d.Unverified, path)
		default:
			d.Fetch = append(d.Fetch, path)
		}
	}

	// --prune deletes only what we can prove was ours and unmodified (invariant 4).
	if prune {
		for _, path := range SortedPaths(local) {
			if _, stillRemote := remote[path]; stillRemote {
				continue
			}
			prev, tracked := st.Files[path]
			if !tracked {
				continue
			}
			// Above the force check: --force does not unlock this. Reported
			// rather than skipped, in its own slice — PruneConflicts' wording
			// promises a deletion this path never gets.
			if prev.Binary {
				d.PruneBinary = append(d.PruneBinary, path)
				continue
			}
			if local[path].Irregular {
				continue
			}
			if !force && local[path].SHA != prev.SHA {
				d.PruneConflicts = append(d.PruneConflicts, path)
				continue
			}
			d.Delete = append(d.Delete, path)
		}
	}
	return d
}

type pushCandidate struct {
	Path    string
	IfMatch string
}

type pushDecision struct {
	Write     []pushCandidate
	Unchanged int
	Conflicts []string
	Delete    []string

	Irregular []string

	BinaryConflicts []string

	// Tracked binary:true, on disk, gone from the server.
	BinaryGone []string

	PruneConflicts []string

	// A proven byte match (invariant 17): a determination, not an act.
	Verified int

	// See pullDecision.Unverified: the untracked-collision case, disjoint
	// from Conflicts (the tracked, remote-moved case).
	Unverified []string

	// See pullDecision.Diverged: untracked, but a fresh baseline proved the
	// bytes differ.
	Diverged []string

	// See pullDecision.StaleProof: untracked, unchanged since a baseline
	// proved it against a past revision, but the listing etag has moved on.
	StaleProof []string
}

func planPush(remote map[string]RemoteEntry, local map[string]localFile, st State, baseline map[string]BaselineEntry, force, prune bool) pushDecision {
	var d pushDecision

	for _, path := range SortedPaths(local) {
		lf := local[path]
		prev, tracked := st.Files[path]
		r, onServer := remote[path]
		b := baseline[path]

		localChanged := !tracked || lf.SHA != prev.SHA
		remoteMoved := onServer && tracked && r.Etag != prev.Etag

		// See planPull for why each conjunct is load-bearing.
		proven := !tracked && !lf.Irregular &&
			b.Etag != "" && r.Etag != "" && b.Etag == r.Etag &&
			b.SHA != "" && b.SHA == lf.SHA

		// See planPull's diverged: same conjuncts, sha disagrees instead of
		// matching.
		diverged := !tracked && !lf.Irregular &&
			b.Etag != "" && r.Etag != "" && b.Etag == r.Etag &&
			b.SHA != "" && b.SHA != lf.SHA

		// See planPull's staleProof: same conjuncts as diverged, but
		// b.Etag disagrees with r.Etag instead of agreeing. r.Etag != ""
		// stands in for onServer here, same as proven/diverged above.
		staleProof := !tracked && !lf.Irregular &&
			b.Etag != "" && r.Etag != "" && b.Etag != r.Etag &&
			b.SHA != "" && b.SHA == lf.SHA

		switch {
		case lf.Irregular:
			// Recorded regardless of whether the server has the path.
			d.Irregular = append(d.Irregular, path)
			continue
		case tracked && !localChanged && onServer && !remoteMoved:
			d.Unchanged++
			continue
		case tracked && !localChanged && remoteMoved:
			d.Unchanged++
			continue
		// Above the generic remoteMoved arm, so a moved binary gets the binary
		// wording rather than "`dsx pull` first".
		case !force && tracked && prev.Binary && onServer && (prev.SHA == "" || remoteMoved):
			d.BinaryConflicts = append(d.BinaryConflicts, path)
			continue
		case !force && remoteMoved:
			d.Conflicts = append(d.Conflicts, path)
			continue
		case proven:
			d.Verified++
			continue
		case diverged && !force:
			d.Diverged = append(d.Diverged, path)
			continue
		case staleProof && !force:
			d.StaleProof = append(d.StaleProof, path)
			continue
		case !force && !tracked && onServer:
			d.Unverified = append(d.Unverified, path)
			continue
		case !force && tracked && prev.Binary && !onServer:
			d.BinaryGone = append(d.BinaryGone, path)
			continue
		}

		cand := pushCandidate{Path: path}

		if !force {
			switch {
			case !onServer:
				cand.IfMatch = "0"
			case tracked && prev.Etag != "":
				cand.IfMatch = prev.Etag
			}
		}
		d.Write = append(d.Write, cand)
	}

	if prune {
		for _, path := range SortedPaths(remote) {
			if localCovers(local, path) {
				continue
			}
			prev, tracked := st.Files[path]
			if !tracked {
				continue
			}
			// Silent by design; see TestPlainPushPruneIsSilentAboutASteadyStateBinaryPath.
			if prev.Binary {
				continue
			}
			// The server moved this path ahead of the ledger: a conflict, not a
			// delete, unless --force.
			if !force && remote[path].Etag != prev.Etag {
				d.PruneConflicts = append(d.PruneConflicts, path)
				continue
			}
			d.Delete = append(d.Delete, path)
		}
	}
	return d
}
