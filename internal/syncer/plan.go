package syncer

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

	// Gone from the server, tracked binary:true, still on disk. Never deleted --
	// but never silent either; see the prune loop.
	PruneBinary []string
}

func planPull(remote map[string]RemoteEntry, local map[string]localFile, st State, force, prune bool) pullDecision {
	var d pullDecision

	for _, path := range SortedPaths(remote) {
		r := remote[path]
		prev, tracked := st.Files[path]
		onDisk, present := local[path]

		// localDirty compares bytes (SHA), not etag: an etag-only guard cannot
		// see the both-sides-changed case, which is the canonical conflict.
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

	// --prune deletes only what we can prove was ours and unmodified: untracked
	// is not ours, locally-edited is a conflict, and an Irregular (symlink, etc.)
	// was never a plain file here, so its absence is not proof of a deletion.
	if prune {
		for _, path := range SortedPaths(local) {
			if _, stillRemote := remote[path]; stillRemote {
				continue
			}
			prev, tracked := st.Files[path]
			if !tracked {
				continue
			}
			// Mirrors planPush's guard, and is likewise
			// unconditional -- but NOT because --force is the only way in. A
			// forced push of such a path leaves {Binary: true, SHA: <real>}
			// (writeBatch carries the marker), and against that ledger the local
			// bytes MATCH, so the SHA check below does not divert: this line is
			// the only thing standing between a plain, non-force `pull --prune`
			// and deleting the user's only copy of a file dsx can never re-fetch.
			// TestForcedPushOfABinaryEntryDoesNotArmAPlainPullPrune.
			//
			// Reported, not skipped. `continue` would keep the file and tell the
			// user nothing, and the silence is not free: the next plain `dsx push`
			// sees local+tracked+prev.Binary with onServer FALSE, so planPush's
			// BinaryConflicts case cannot fire, and the path falls through to
			// Write with if_match "0" -- a silent re-upload.
			// Its own slice, not PruneConflicts: this guard sits ABOVE the force
			// check, so --force will not delete it either, and PruneConflicts'
			// wording promises exactly that deletion.
			// TestPlainPullPruneReportsAnUnprunableBinaryPath.
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
			// Record it regardless of whether the server has the path: a
			// local-only symlink dropped from every field leaves the user with
			// "pushed 0" and no trace it was skipped. It is never pushable.
			d.Irregular = append(d.Irregular, path)
			continue
		case tracked && !localChanged && onServer && !remoteMoved:
			d.Unchanged++
			continue
		case tracked && !localChanged && remoteMoved:
			d.Unchanged++
			continue
		case !force && remoteMoved:
			d.Conflicts = append(d.Conflicts, path)
			continue
		case !force && !tracked && onServer:
			d.Conflicts = append(d.Conflicts, path)
			continue
		case !force && tracked && prev.Binary && onServer:
			d.BinaryConflicts = append(d.BinaryConflicts, path)
			continue
		}

		cand := pushCandidate{Path: path}

		if !force {
			switch {
			case !onServer:
				cand.IfMatch = "0"
			case tracked && !prev.Binary:
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
			// Silent by design, unlike planPull's mirror of this guard. There, the
			// shape (tracked binary AND on disk) is an anomaly worth a word.
			// Here it is the STEADY STATE of
			// every binary file the project has ever had -- pulled, never landed
			// on disk, so localCovers is false on every run. Reporting it would
			// put a conflict and exit 3 on every `push --prune` of any project
			// holding a single image.
			if prev.Binary {
				continue
			}
			// The server moved this path ahead of the ledger since the last
			// sync: deleting it with a stale if_match would drop a change the
			// user never pulled. Mirror planPull's prune-conflict — a conflict,
			// not a delete, unless --force.
			if !force && remote[path].Etag != prev.Etag {
				d.PruneConflicts = append(d.PruneConflicts, path)
				continue
			}
			d.Delete = append(d.Delete, path)
		}
	}
	return d
}
