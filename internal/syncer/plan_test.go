package syncer

import (
	"slices"
	"testing"
)

func remoteOf(entries ...RemoteEntry) map[string]RemoteEntry {
	m := map[string]RemoteEntry{}
	for _, e := range entries {
		e.Type = "file"
		m[e.Path] = e
	}
	return m
}

func localOf(files ...localFile) map[string]localFile {
	m := map[string]localFile{}
	for _, f := range files {
		m[f.Path] = f
	}
	return m
}

func stateOf(entries map[string]FileState) State {
	if entries == nil {
		entries = map[string]FileState{}
	}
	return State{ProjectID: "p", Files: entries}
}

func TestPlanPull(t *testing.T) {
	t.Run("etag match and sha match costs nothing", func(t *testing.T) {
		d := planPull(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "1", Size: 3}),
			localOf(localFile{Path: "a.css", SHA: "sha1"}),
			stateOf(map[string]FileState{"a.css": {Etag: "1", SHA: "sha1"}}),
			false, false)

		if d.Unchanged != 1 || len(d.Fetch) != 0 {
			t.Errorf("unchanged=%d fetch=%v, want 1 and none", d.Unchanged, d.Fetch)
		}
	})

	t.Run("new etag is fetched", func(t *testing.T) {
		d := planPull(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "2"}),
			localOf(localFile{Path: "a.css", SHA: "sha1"}),
			stateOf(map[string]FileState{"a.css": {Etag: "1", SHA: "sha1"}}),
			false, false)

		if !slices.Equal(d.Fetch, []string{"a.css"}) {
			t.Errorf("fetch=%v, want [a.css]", d.Fetch)
		}
	})

	t.Run("untracked remote file is fetched", func(t *testing.T) {
		d := planPull(
			remoteOf(RemoteEntry{Path: "new.css", Etag: "1"}),
			localOf(), stateOf(nil), false, false)

		if !slices.Equal(d.Fetch, []string{"new.css"}) {
			t.Errorf("fetch=%v, want [new.css]", d.Fetch)
		}
	})

	t.Run("local edit at same etag is a conflict, not an overwrite", func(t *testing.T) {
		d := planPull(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "1"}),
			localOf(localFile{Path: "a.css", SHA: "edited"}),
			stateOf(map[string]FileState{"a.css": {Etag: "1", SHA: "sha1"}}),
			false, false)

		if !slices.Equal(d.Conflicts, []string{"a.css"}) {
			t.Errorf("conflicts=%v, want [a.css]", d.Conflicts)
		}
		if len(d.Fetch) != 0 {
			t.Errorf("fetch=%v, want none — a conflict must not silently overwrite", d.Fetch)
		}
	})

	t.Run("force overrides a local edit", func(t *testing.T) {
		d := planPull(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "1"}),
			localOf(localFile{Path: "a.css", SHA: "edited"}),
			stateOf(map[string]FileState{"a.css": {Etag: "1", SHA: "sha1"}}),
			true, false)

		if !slices.Equal(d.Fetch, []string{"a.css"}) {
			t.Errorf("fetch=%v, want [a.css] under --force", d.Fetch)
		}
	})

	t.Run("untracked local collision is a conflict", func(t *testing.T) {
		d := planPull(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "1"}),
			localOf(localFile{Path: "a.css", SHA: "mine"}),
			stateOf(nil), false, false)

		if !slices.Equal(d.Conflicts, []string{"a.css"}) {
			t.Errorf("conflicts=%v, want [a.css]", d.Conflicts)
		}
	})

	t.Run("known binary at same etag is not re-requested", func(t *testing.T) {
		d := planPull(
			remoteOf(RemoteEntry{Path: "og.png", Etag: "1"}),
			localOf(),
			stateOf(map[string]FileState{"og.png": {Etag: "1", Binary: true}}),
			false, false)

		if !slices.Equal(d.Binary, []string{"og.png"}) {
			t.Errorf("binary=%v, want [og.png]", d.Binary)
		}
		if len(d.Fetch) != 0 {
			t.Errorf("fetch=%v, want none — a known binary must cost no request", d.Fetch)
		}
	})

	t.Run("binary with a new etag is retried", func(t *testing.T) {
		d := planPull(
			remoteOf(RemoteEntry{Path: "og.png", Etag: "2"}),
			localOf(),
			stateOf(map[string]FileState{"og.png": {Etag: "1", Binary: true}}),
			false, false)

		if !slices.Equal(d.Fetch, []string{"og.png"}) {
			t.Errorf("fetch=%v, want [og.png] — a changed etag may no longer be binary", d.Fetch)
		}
	})

	t.Run("prune removes tracked local files gone from the server", func(t *testing.T) {
		d := planPull(
			remoteOf(),
			localOf(localFile{Path: "gone.css", SHA: "s"}),
			stateOf(map[string]FileState{"gone.css": {Etag: "1", SHA: "s"}}),
			false, true)

		if !slices.Equal(d.Delete, []string{"gone.css"}) {
			t.Errorf("delete=%v, want [gone.css]", d.Delete)
		}
	})

	t.Run("prune never touches untracked local files", func(t *testing.T) {
		d := planPull(
			remoteOf(),
			localOf(localFile{Path: "mine.txt", SHA: "s"}),
			stateOf(nil), false, true)

		if len(d.Delete) != 0 {
			t.Errorf("delete=%v, want none — an untracked file was never ours", d.Delete)
		}
	})
}

func TestPlanPush(t *testing.T) {
	t.Run("unchanged file is not sent", func(t *testing.T) {
		d := planPush(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "1"}),
			localOf(localFile{Path: "a.css", SHA: "s"}),
			stateOf(map[string]FileState{"a.css": {Etag: "1", SHA: "s"}}),
			false, false)

		if d.Unchanged != 1 || len(d.Write) != 0 {
			t.Errorf("unchanged=%d write=%v, want 1 and none", d.Unchanged, d.Write)
		}
	})

	t.Run("local edit is sent guarded by if_match", func(t *testing.T) {
		d := planPush(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "1"}),
			localOf(localFile{Path: "a.css", SHA: "edited"}),
			stateOf(map[string]FileState{"a.css": {Etag: "1", SHA: "s"}}),
			false, false)

		if len(d.Write) != 1 || d.Write[0].IfMatch != "1" {
			t.Fatalf("write=%+v, want one entry guarded by etag 1", d.Write)
		}
	})

	t.Run("new file asserts non-existence", func(t *testing.T) {
		d := planPush(
			remoteOf(),
			localOf(localFile{Path: "new.css", SHA: "s"}),
			stateOf(nil), false, false)

		if len(d.Write) != 1 || d.Write[0].IfMatch != "0" {
			t.Fatalf(`write=%+v, want one entry with if_match "0"`, d.Write)
		}
	})

	t.Run("remote moved ahead is a conflict", func(t *testing.T) {
		d := planPush(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "2"}),
			localOf(localFile{Path: "a.css", SHA: "edited"}),
			stateOf(map[string]FileState{"a.css": {Etag: "1", SHA: "s"}}),
			false, false)

		if !slices.Equal(d.Conflicts, []string{"a.css"}) {
			t.Errorf("conflicts=%v, want [a.css]", d.Conflicts)
		}
		if len(d.Write) != 0 {
			t.Errorf("write=%v, want none — must not clobber a newer server copy", d.Write)
		}
	})

	t.Run("force sends unconditionally", func(t *testing.T) {
		d := planPush(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "2"}),
			localOf(localFile{Path: "a.css", SHA: "edited"}),
			stateOf(map[string]FileState{"a.css": {Etag: "1", SHA: "s"}}),
			true, false)

		if len(d.Write) != 1 || d.Write[0].IfMatch != "" {
			t.Fatalf("write=%+v, want one unguarded entry under --force", d.Write)
		}
	})

	t.Run("untracked collision is a conflict", func(t *testing.T) {
		d := planPush(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "1"}),
			localOf(localFile{Path: "a.css", SHA: "mine"}),
			stateOf(nil), false, false)

		if !slices.Equal(d.Conflicts, []string{"a.css"}) {
			t.Errorf("conflicts=%v, want [a.css]", d.Conflicts)
		}
	})

	t.Run("server ahead with no local change is left to pull", func(t *testing.T) {
		d := planPush(
			remoteOf(RemoteEntry{Path: "a.css", Etag: "2"}),
			localOf(localFile{Path: "a.css", SHA: "s"}),
			stateOf(map[string]FileState{"a.css": {Etag: "1", SHA: "s"}}),
			false, false)

		if d.Unchanged != 1 || len(d.Write) != 0 || len(d.Conflicts) != 0 {
			t.Errorf("unchanged=%d write=%v conflicts=%v, want 1/none/none",
				d.Unchanged, d.Write, d.Conflicts)
		}
	})

	t.Run("prune deletes tracked remote files gone locally", func(t *testing.T) {
		d := planPush(
			remoteOf(RemoteEntry{Path: "gone.css", Etag: "1"}),
			localOf(),
			stateOf(map[string]FileState{"gone.css": {Etag: "1", SHA: "s"}}),
			false, true)

		if !slices.Equal(d.Delete, []string{"gone.css"}) {
			t.Errorf("delete=%v, want [gone.css]", d.Delete)
		}
	})

	t.Run("prune must never delete a binary the server would not serve", func(t *testing.T) {
		d := planPush(
			remoteOf(RemoteEntry{Path: "assets/og.png", Etag: "1"}),
			localOf(),
			stateOf(map[string]FileState{"assets/og.png": {Etag: "1", Binary: true}}),
			false, true)

		if len(d.Delete) != 0 {
			t.Fatalf("delete=%v, want none — pruning an unpullable binary destroys it", d.Delete)
		}
	})

	t.Run("prune leaves untracked remote files alone", func(t *testing.T) {
		d := planPush(
			remoteOf(RemoteEntry{Path: "theirs.css", Etag: "1"}),
			localOf(), stateOf(nil), false, true)

		if len(d.Delete) != 0 {
			t.Errorf("delete=%v, want none", d.Delete)
		}
	})
}

func TestSyncStateWithFileIsImmutable(t *testing.T) {
	orig := stateOf(map[string]FileState{"a": {Etag: "1"}})
	next := orig.withFile("b", FileState{Etag: "2"})

	if _, leaked := orig.Files["b"]; leaked {
		t.Error("withFile mutated the receiver")
	}
	if next.Files["a"].Etag != "1" || next.Files["b"].Etag != "2" {
		t.Errorf("copy = %+v, want both entries carried over", next.Files)
	}
}
