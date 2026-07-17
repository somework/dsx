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
