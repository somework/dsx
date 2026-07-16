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
}

// planPull decides, without touching the network, what a pull would do.
func planPull(remote map[string]remoteEntry, local map[string]localFile, st syncState, force, prune bool) pullDecision {
	var d pullDecision

	for _, path := range sortedPaths(remote) {
		r := remote[path]
		prev, tracked := st.Files[path]
		onDisk, present := local[path]

		switch {
		case tracked && prev.Etag == r.Etag && prev.Binary:
			// Known-unreadable and unchanged since we learned that.
			d.Binary = append(d.Binary, path)
		case tracked && prev.Etag == r.Etag && present && onDisk.SHA == prev.SHA:
			d.Unchanged++
		case tracked && prev.Etag == r.Etag && present && onDisk.SHA != prev.SHA && !force:
			// Server unchanged, local edited: overwriting would destroy the edit.
			d.Conflicts = append(d.Conflicts, path)
		case !tracked && present && !force:
			// No common ancestor, so we cannot tell which side is newer.
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
			if _, tracked := st.Files[path]; !tracked {
				continue // never ours; leave it alone
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
