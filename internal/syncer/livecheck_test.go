package syncer

import (
	"go/ast"
	"go/build/constraint"
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

// liveSources is every file the TAG hides, found by reading the tag. Reading
// one file by name was right while there was one; globbing "live*_test.go"
// then made the floor depend on a naming habit, and `push_live_test.go` is a
// perfectly natural name that would sit outside it. The build constraint is
// the actual thing that hides a file from CI, so it is the thing to match.
func liveSources(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if hasLiveConstraint(src) {
			out = append(out, path)
		}
	}
	if len(out) == 0 {
		t.Fatal("no live-tagged sources found; the walk matched nothing and this guard proved nothing")
	}
	sort.Strings(out)
	return out
}

// hasLiveConstraint reads the //go:build line the way the toolchain does,
// rather than matching the word: `//go:build live && !windows` and
// `//go:build !live` are different answers and a substring search gives the
// same one.
func hasLiveConstraint(src []byte) bool {
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "//") {
			// The constraint must precede the package clause; past it there is
			// nothing left to find.
			if strings.HasPrefix(line, "package ") {
				return false
			}
			continue
		}
		expr, err := constraint.Parse(line)
		if err != nil {
			continue
		}
		if expr.Eval(func(tag string) bool { return tag == "live" }) {
			return true
		}
	}
	return false
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

// The walk has to reach both real live files, and — the half a glob could
// never have — it must reach one whose name does not start with "live", and
// must not sweep in an ordinary test that merely mentions the word.
func TestTheLiveFloorCoversEveryLiveSource(t *testing.T) {
	got := liveSources(t)
	for _, want := range []string{"live_test.go", "livereply_test.go"} {
		if !slices.Contains(got, want) {
			t.Errorf("%s is outside the floor; found %v", want, got)
		}
	}
	if slices.Contains(got, "livecheck_test.go") {
		t.Error("livecheck_test.go carries no live constraint and must not be in the floor")
	}
}

func TestTheLiveConstraintIsReadNotMatched(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want bool
	}{
		{"plain", "//go:build live\n\npackage syncer\n", true},
		{"and", "//go:build live && !windows\n\npackage syncer\n", true},
		{"negated", "//go:build !live\n\npackage syncer\n", false},
		{"other tag", "//go:build integration\n\npackage syncer\n", false},
		{"none", "package syncer\n", false},
		{"the word in a comment after the package clause", "package syncer\n\n// live\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasLiveConstraint([]byte(tc.src)); got != tc.want {
				t.Errorf("hasLiveConstraint = %v, want %v for:\n%s", got, tc.want, tc.src)
			}
		})
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
