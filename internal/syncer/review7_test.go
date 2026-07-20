package syncer

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/somework/dsx/internal/dsxerr"
)

// --- S1: WalkTree termination guard ------------------------------------------

// A list_files reply is untrusted (invariant 7). A directory entry that re-lists
// the root (path "") would drive an unbounded re-walk — every level a real
// network call, goroutines growing until hang/OOM. WalkTree must refuse quickly.
func TestWalkTreeRefusesASelfReferentialRootListing(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor(
			fileEntry("a.md", "e1", 1),
			dirEntry(""), // re-lists the root
		)}
	})
	c := fakeClient(f)

	withTimeout(t, 5*time.Second, "WalkTree", func() error {
		if _, err := WalkTree(context.Background(), c, "p1", 4); err == nil {
			return fmt.Errorf("WalkTree accepted a self-referential root listing instead of refusing it")
		}
		return nil
	})
	if n := f.CountTool("list_files"); n > 4 {
		t.Errorf("list_files called %d times on a self-referential listing; the guard is not bounding recursion", n)
	}
}

// A directory whose listing names itself (child path == the requested dir) is a
// one-step cycle. Without a guard walk(dir) → walk(dir) → … forever.
func TestWalkTreeRefusesADirectoryThatListsItself(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch args["path"] {
		case nil:
			return fakeReply{Text: listingFor(dirEntry("sub"))}
		case "sub":
			return fakeReply{Text: listingFor(
				fileEntry("sub/a.md", "e1", 1),
				dirEntry("sub"), // lists itself
			)}
		}
		return fakeReply{Text: "[]"}
	})
	c := fakeClient(f)

	withTimeout(t, 5*time.Second, "WalkTree", func() error {
		if _, err := WalkTree(context.Background(), c, "p1", 4); err == nil {
			return fmt.Errorf("WalkTree accepted a directory that lists itself")
		}
		return nil
	})
	if n := f.CountTool("list_files"); n > 8 {
		t.Errorf("list_files called %d times; a self-listing directory is not being bounded", n)
	}
}

// A dir entry that does not strictly descend from its parent (points sideways or
// upward) is a cycle vector. WalkTree must refuse it, not follow it.
func TestWalkTreeRefusesAChildThatDoesNotDescendFromItsParent(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch args["path"] {
		case nil:
			return fakeReply{Text: listingFor(dirEntry("a"))}
		case "a":
			return fakeReply{Text: listingFor(dirEntry("b"))} // "b" is not under "a"
		case "b":
			return fakeReply{Text: listingFor(dirEntry("a"))} // cycle back
		}
		return fakeReply{Text: "[]"}
	})
	c := fakeClient(f)

	withTimeout(t, 5*time.Second, "WalkTree", func() error {
		if _, err := WalkTree(context.Background(), c, "p1", 4); err == nil {
			return fmt.Errorf("WalkTree followed a sideways/cyclic directory listing")
		}
		return nil
	})
	if n := f.CountTool("list_files"); n > 8 {
		t.Errorf("list_files called %d times; a cyclic listing is not being bounded", n)
	}
}

// Every directory names a strictly deeper child, forever. Strict descent alone
// would still recurse without end; the depth cap is the backstop that bounds it.
func TestWalkTreeBoundsAnEverDeepeningListing(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		p, _ := args["path"].(string)
		child := "d"
		if p != "" {
			child = p + "/d"
		}
		return fakeReply{Text: listingFor(dirEntry(child))}
	})
	c := fakeClient(f)

	withTimeout(t, 10*time.Second, "WalkTree", func() error {
		if _, err := WalkTree(context.Background(), c, "p1", 4); err == nil {
			return fmt.Errorf("WalkTree walked an infinitely deepening tree without refusing")
		}
		return nil
	})
}

// The guard must not change behaviour for a legitimate deep, wide tree.
func TestWalkTreeWalksALegitimateDeepAndWideTree(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch args["path"] {
		case nil:
			return fakeReply{Text: listingFor(
				fileEntry("root.md", "e0", 1),
				dirEntry("a"), dirEntry("b"),
			)}
		case "a":
			return fakeReply{Text: listingFor(
				fileEntry("a/x.md", "e1", 1),
				dirEntry("a/deep"),
			)}
		case "a/deep":
			return fakeReply{Text: listingFor(fileEntry("a/deep/y.md", "e2", 2))}
		case "b":
			return fakeReply{Text: listingFor(fileEntry("b/z.md", "e3", 3))}
		}
		return fakeReply{Text: "[]"}
	})
	c := fakeClient(f)

	withTimeout(t, 5*time.Second, "WalkTree", func() error {
		files, err := WalkTree(context.Background(), c, "p1", 4)
		if err != nil {
			return err
		}
		want := []string{"a/deep/y.md", "a/x.md", "b/z.md", "root.md"}
		if got := SortedPaths(files); !slices.Equal(got, want) {
			return fmt.Errorf("walked %v, want %v", got, want)
		}
		return nil
	})
}

// --- S2: deletePaths conflict hint on the runtime path -----------------------

// A refused prune-delete surfaces a conflict. On a --force rerun the path is
// deleted, DESTROYING the server's newer copy (invariant 4). The machine hint
// must say so, not tell the agent to pull.
func TestDeletePathsConflictHintWarnsForceWouldDelete(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "finalize_plan":
			return fakeReply{Text: `{"plan_token":"tok"}`}
		case "delete_files":
			return fakeReply{
				IsError: true,
				Text:    `{"conflicts":[{"path":"gone.css","etag":"e9"}],"message":"moved ahead"}`,
			}
		}
		return fakeReply{Text: "[]"}
	})

	st := State{Files: map[string]FileState{"gone.css": {Etag: "e1"}}}
	err := deletePaths(context.Background(), fakeClient(f), "p1", []string{"gone.css"}, st)
	if err == nil {
		t.Fatal("a server refusal on delete was reported as success")
	}
	msg := err.Error()
	if !strings.Contains(strings.ToUpper(msg), "DELETE") {
		t.Errorf("the prune-delete conflict hint does not warn that --force DELETES the server's newer copy: %q", msg)
	}
	if strings.Contains(msg, "pull") {
		t.Errorf("the hint tells the agent to pull, but on a --force rerun planPush DELETES the server's copy: %q", msg)
	}
}

// --- S3: writeBatch must refuse an empty etag (symmetry with ParseEnvelope) ---

// ParseEnvelope requires a non-empty etag before reassembling a read. The write
// side must be symmetric: an empty etag pinned into the ledger reads as
// "unchanged" (empty-vs-empty) next run. Refuse it, do not silently record it.
func TestWriteBatchRefusesAnEmptyEtag(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "finalize_plan":
			return fakeReply{Text: `{"plan_token":"tok"}`}
		case "write_files":
			return fakeReply{Text: `{"etags":{"a.css":""},"written":1}`}
		}
		return fakeReply{Text: "[]"}
	})

	batch := []writeSpec{{
		Path:     "a.css",
		Data:     base64.StdEncoding.EncodeToString([]byte("hi")),
		Encoding: "base64",
	}}
	st := State{Files: map[string]FileState{}}
	var rep PushReport

	err := writeBatch(context.Background(), fakeClient(f), "p1", batch, &st, &rep)
	if err == nil {
		t.Fatal(`an empty etag was accepted; "" is now pinned in the ledger and next run reads empty-vs-empty as unchanged`)
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindProtocol {
		t.Errorf("classified %q, want %q", got, dsxerr.KindProtocol)
	}
	if fst, recorded := st.Files["a.css"]; recorded {
		t.Errorf("an empty-etag write was recorded into the ledger: %+v", fst)
	}
	if slices.Contains(rep.Written, "a.css") {
		t.Errorf("an empty-etag write was reported as written: %v", rep.Written)
	}
}

// --- S4: shared safety-property parity between planPush and planPull ----------

// parityScenario is one path exercising a distinct combination of on-remote,
// on-local, tracked, moved, and irregular. The matrix is deterministic — no
// randomness — so a future re-drift of a prune/irregular guard fails here.
type parityScenario struct {
	path        string
	onRemote    bool
	remoteEtag  string
	onLocal     bool
	localSHA    string
	localIrreg  bool
	tracked     bool
	stateEtag   string
	stateSHA    string
	stateBinary bool
}

func buildParityInputs(scen []parityScenario) (map[string]RemoteEntry, map[string]localFile, State) {
	remote := map[string]RemoteEntry{}
	local := map[string]localFile{}
	files := map[string]FileState{}
	for _, s := range scen {
		if s.onRemote {
			remote[s.path] = RemoteEntry{Path: s.path, Type: "file", Etag: s.remoteEtag, Size: 1}
		}
		if s.onLocal {
			if s.localIrreg {
				local[s.path] = localFile{Path: s.path, Irregular: true}
			} else {
				local[s.path] = localFile{Path: s.path, SHA: s.localSHA, Size: 1}
			}
		}
		if s.tracked {
			files[s.path] = FileState{Etag: s.stateEtag, SHA: s.stateSHA, Binary: s.stateBinary}
		}
	}
	return remote, local, State{ProjectID: "p", Files: files}
}

// parityMatrix covers the cross-product corners that the prune (#2a) and
// irregular (#4) guards protect.
func parityMatrix() []parityScenario {
	return []parityScenario{
		// both sides agree, ledger matches: Unchanged on both.
		{path: "unchanged.css", onRemote: true, remoteEtag: "e1", onLocal: true, localSHA: "s1", tracked: true, stateEtag: "e1", stateSHA: "s1"},
		// local edited at same server etag.
		{path: "local-edit.css", onRemote: true, remoteEtag: "e1", onLocal: true, localSHA: "sX", tracked: true, stateEtag: "e1", stateSHA: "s1"},
		// server moved, local untouched.
		{path: "remote-moved.css", onRemote: true, remoteEtag: "e2", onLocal: true, localSHA: "s1", tracked: true, stateEtag: "e1", stateSHA: "s1"},
		// both moved.
		{path: "both-moved.css", onRemote: true, remoteEtag: "e2", onLocal: true, localSHA: "sX", tracked: true, stateEtag: "e1", stateSHA: "s1"},
		// tracked, gone locally, server moved ahead of the ledger: the #2a case.
		{path: "remote-moved-gone-local.css", onRemote: true, remoteEtag: "e2", onLocal: false, tracked: true, stateEtag: "e1", stateSHA: "s1"},
		// tracked, gone locally, server still at the ledger etag: a clean prune.
		{path: "remote-still-gone-local.css", onRemote: true, remoteEtag: "e1", onLocal: false, tracked: true, stateEtag: "e1", stateSHA: "s1"},
		// tracked, gone from server, local untouched: a clean pull-prune.
		{path: "local-still-gone-remote.css", onRemote: false, onLocal: true, localSHA: "s1", tracked: true, stateEtag: "e1", stateSHA: "s1"},
		// tracked, gone from server, local edited: the pull-prune conflict case.
		{path: "local-edit-gone-remote.css", onRemote: false, onLocal: true, localSHA: "sX", tracked: true, stateEtag: "e1", stateSHA: "s1"},
		// irregular locally and present on server.
		{path: "irregular-both.css", onRemote: true, remoteEtag: "e1", onLocal: true, localIrreg: true, tracked: true, stateEtag: "e1", stateSHA: "s1"},
		// irregular locally, never on server: the #4 local-only case.
		{path: "irregular-local-only.css", onRemote: false, onLocal: true, localIrreg: true},
		// tracked binary gone locally: must never be pruned (unpullable).
		{path: "binary-gone-local.png", onRemote: true, remoteEtag: "e1", onLocal: false, tracked: true, stateEtag: "e1", stateSHA: "s1", stateBinary: true},
		// The shape a FORCED push leaves behind: binary:true carried through
		// beside a real SHA, and the local file still matching it, so the sha
		// check sees equality.
		{path: "binary-forced-push.png", onRemote: false, onLocal: true, localSHA: "s1", tracked: true, stateEtag: "e1", stateSHA: "s1", stateBinary: true},
		// tracked binary gone from the server, a file sitting at that path
		// locally.
		{path: "binary-gone-remote.png", onRemote: false, onLocal: true, localSHA: "user-authored", tracked: true, stateEtag: "e1", stateBinary: true},
		// untracked remote-only file.
		{path: "untracked-remote.css", onRemote: true, remoteEtag: "e1"},
		// untracked local file colliding with a remote path.
		{path: "untracked-collision.css", onRemote: true, remoteEtag: "e1", onLocal: true, localSHA: "mine"},
	}
}

func pushBuckets(d pushDecision) map[string][]string {
	writes := make([]string, 0, len(d.Write))
	for _, w := range d.Write {
		writes = append(writes, w.Path)
	}
	return map[string][]string{
		"Write":          writes,
		"Conflicts":      d.Conflicts,
		"BinaryConfl":    d.BinaryConflicts,
		"PruneConflicts": d.PruneConflicts,
		"Delete":         d.Delete,
		"Irregular":      d.Irregular,
	}
}

func pullBuckets(d pullDecision) map[string][]string {
	return map[string][]string{
		"Fetch":          d.Fetch,
		"Binary":         d.Binary,
		"Conflicts":      d.Conflicts,
		"PruneConflicts": d.PruneConflicts,
		"Delete":         d.Delete,
		"Irregular":      d.Irregular,
	}
}

func TestPlannerParitySharedSafetyProperties(t *testing.T) {
	scen := parityMatrix()
	remote, local, st := buildParityInputs(scen)

	for _, force := range []bool{false, true} {
		for _, prune := range []bool{false, true} {
			name := fmt.Sprintf("force=%v/prune=%v", force, prune)
			t.Run(name, func(t *testing.T) {
				push := planPush(remote, local, st, force, prune)
				pull := planPull(remote, local, st, force, prune)
				pb := pushBuckets(push)
				lb := pullBuckets(pull)

				// P1 prune-safety: without --force, a scheduled Delete must have
				// its surviving side unchanged versus the ledger. For push the
				// survivor is the server (etag); for pull it is local (sha).
				// Reverting the #2a etag guard schedules a moved-ahead remote for
				// deletion and trips this.
				if prune && !force {
					for _, p := range pb["Delete"] {
						if remote[p].Etag != st.Files[p].Etag {
							t.Errorf("planPush deletes %q whose server etag %q != ledger etag %q (a moved-ahead copy would be destroyed)",
								p, remote[p].Etag, st.Files[p].Etag)
						}
					}
					for _, p := range lb["Delete"] {
						if local[p].SHA != st.Files[p].SHA {
							t.Errorf("planPull deletes %q whose local sha %q != ledger sha %q (an edited copy would be destroyed)",
								p, local[p].SHA, st.Files[p].SHA)
						}
					}
				}

				// P2 irregular accountability: a local irregular file must never
				// be scheduled for network work and must be recorded in Irregular
				// whenever the planner is responsible for it. Reverting the #4
				// guard silently drops the local-only irregular from every field.
				for _, s := range scen {
					if !s.onLocal || !s.localIrreg {
						continue
					}
					if !slices.Contains(pb["Irregular"], s.path) {
						t.Errorf("planPush dropped local irregular %q from Irregular (silently skipped, no trace)", s.path)
					}
					if slices.Contains(pb["Write"], s.path) {
						t.Errorf("planPush scheduled a write for local irregular %q", s.path)
					}
					// planPull iterates the remote, so it only accounts for an
					// irregular that is also present on the server.
					if s.onRemote {
						if !slices.Contains(lb["Irregular"], s.path) {
							t.Errorf("planPull dropped irregular %q (present on server) from Irregular", s.path)
						}
						if slices.Contains(lb["Fetch"], s.path) {
							t.Errorf("planPull scheduled a fetch over local irregular %q", s.path)
						}
					}
				}

				// P4 binary non-prunability (invariant 4): neither planner may
				// schedule a tracked binary path for deletion, and unlike the
				// etag/sha guards this one is unconditional — --force does not
				// unlock it.
				for _, s := range scen {
					if !s.tracked || !s.stateBinary {
						continue
					}
					if slices.Contains(pb["Delete"], s.path) {
						t.Errorf("planPush schedules binary-marked %q for deletion", s.path)
					}
					if slices.Contains(lb["Delete"], s.path) {
						t.Errorf("planPull schedules binary-marked %q for deletion", s.path)
					}
				}

				// P3 no double-classification: a path lands in at most one bucket
				// per planner. A planner that both writes and deletes (or conflicts
				// and deletes) the same path is incoherent and unsafe.
				assertDisjoint(t, "planPush", pb)
				assertDisjoint(t, "planPull", lb)
			})
		}
	}
}

func assertDisjoint(t *testing.T, who string, buckets map[string][]string) {
	t.Helper()
	names := make([]string, 0, len(buckets))
	for k := range buckets {
		names = append(names, k)
	}
	slices.Sort(names)
	seen := map[string]string{}
	for _, name := range names {
		for _, p := range buckets[name] {
			if prev, dup := seen[p]; dup {
				t.Errorf("%s classified %q into both %s and %s", who, p, prev, name)
			}
			seen[p] = name
		}
	}
}
