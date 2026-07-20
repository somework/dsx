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
}

func planPull(remote map[string]RemoteEntry, local map[string]localFile, st State, force, prune bool) pullDecision {
	var d pullDecision

	for _, path := range SortedPaths(remote) {
		r := remote[path]
		prev, tracked := st.Files[path]
		onDisk, present := local[path]

		// localDirty compares bytes (SHA), not etag (invariant 2).
		localDirty := present && (!tracked || onDisk.SHA != prev.SHA)

		switch {
		case present && onDisk.Irregular:
			d.Irregular = append(d.Irregular, path)
		case tracked && prev.Etag == r.Etag && prev.Binary:
			d.Binary = append(d.Binary, path)
		case tracked && prev.Etag == r.Etag && present && !localDirty:
			d.Unchanged++
		case localDirty && !force:
			d.Conflicts = append(d.Conflicts, path)
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
}

func planPush(remote map[string]RemoteEntry, local map[string]localFile, st State, force, prune bool) pushDecision {
	var d pushDecision

	for _, path := range SortedPaths(local) {
		lf := local[path]
		prev, tracked := st.Files[path]
		r, onServer := remote[path]

		localChanged := !tracked || lf.SHA != prev.SHA
		remoteMoved := onServer && tracked && r.Etag != prev.Etag

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
		case !force && !tracked && onServer:
			d.Conflicts = append(d.Conflicts, path)
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
