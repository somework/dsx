package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// Invariant 9: .dsxignore filters both sides, never one.
//
// The failure mode is asymmetric and quiet. A path dropped from the local scan
// but left in the server's listing is indistinguishable from a file the user
// deleted, so `push --prune` deletes it from the server: data loss produced by
// the very file whose purpose is to say "leave this alone".
//
// runPull and runPush each used to spell the pairing out by hand -- loadIgnore,
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
	remote := map[string]remoteEntry{
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
			remote := map[string]remoteEntry{}
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

// TestSyncCallersCannotFilterOneSide is invariant 9's structural guard.
//
// The behavioural tests above prove survey filters both sides; they cannot prove
// nobody bypasses it. This one reads runPull's and runPush's own syntax and
// refuses the three calls whose hand-assembly was the original hazard. It is an
// AST test rather than a package boundary because the whole binary is one
// package -- see CLAUDE.md on why splitting ignore.go from state.go would break
// the invariant it serves.
func TestSyncCallersCannotFilterOneSide(t *testing.T) {
	const forbidden = "filterRemote, scanLocal or loadIgnore"
	banned := map[string]bool{"filterRemote": true, "scanLocal": true, "loadIgnore": true}

	for _, fn := range []struct{ file, name string }{
		{"pull.go", "runPull"},
		{"push.go", "runPush"},
	} {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, fn.file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", fn.file, err)
		}

		var found []string
		ast.Inspect(f, func(n ast.Node) bool {
			decl, ok := n.(*ast.FuncDecl)
			if !ok || decl.Name.Name != fn.name {
				return true
			}
			ast.Inspect(decl.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && banned[id.Name] {
					found = append(found, id.Name)
				}
				return true
			})
			return false
		})

		if len(found) > 0 {
			t.Errorf("%s calls %v directly; it must go through survey. "+
				"Hand-assembling %s is how one side gets filtered and the other does not, "+
				"and --prune reads that difference as a deletion.", fn.name, found, forbidden)
		}
	}
}
