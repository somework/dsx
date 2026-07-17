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

func TestSurveySidesCannotDisagree(t *testing.T) {
	for _, rules := range []string{
		"dist/\n",
		"dist/\n!dist/keep.css\n",
		"*.log\n",
		"**/node_modules/\n",
		"",
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
