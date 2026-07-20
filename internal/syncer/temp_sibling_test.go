package syncer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// save() writes through os.CreateTemp(dir, legacyStateFileName+".*") and probes the
// filesystem through os.MkdirTemp(dir, caseProbeName+"-fold-*"). A kill between
// create and rename leaves the sibling behind. The builtin patterns are
// compiled anchored, so a bare name never matched these — and an unmatched
// leftover is an untracked local file that the next push uploads. For the
// ledger sibling that means dsx's own project id and full file map land in the
// design project.
func TestLedgerAndProbeLeftoversAreIgnoredLocally(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kept.css"), []byte("a{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		legacyStateFileName + ".2841913057",
		legacyStateFileName + ".tmp",
		caseProbeName,
		caseProbeName + "-fold-993421",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ig, err := parseIgnore("")
	if err != nil {
		t.Fatal(err)
	}
	local, err := scanLocal(dir, ig)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := local["kept.css"]; !ok {
		t.Fatal("kept.css missing — the test scanned nothing")
	}
	for path := range local {
		if path == "kept.css" {
			continue
		}
		t.Errorf("scanLocal returned %q; a dsx-internal leftover would be pushed as a new file", path)
	}
}

// The same names arriving from the server must be refused, not written: a
// remote file called .dsx-state.json.tmp lands beside the ledger and is
// indistinguishable from our own leftover on the next run.
func TestCheckRemotePathRefusesLedgerAndProbeSiblings(t *testing.T) {
	for _, rel := range []string{
		legacyStateFileName,
		legacyStateFileName + ".2841913057",
		strings.ToUpper(legacyStateFileName) + ".tmp",
		caseProbeName + "-fold-1",
		"nested/" + legacyStateFileName + ".tmp",
		".git/config",
		"node_modules/x/index.js",
	} {
		t.Run(rel, func(t *testing.T) {
			if err := checkRemotePath(rel); err == nil {
				t.Errorf("checkRemotePath(%q) allowed a dsx-internal or local-only path", rel)
			}
		})
	}
}

func TestCheckRemotePathStillAllowsOrdinaryPaths(t *testing.T) {
	for _, rel := range []string{
		"tokens.css",
		"type/scale.css",
		"docs/.dsx-notes.md",
		"a/b/c.json",
	} {
		t.Run(rel, func(t *testing.T) {
			if err := checkRemotePath(rel); err != nil {
				t.Errorf("checkRemotePath(%q) refused an ordinary path: %v", rel, err)
			}
		})
	}
}

// filterRemote is the invariant-9 half: a path hidden locally must also leave
// the server listing, or push --prune reads its absence as a user deletion.
func TestFilterRemoteDropsLedgerSiblings(t *testing.T) {
	ig, err := parseIgnore("")
	if err != nil {
		t.Fatal(err)
	}
	remote := remoteOf(
		RemoteEntry{Path: "tokens.css", Etag: "e1"},
		RemoteEntry{Path: legacyStateFileName + ".tmp", Etag: "e2"},
		RemoteEntry{Path: caseProbeName + "-fold-7", Etag: "e3"},
	)
	out := filterRemote(remote, ig)
	if _, ok := out["tokens.css"]; !ok {
		t.Error("filterRemote dropped an ordinary path")
	}
	if len(out) != 1 {
		t.Errorf("filterRemote kept %v, want only tokens.css", SortedPaths(out))
	}
}

// Both consumers of builtinIgnores must read it the same way. parseIgnore
// compiles globs; isBuiltinIgnoredName compared literals, so a glob entry would
// silently stop guarding checkRemotePath.
func TestBothConsumersOfBuiltinIgnoresAgree(t *testing.T) {
	ig, err := parseIgnore("")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		".git", "node_modules", ".DS_Store",
		legacyStateFileName, legacyStateFileName + ".55", ignoreFileName,
		caseProbeName, caseProbeName + "-fold-2",
	} {
		t.Run(name, func(t *testing.T) {
			viaGlob := ig.match(name)
			viaName := isBuiltinIgnoredName(name)
			if viaGlob != viaName {
				t.Errorf("ignoreSet.match=%v but isBuiltinIgnoredName=%v for %q — "+
					"one list, two readings", viaGlob, viaName, name)
			}
		})
	}
}
