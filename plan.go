package main

// The sync decisions live here, apart from the transport, because this is
// where a mistake costs data: a wrong call silently overwrites an edit or
// deletes a file nobody asked to delete. Pure input, pure output, testable.

// localCovers reports whether the scan accounts for a remote path.
//
// Exact membership is not enough. A symlinked *directory* is recorded once, at
// the link, because WalkDir does not descend it -- so nothing underneath was
// ever looked at. Reading that silence as "the user deleted every file under
// here" is how one symlink took out a whole server-side subtree, and it is the
// same mechanism the leaf-level fix closed, one directory up.
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

	// Irregular are paths that exist locally but are not regular files. They
	// are not conflicts: a conflict is a disagreement about content that a
	// human resolves, and none of --force, `dsx pull` or `dsx push` resolves a
	// symlink. Reporting them as conflicts meant exit 3 forever, under advice
	// that was false three ways over.
	Irregular []string

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
			// Its own class, not a conflict: nothing the user can type resolves
			// it, so exit 3 would be a demand with no answer.
			d.Irregular = append(d.Irregular, path)
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
		// Iterating what is on disk, not what is tracked, is what makes the
		// symlinked-directory case safe here for free: nothing under the link
		// was ever scanned, so nothing under it can be reached from this loop.
		// planPush needs localCovers because it iterates the server's listing.
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

	// Irregular are paths that exist locally but hold no bytes dsx can send.
	// See pullDecision.Irregular: reported, never a conflict.
	Irregular []string

	// BinaryConflicts are conflicts on paths read_file will not serve. They are
	// conflicts -- a human must choose -- but the ordinary advice is false for
	// them: the server has not moved, and `dsx pull` provably cannot resolve
	// them (planPull classifies the path Binary and fetches nothing), so push
	// conflicts identically forever. Only --force resolves it, and here --force
	// is unrecoverable. Saying so is the whole point of the separate class.
	BinaryConflicts []string
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
			// be named -- dropping it silently is what let prune read it as a
			// deletion -- but it is not a conflict: --force does not resolve a
			// symlink, `dsx pull` does not, and saying otherwise left the sync
			// at exit 3 with no way out.
			if onServer {
				d.Irregular = append(d.Irregular, path)
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
			// most worth refusing, and the one whose advice must be true.
			d.BinaryConflicts = append(d.BinaryConflicts, path)
			continue
		}

		cand := pushCandidate{Path: path}
		// if_match turns a blind overwrite into a checked one: the server
		// refuses the write if the file moved since we last agreed on it.
		if !force {
			// Absence is checked first. A tracked path missing from the listing
			// used to be guarded with its remembered etag, and the server has no
			// row at that etag -- so the write was refused as stale when the file
			// was simply gone. "0" is the sentinel that says "not there".
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
		for _, path := range sortedPaths(remote) {
			// localCovers, not plain membership: an irregular path counts as
			// present, and so does everything beneath a symlinked directory the
			// walk never entered. That is the whole reason scanLocal records
			// irregular paths instead of dropping them.
			if localCovers(local, path) {
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
