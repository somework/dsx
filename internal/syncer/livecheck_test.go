package syncer

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The live suite is invisible to CI: `//go:build live` means `go test ./...`
// never compiles live_test.go, so no mutation inside it fails anything. This
// file reads that source as bytes — a build tag does not stop a parser — and
// enforces the one property a parser can see. It is a floor, not a ceiling: it
// catches a live test that asserts NOTHING, not one assertion weakened among
// several. Only moving a decision out of the live half into a pure function
// CI actually runs closes that.
func assertionlessLiveTests(src []byte) (bad []string, seen int, err error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "live_test.go", src, 0)
	if err != nil {
		return nil, 0, err
	}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || !strings.HasPrefix(fn.Name.Name, "TestLive") || fn.Body == nil {
			continue
		}
		seen++
		if !reachesAnAssertion(fn.Body) {
			bad = append(bad, fn.Name.Name)
		}
	}
	return bad, seen, nil
}

func reachesAnAssertion(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "Error", "Errorf", "Fatal", "Fatalf":
			found = true
			return false
		}
		return true
	})
	return found
}

// liveSources is every file the tag hides, not one file by name. Reading only
// "live_test.go" was right while there was one; a second live file added
// beside it would have been outside the floor entirely, which is the failure
// this guard exists to prevent happening to a test.
func liveSources(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob("live*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no live sources found; the glob matched nothing and this guard proved nothing")
	}
	sort.Strings(paths)
	return paths
}

func TestEveryLiveTestReachesAnAssertion(t *testing.T) {
	total := 0
	for _, path := range liveSources(t) {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		bad, seen, err := assertionlessLiveTests(src)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		total += seen
		if len(bad) > 0 {
			t.Errorf("%s: live tests that never reach an assertion: %v — CI cannot run them, so an assertionless one is dead weight nothing else will catch", path, bad)
		}
	}
	if total == 0 {
		t.Fatal("no TestLive functions found; the walker matched nothing and this guard proved nothing")
	}
}

// The glob has to actually reach the file this commit added, or it is the
// by-name read again wearing a wildcard.
func TestTheLiveFloorCoversEveryLiveSource(t *testing.T) {
	got := liveSources(t)
	for _, want := range []string{"live_test.go", "livereply_test.go"} {
		if !slices.Contains(got, want) {
			t.Errorf("%s is outside the floor; found %v", want, got)
		}
	}
}

// The paired positive control: the guard above is satisfied by a walker that
// finds nothing, so the detector is run against a source that is known to
// contain exactly one offender.
func TestTheLiveWalkerDetectsAnAssertionlessTest(t *testing.T) {
	const src = `package syncer

func TestLiveAsserts(t *testing.T) {
	if got != want {
		t.Fatalf("boom %v", got)
	}
}

func TestLiveOnlyLogs(t *testing.T) {
	if a == b {
		t.Logf("VERDICT: %v", a)
	}
}

func TestLiveAssertsInASubtest(t *testing.T) {
	t.Run("sub", func(t *testing.T) {
		t.Errorf("boom")
	})
}

func NotATestLiveFunc(t *testing.T) {
	t.Logf("ignored")
}
`
	bad, seen, err := assertionlessLiveTests([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if seen != 3 {
		t.Errorf("seen = %d, want 3 — only TestLive* functions count", seen)
	}
	if !slices.Equal(bad, []string{"TestLiveOnlyLogs"}) {
		t.Errorf("bad = %v, want [TestLiveOnlyLogs]", bad)
	}
}
