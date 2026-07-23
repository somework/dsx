package syncer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestPushDoesNotPruneASubtreeHiddenBehindASymlinkedDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ")
	}
	dir := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.WriteFile(filepath.Join(elsewhere, "Button.tsx"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(dir, "components")); err != nil {
		t.Fatal(err)
	}

	ig, err := loadIgnore(dir)
	if err != nil {
		t.Fatal(err)
	}
	local, err := scanLocal(dir, ig)
	if err != nil {
		t.Fatal(err)
	}
	if lf, ok := local["components"]; !ok || !lf.Irregular {
		t.Fatalf("the link itself was not recorded as irregular: %+v", local)
	}

	remote := map[string]RemoteEntry{
		"components/Button.tsx": {Path: "components/Button.tsx", Etag: "e1", Size: 1},
	}
	st := State{Files: map[string]FileState{
		"components/Button.tsx": {Etag: "e1", Size: 1, SHA: SHA256Hex([]byte("x"))},
	}}

	d := planPush(remote, local, st, nil, nil, forceNone, true)
	if len(d.Delete) != 0 {
		t.Fatalf("push --prune deleted %v from the server; nothing under a symlinked directory "+
			"was ever scanned, so its absence proves nothing", d.Delete)
	}
}

func TestPullDoesNotPruneASubtreeHiddenBehindASymlinkedDirectory(t *testing.T) {
	local := map[string]localFile{"components": {Path: "components", Irregular: true}}
	st := State{Files: map[string]FileState{
		"components/Button.tsx": {Etag: "e1", SHA: "abc"},
	}}
	d := planPull(map[string]RemoteEntry{}, local, st, nil, false, true, false)
	if len(d.Delete) != 0 || len(d.PruneConflicts) != 0 {
		t.Fatalf("pull --prune acted on %v/%v under a symlinked directory it never looked inside",
			d.Delete, d.PruneConflicts)
	}
}

func TestIrregularPathsAreReportedAsThemselvesNotAsAFalseConflict(t *testing.T) {
	rep := PushReport{Irregular: []string{"logo.svg"}}
	out := rep.Render(false)
	if !strings.Contains(out, "logo.svg") {
		t.Fatalf("the path is not named: %q", out)
	}
	if !strings.Contains(out, "regular file") && !strings.Contains(out, "symlink") {
		t.Errorf("the message must say what is actually wrong: %q", out)
	}
	for _, lie := range []string{"server moved ahead", "--force to overwrite"} {
		if strings.Contains(out, lie) {
			t.Errorf("the message claims %q, which is false for a symlink: %q", lie, out)
		}
	}

	pr := PullReport{Irregular: []string{"logo.svg"}}
	pout := pr.Render(false)
	if !strings.Contains(pout, "logo.svg") {
		t.Fatalf("pull does not name it: %q", pout)
	}
	if strings.Contains(pout, "--force to overwrite") {
		t.Errorf("pull claims --force would overwrite a symlink: %q", pout)
	}
}

func TestIrregularPathsDoNotBlockASyncForever(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ")
	}
	dir := t.TempDir()
	mkfile(t, dir, "keep.css", "k")
	target := filepath.Join(t.TempDir(), "shared.svg")
	if err := os.WriteFile(target, []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "logo.svg")); err != nil {
		t.Fatal(err)
	}

	ig, err := loadIgnore(dir)
	if err != nil {
		t.Fatal(err)
	}
	local, err := scanLocal(dir, ig)
	if err != nil {
		t.Fatal(err)
	}
	remote := map[string]RemoteEntry{
		"keep.css": {Path: "keep.css", Etag: "e0", Size: 1},
		"logo.svg": {Path: "logo.svg", Etag: "e1", Size: 6},
	}
	st := State{Files: map[string]FileState{
		"keep.css": {Etag: "e0", Size: 1, SHA: SHA256Hex([]byte("k"))},
		"logo.svg": {Etag: "e1", Size: 6, SHA: SHA256Hex([]byte("<svg/>"))},
	}}

	d := planPush(remote, local, st, nil, nil, forceNone, true)
	if slices.Contains(d.Conflicts, "logo.svg") {
		t.Error("a symlink is reported as a conflict; it is not a disagreement about content, " +
			"and calling it one means exit 3 forever with no way out")
	}
	if !slices.Contains(d.Irregular, "logo.svg") {
		t.Errorf("irregular = %v, want logo.svg named in its own class", d.Irregular)
	}
	if err := (PushReport{Conflicts: d.Conflicts}).Outcome(); err != nil {
		t.Errorf("a directory holding a symlink cannot sync at all: %v", err)
	}
}

func TestNegatedPathUnderAnExcludedDirectoryIsScannedLocally(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "dist/app.js", "generated")
	mkfile(t, dir, "dist/keep.css", "hand written")
	if err := os.WriteFile(filepath.Join(dir, ignoreFileName), []byte("dist/\n!dist/keep.css\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ig, err := loadIgnore(dir)
	if err != nil {
		t.Fatal(err)
	}
	local, err := scanLocal(dir, ig)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := local["dist/keep.css"]; !ok {
		t.Fatalf("!dist/keep.css was excluded from the scan while filterRemote keeps it: "+
			"the two sides disagree, and --prune acts on the difference. scanned: %v", SortedPaths(local))
	}
	if _, ok := local["dist/app.js"]; ok {
		t.Error("dist/app.js was scanned despite dist/")
	}

	remote := filterRemote(map[string]RemoteEntry{
		"dist/app.js":   {Path: "dist/app.js", Etag: "1"},
		"dist/keep.css": {Path: "dist/keep.css", Etag: "2"},
	}, ig)
	if _, ok := remote["dist/keep.css"]; !ok {
		t.Error("filterRemote dropped the negated path")
	}
	if _, ok := remote["dist/app.js"]; ok {
		t.Error("filterRemote kept the excluded path")
	}
}

func TestBuiltInDirectoriesAreStillPrunedFromTheWalk(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "node_modules/pkg/index.js", "x")
	mkfile(t, dir, ".git/objects/ab/cdef", "x")
	mkfile(t, dir, "app.css", "y")
	if err := os.WriteFile(filepath.Join(dir, ignoreFileName), []byte("dist/\n!dist/keep.css\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ig, err := loadIgnore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !ig.canSkipDir("node_modules") {
		t.Error("node_modules is no longer pruned from the walk; a user's `!` rule must not cost that")
	}
	if !ig.canSkipDir(".git") {
		t.Error(".git is no longer pruned from the walk")
	}
	if ig.canSkipDir("dist") {
		t.Error("dist was pruned whole even though a `!` rule re-includes something under it")
	}
}

func TestREADMEsIgnoreExampleIsAcceptedByTheParser(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(readme), "# a comment must be on its own line")
	if start < 0 {
		t.Fatal("the .dsxignore example moved; point this test at it again")
	}
	block := string(readme)[start:]
	block = block[:strings.Index(block, "```")]

	s, err := parseIgnore(block)
	if err != nil {
		t.Fatalf("README's .dsxignore example does not parse: %v", err)
	}
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"dist/app.js", true}, {"a/b/x.map", true}, {"build/out.js", true},
		{"dist/keep.css", false}, {"styles.css", false}, {"pkg/build/x.js", false},
	} {
		if got := s.match(tc.path); got != tc.want {
			t.Errorf("README's example: match(%q) = %v, want %v — the documented rule does nothing",
				tc.path, got, tc.want)
		}
	}
}

func TestStatusJSONStillNamesEveryConflictInTheConflictsField(t *testing.T) {
	rep := PullReport{
		Conflicts:      []string{"hero.css", "scratch.css"},
		PruneConflicts: []string{"scratch.css"},
	}
	var got struct {
		Conflicts      []string `json:"conflicts"`
		PruneConflicts []string `json:"prune_conflicts"`
	}
	if err := json.Unmarshal([]byte(rep.Render(true)), &got); err != nil {
		t.Fatalf("--json is not JSON: %v", err)
	}
	for _, want := range []string{"hero.css", "scratch.css"} {
		if !slices.Contains(got.Conflicts, want) {
			t.Errorf("conflicts = %v, want it to name %q — it is the field that has always meant "+
				"'everything a human must look at'", got.Conflicts, want)
		}
	}
	if !slices.Contains(got.PruneConflicts, "scratch.css") {
		t.Errorf("prune_conflicts = %v, want the discriminator", got.PruneConflicts)
	}
	if slices.Contains(got.PruneConflicts, "hero.css") {
		t.Errorf("prune_conflicts = %v, want only what --force would DELETE", got.PruneConflicts)
	}
}

func TestProseAndJSONAgreeOnHowManyConflictsThereAre(t *testing.T) {
	rep := PullReport{Conflicts: []string{"a.css", "b.css"}, PruneConflicts: []string{"b.css"}}
	prose := rep.Render(false)
	if !strings.Contains(prose, "conflicts 2") {
		t.Errorf("prose = %q, want it to count both", prose)
	}
	var got PullReport
	if err := json.Unmarshal([]byte(rep.Render(true)), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Conflicts) != 2 {
		t.Errorf("json conflicts = %v, prose says 2", got.Conflicts)
	}

	if strings.Count(prose, "b.css") != 1 {
		t.Errorf("b.css is reported twice: %q", prose)
	}
	for _, line := range strings.Split(prose, "\n") {
		if strings.Contains(line, "b.css") && !strings.Contains(strings.ToUpper(line), "DELETE") {
			t.Errorf("the prune-conflict line does not warn about deletion: %q", line)
		}
		if strings.Contains(line, "a.css") && strings.Contains(strings.ToUpper(line), "DELETE") {
			t.Errorf("an overwrite-conflict line warns about deletion: %q", line)
		}
	}
}

func TestPushAssertsAbsenceForAPathTheListingSaysIsGone(t *testing.T) {
	remote := map[string]RemoteEntry{}
	local := map[string]localFile{"a.css": {Path: "a.css", Size: 1, SHA: "new"}}
	st := State{Files: map[string]FileState{"a.css": {Etag: "e-old", SHA: "old"}}}

	d := planPush(remote, local, st, nil, nil, forceNone, false)
	if len(d.Write) != 1 {
		t.Fatalf("write = %+v, want the file re-created", d.Write)
	}
	if got := d.Write[0].IfMatch; got != "0" {
		t.Errorf(`if_match = %q, want "0": the listing says the path does not exist, and "0" is `+
			`the sentinel asserting exactly that`, got)
	}
}
