package syncer

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func mkfile(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustParseIgnore(t *testing.T, text string) *ignoreSet {
	t.Helper()
	s, err := parseIgnore(text)
	if err != nil {
		t.Fatalf("parseIgnore: %v", err)
	}
	return s
}

func TestIgnoreBuiltInsHoldWithNoFile(t *testing.T) {
	s := mustParseIgnore(t, "")
	for _, p := range []string{
		".git/config", "node_modules/x/index.js", ".DS_Store",
		"a/b/.git/HEAD", "deep/node_modules/pkg/p.js", oldStateFileName,
	} {
		if !s.match(p) {
			t.Errorf("built-in exclusion lost: %q is not ignored", p)
		}
	}
	for _, p := range []string{"styles.css", "src/app.js", "gitignore.md"} {
		if s.match(p) {
			t.Errorf("%q must not be ignored", p)
		}
	}
}

func TestBuiltinDsxIsUnanchoredAndSkipsTheSubtree(t *testing.T) {
	s := mustParseIgnore(t, "")
	var re *regexp.Regexp
	for _, r := range s.rules[:s.builtins] {
		if r.source == ".dsx" {
			re = r.re
		}
	}
	if re == nil {
		t.Fatal(`no builtin rule with source ".dsx"`)
	}
	if want := `(?i)^(?:.*/)?\.dsx$`; re.String() != want {
		t.Errorf("compiled .dsx pattern = %q, want %q", re.String(), want)
	}
	if !s.canSkipDir(".dsx") {
		t.Error(`canSkipDir(".dsx") = false, want true`)
	}
	if !s.canSkipDir("a/b/.dsx") {
		t.Error(`canSkipDir("a/b/.dsx") = false, want true`)
	}
}

func TestIgnoreBuiltInsCannotBeNegated(t *testing.T) {
	s := mustParseIgnore(t, "!.git\n!"+oldStateFileName+"\n!node_modules\n")
	for _, p := range []string{".git/config", oldStateFileName, "node_modules/x.js"} {
		if !s.match(p) {
			t.Errorf("%q was un-ignored by a user rule; built-ins are not negotiable", p)
		}
	}
}

func TestIgnoreCommentsAndBlanksAreNotPatterns(t *testing.T) {
	s := mustParseIgnore(t, "# a comment\n\n   \ndist\n")
	if s.match("# a comment") || s.match("") {
		t.Error("a comment or a blank line became a pattern")
	}
	if !s.match("dist/app.js") {
		t.Error("dist was not applied")
	}
}

func TestIgnoreBareNameMatchesAtAnyDepth(t *testing.T) {
	s := mustParseIgnore(t, "build\n")
	for _, p := range []string{"build/x.js", "a/build/x.js", "a/b/build"} {
		if !s.match(p) {
			t.Errorf("%q should match the bare name rule", p)
		}
	}
	if s.match("rebuild/x.js") {
		t.Error("a bare name must match a whole segment, not a substring")
	}
}

func TestIgnoreAnchoredPatternOnlyMatchesAtTheRoot(t *testing.T) {
	s := mustParseIgnore(t, "/dist\n")
	if !s.match("dist/app.js") {
		t.Error("anchored rule missed the root")
	}
	if s.match("pkg/dist/app.js") {
		t.Error("anchored rule leaked to a nested directory")
	}
}

func TestIgnoreDirOnlyRuleSpareTheFileOfTheSameName(t *testing.T) {
	s := mustParseIgnore(t, "cache/\n")
	if !s.match("cache/x.bin") {
		t.Error("cache/ should exclude what is under it")
	}
	if s.match("cache") {
		t.Error("a trailing slash means directories only; the file `cache` must survive")
	}
}

func TestIgnoreGlobsStayWithinOneSegmentUnlessDoubled(t *testing.T) {
	s := mustParseIgnore(t, "*.map\nsrc/**/gen.js\n")
	if !s.match("a/b/app.js.map") {
		t.Error("*.map should match at any depth")
	}
	if !s.match("src/gen.js") || !s.match("src/a/b/gen.js") {
		t.Error("** should span zero or more segments")
	}
	if s.match("other/gen.js") {
		t.Error("src/**/gen.js leaked outside src")
	}
}

func TestIgnoreLastMatchingRuleWins(t *testing.T) {
	s := mustParseIgnore(t, "dist/\n!dist/keep.css\n")
	if !s.match("dist/app.js") {
		t.Error("dist/ stopped applying")
	}
	if s.match("dist/keep.css") {
		t.Error("a later negation must re-include the path")
	}
}

func TestIgnoreRejectsAPatternItCannotHonour(t *testing.T) {
	if _, err := parseIgnore("[\n"); err == nil {
		t.Fatal("a malformed pattern was accepted; the user would think it applied")
	}
}

func TestLoadIgnoreWithNoFileIsNotAnError(t *testing.T) {
	s, err := loadIgnore(t.TempDir())
	if err != nil {
		t.Fatalf("a missing .dsxignore must be fine: %v", err)
	}
	if !s.match(".git/config") {
		t.Error("built-ins must still apply with no file present")
	}
}

func TestLoadIgnoreReadsTheFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ignoreFileName), []byte("dist/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := loadIgnore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !s.match("dist/app.js") {
		t.Error(".dsxignore was not read")
	}
}

func TestIgnoreFileItselfIsNotSynced(t *testing.T) {
	s := mustParseIgnore(t, "")
	if !s.match(ignoreFileName) {
		t.Error(".dsxignore must exclude itself")
	}
}

func TestScanLocalHonoursDsxignore(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "keep.css", "a")
	mkfile(t, dir, "dist/app.js", "b")
	mkfile(t, dir, "notes.map", "c")
	if err := os.WriteFile(filepath.Join(dir, ignoreFileName), []byte("dist/\n*.map\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ig, err := loadIgnore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := scanLocal(dir, ig)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["keep.css"]; !ok {
		t.Error("keep.css was dropped")
	}
	for _, p := range []string{"dist/app.js", "notes.map", ignoreFileName} {
		if _, ok := got[p]; ok {
			t.Errorf("%q was scanned despite being ignored", p)
		}
	}
}

func TestFilterRemoteDropsIgnoredPaths(t *testing.T) {
	ig := mustParseIgnore(t, "dist/\n")
	remote := map[string]RemoteEntry{
		"styles.css":  {Path: "styles.css", Etag: "1"},
		"dist/app.js": {Path: "dist/app.js", Etag: "2"},
	}
	got := filterRemote(remote, ig)
	if _, ok := got["dist/app.js"]; ok {
		t.Fatal("an ignored path survived into the remote map; --prune would delete it upstream")
	}
	if _, ok := got["styles.css"]; !ok {
		t.Fatal("a tracked path was dropped")
	}
	if _, ok := remote["dist/app.js"]; !ok {
		t.Error("filterRemote mutated its input")
	}
}

func TestIgnoredPathIsNeverPrunedFromTheServer(t *testing.T) {
	ig := mustParseIgnore(t, "dist/\n")
	remote := filterRemote(map[string]RemoteEntry{
		"dist/app.js": {Path: "dist/app.js", Etag: "2"},
	}, ig)
	local := map[string]localFile{}
	st := State{Files: map[string]FileState{
		"dist/app.js": {Etag: "2", SHA: "abc", Size: 3},
	}}

	d := planPush(remote, local, st, nil, nil, forceNone, true)
	for _, p := range d.Delete {
		if p == "dist/app.js" {
			t.Fatal("push --prune deleted a merely-ignored file from the server")
		}
	}
}

func TestIgnoredPathIsNeverPrunedFromDisk(t *testing.T) {
	ig := mustParseIgnore(t, "dist/\n")
	remote := filterRemote(map[string]RemoteEntry{}, ig)
	local := map[string]localFile{}
	st := State{Files: map[string]FileState{"dist/app.js": {Etag: "2", SHA: "abc"}}}

	d := planPull(remote, local, st, nil, false, true)
	for _, p := range d.Delete {
		if p == "dist/app.js" {
			t.Fatal("pull --prune deleted an ignored file from disk")
		}
	}
}
