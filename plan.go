package main

// The sync decisions live here, apart from the transport, because this is
// where a mistake costs data: a wrong call silently overwrites an edit or
// deletes a file nobody asked to delete. Pure input, pure output, testable.

type pullDecision struct {
	Fetch     []string
	Unchanged int
	Binary    []string
	Conflicts []string
	Delete    []string

	// PruneConflicts are paths gone from the server and edited here. They are
	// conflicts too, but --force resolves them by DELETING rather than by
	// overwriting, and the bytes it destroys exist nowhere else. Reporting them
	// in the same breath as an overwrite told the user --force would do the one
	// thing it would not do.
	PruneConflicts []string
}

// planPull decides, without touching the network, what a pull would do.
func planPull(remote map[string]remoteEntry, local map[string]localFile, st syncState, force, prune bool) pullDecision {
	var d pullDecision

	for _, path := range sortedPaths(remote) {
		r := remote[path]
		prev, tracked := st.Files[path]
		onDisk, present := local[path]

		// Dirty means the bytes on disk are not the bytes we last agreed on --
		// either we edited them, or they were never ours to begin with. Keying
		// the conflict off this rather than off the etag is what makes the
		// both-sides-changed case safe: an etag test only sees the server.
		localDirty := present && (!tracked || onDisk.SHA != prev.SHA)

		switch {
		case present && onDisk.Irregular:
			// A symlink, a fifo, something the user put there deliberately.
			// Writing would go through it to wherever it points -- safeJoin
			// refuses that outright -- and dsx cannot judge what it is for.
			d.Conflicts = append(d.Conflicts, path)
		case tracked && prev.Etag == r.Etag && prev.Binary:
			// Known-unreadable and unchanged since we learned that.
			d.Binary = append(d.Binary, path)
		case tracked && prev.Etag == r.Etag && present && !localDirty:
			d.Unchanged++
		case localDirty && !force:
			// Overwriting would destroy work that exists nowhere else.
			d.Conflicts = append(d.Conflicts, path)
		default:
			d.Fetch = append(d.Fetch, path)
		}
	}

	if prune {
		for _, path := range sortedPaths(local) {
			if _, stillRemote := remote[path]; stillRemote {
				continue
			}
			prev, tracked := st.Files[path]
			if !tracked {
				continue // never ours; leave it alone
			}
			if local[path].Irregular {
				continue // not ours to remove; we never held its bytes
			}
			if !force && local[path].SHA != prev.SHA {
				// Gone from the server, edited here: the local copy is the
				// only one left. Deleting it would be unrecoverable -- and
				// --force resolves this by deleting, not by overwriting, so it
				// is reported apart from the conflicts that --force overwrites.
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
}

// planPush decides, without touching the network, what a push would do.
func planPush(remote map[string]remoteEntry, local map[string]localFile, st syncState, force, prune bool) pushDecision {
	var d pushDecision

	for _, path := range sortedPaths(local) {
		lf := local[path]
		prev, tracked := st.Files[path]
		r, onServer := remote[path]

		localChanged := !tracked || lf.SHA != prev.SHA
		remoteMoved := onServer && tracked && r.Etag != prev.Etag

		switch {
		case lf.Irregular:
			// Not a regular file, so there are no bytes to send. It must still
			// be named: dropping it silently is what let prune read it as a
			// deletion and remove the server's copy.
			if onServer {
				d.Conflicts = append(d.Conflicts, path)
			}
			continue
		case tracked && !localChanged && onServer && !remoteMoved:
			d.Unchanged++
			continue
		case tracked && !localChanged && remoteMoved:
			// Server is ahead and we have nothing to send. That is a pull.
			d.Unchanged++
			continue
		case !force && remoteMoved:
			d.Conflicts = append(d.Conflicts, path)
			continue
		case !force && !tracked && onServer:
			d.Conflicts = append(d.Conflicts, path)
			continue
		case !force && tracked && prev.Binary && onServer:
			// read_file will not serve these bytes, so dsx never held them and
			// has no sha to compare: it cannot tell a deliberate replacement
			// from an accident. Worse, the server's copy is the only copy --
			// there is no `resources` capability and no encoding parameter, so
			// an overwrite here is unrecoverable. That makes it the one write
			// most worth refusing.
			d.Conflicts = append(d.Conflicts, path)
			continue
		}

		cand := pushCandidate{Path: path}
		// if_match turns a blind overwrite into a checked one: the server
		// refuses the write if the file moved since we last agreed on it.
		if !force {
			switch {
			case tracked && !prev.Binary:
				cand.IfMatch = prev.Etag
			case !onServer:
				cand.IfMatch = "0" // assert the path does not exist yet
			}
		}
		d.Write = append(d.Write, cand)
	}

	if prune {
		for _, path := range sortedPaths(remote) {
			// An irregular path is still in `local`, so it counts as present
			// here and is never pruned. That is the whole reason scanLocal
			// records it instead of dropping it.
			if _, stillLocal := local[path]; stillLocal {
				continue
			}
			prev, tracked := st.Files[path]
			if !tracked {
				continue // not ours to remove
			}
			if prev.Binary {
				// Tracked only to record that read_file will not serve it.
				// It was never on disk, so its absence is not a deletion.
				continue
			}
			d.Delete = append(d.Delete, path)
		}
	}
	return d
}
