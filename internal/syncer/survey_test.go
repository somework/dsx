package syncer

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Invariant 9: .dsxignore filters both sides, never one.
//
// The failure mode is asymmetric and quiet. A path dropped from the local scan
// but left in the server's listing is indistinguishable from a file the user
// deleted, so `push --prune` deletes it from the server: data loss produced by
// the very file whose purpose is to say "leave this alone".
//
// Pull and Push each used to spell the pairing out by hand -- loadIgnore,
// then filterRemote, then scanLocal, three calls apiece, each with its own
// comment explaining why the second and third must agree. That is a rule two
// callers must remember. survey makes it a property one supplier guarantees:
// there is no second call to forget, and no *ignoreSet in the caller's hands to
// pass to only one side.

func TestSurveyFiltersBothSides(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".dsxignore"), []byte("dist/\n*.log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"keep.md", "dist/app.js", "debug.log"} {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// The server's listing carries the same ignored paths. If survey filtered
	// only the disk, these would survive here and read as local deletions.
	remote := map[string]RemoteEntry{
		"keep.md":     fileEntry("keep.md", "e1", 1),
		"dist/app.js": fileEntry("dist/app.js", "e2", 1),
		"debug.log":   fileEntry("debug.log", "e3", 1),
	}

	gotRemote, gotLocal, err := survey(dir, remote)
	if err != nil {
		t.Fatalf("survey: %v", err)
	}

	for _, p := range []string{"dist/app.js", "debug.log"} {
		if _, ok := gotRemote[p]; ok {
			t.Errorf("survey left ignored %q in the listing: push --prune would delete it from the server", p)
		}
		if _, ok := gotLocal[p]; ok {
			t.Errorf("survey left ignored %q in the scan", p)
		}
	}
	if _, ok := gotRemote["keep.md"]; !ok {
		t.Error("survey dropped keep.md from the listing")
	}
	if _, ok := gotLocal["keep.md"]; !ok {
		t.Error("survey dropped keep.md from the scan")
	}
}

// TestSurveySidesCannotDisagree is the point of the fusion: whatever .dsxignore
// says, the two sides must hide exactly the same set. A path present on one side
// and absent from the other is the input planPull/planPush read as a deletion.
func TestSurveySidesCannotDisagree(t *testing.T) {
	for _, rules := range []string{
		"dist/\n",
		"dist/\n!dist/keep.css\n",
		"*.log\n",
		"**/node_modules/\n",
		"", // no rules: built-ins only
	} {
		t.Run(rules, func(t *testing.T) {
			dir := t.TempDir()
			if rules != "" {
				if err := os.WriteFile(filepath.Join(dir, ".dsxignore"), []byte(rules), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			paths := []string{"a.md", "dist/app.js", "dist/keep.css", "x.log", "node_modules/p/i.js"}
			remote := map[string]RemoteEntry{}
			for _, p := range paths {
				full := filepath.Join(dir, p)
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				remote[p] = fileEntry(p, "e", 1)
			}

			gotRemote, gotLocal, err := survey(dir, remote)
			if err != nil {
				t.Fatalf("survey: %v", err)
			}
			for _, p := range paths {
				_, inRemote := gotRemote[p]
				_, inLocal := gotLocal[p]
				if inRemote != inLocal {
					t.Errorf("%q: listing=%v scan=%v — the two sides disagree, and --prune reads that difference as a deletion",
						p, inRemote, inLocal)
				}
			}
		})
	}
}

// TestSyncCallersCannotFilterOneSide is invariant 9's structural guard: nothing
// in the shipped binary but survey may name the three filtering primitives.
//
// It scans EVERY non-test file rather than a list of known callers, and bans the
// idents in every function except survey itself. An earlier version took the
// obvious shape -- parse "pull.go" and "push.go", look for FuncDecls named
// Pull and Push -- and three ordinary refactoring moves defeated it while
// a live one-sided filter shipped and the suite stayed green:
//
//	move Push to push_run.go    the file list no longer covers it
//	rename Pull                 the name lookup matches nothing
//	hoist the calls into a helper  the inspect stops at the FuncDecl boundary
//
// Each failed open, because a guard reporting only `if len(found) > 0` cannot
// tell "nothing forbidden here" from "I looked in the wrong place". Hence the
// sentinel below: if the guard cannot find the one function it expects to find,
// it has lost its bearings and says so instead of passing.
//
// This is a structural test rather than a package boundary because the whole
// binary is one package -- see CLAUDE.md on why splitting ignore.go from
// state.go would break the invariant it serves. And it is only half the guard:
// syntax cannot see `_, local, err := survey(...)`, which names nothing
// forbidden and still breaks the invariant. prune_ignore_test.go covers that by
// asking what actually reached delete_files.
func TestSyncCallersCannotFilterOneSide(t *testing.T) {
	banned := map[string]bool{"filterRemote": true, "scanLocal": true, "loadIgnore": true}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	surveyFound := false
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			// survey is where the pairing is supposed to live.
			if fn.Name.Name == "survey" {
				surveyFound = true
				continue
			}

			var found []string
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok && banned[id.Name] {
						found = append(found, id.Name)
					}
				}
				return true
			})
			if len(found) > 0 {
				t.Errorf("%s.%s calls %v directly; only survey may. "+
					"Hand-assembling the pairing is how one side gets filtered and the other "+
					"does not, and --prune reads that difference as a deletion.",
					name, fn.Name.Name, found)
			}
		}
	}

	if !surveyFound {
		t.Fatal("survey was not found in any non-test file: this guard cannot tell " +
			"'nothing forbidden' from 'I looked in the wrong place', so it refuses to pass")
	}
}
