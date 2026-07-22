package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/cmd"
)

// flagDeclarations finds every fs.String/Bool/Int call naming flagName across
// every non-test .go file the binary is built from — internal/cli and each
// internal/cmd/<group> — and returns each call's position mapped to its
// default-value expression.
//
// The reach is TestEveryDeclaredFlagIsDocumented's, and deliberately so: a rule
// about how a flag is declared is worth nothing if it holds in one package
// only. The selector names matched are that guard's too, so a declaration
// invisible here is invisible there.
func flagDeclarations(t *testing.T, flagName string) map[string]ast.Expr {
	t.Helper()

	fset := token.NewFileSet()
	out := map[string]ast.Expr{}
	files := 0

	scan := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parsing %s: %v", path, err)
			}
			files++
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) < 2 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "String", "Bool", "Int":
				default:
					return true
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				if strings.Trim(lit.Value, `"`) != flagName {
					return true
				}
				out[fset.Position(call.Pos()).String()] = call.Args[1]
				return true
			})
		}
	}

	scan(".")
	pkgs, err := os.ReadDir(filepath.Join("..", "cmd"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pkgs {
		if p.IsDir() {
			scan(filepath.Join("..", "cmd", p.Name()))
		}
	}
	if files == 0 {
		t.Fatal("no source files found; the walk is broken, not the code")
	}
	return out
}

// TestJSONIsDeclaredOnlyThroughTheHelper holds the one spelling of --json.
//
// cmd.JSONFlag exists so the flag reads the same everywhere, and a hand-rolled
// fs.Bool("json", ...) beside it is not a stylistic variant: the third argument
// is what flag.PrintDefaults shows, so `dsx pull -h` said "JSON output" while
// `dsx ls -h` said "machine-readable output" for the identical flag. One option
// explaining itself two ways is the drift this forbids.
//
// The rule is "call the helper", not "agree on the string", because the reader
// of a call site cannot see the other five: only a single declaration makes
// disagreement unconstructible.
func TestJSONIsDeclaredOnlyThroughTheHelper(t *testing.T) {
	t.Parallel()

	// This asserts an absence, so it needs a positive control or a broken walk
	// reads as a clean codebase forever. --prune is declared by hand and always
	// will be: it belongs to two commands, not thirty, so no helper wraps it.
	if len(flagDeclarations(t, "prune")) == 0 {
		t.Fatal("the walk found no --prune declaration, so it would find no hand-rolled --json " +
			"either; the finder is broken, not the code")
	}

	handRolled := flagDeclarations(t, "json")
	for pos := range handRolled {
		t.Errorf("%s declares --json by hand; call cmd.JSONFlag(fs) instead, or `dsx <cmd> -h` "+
			"describes one flag two ways depending on which command was asked", pos)
	}
}

// TestConcurrencyDefaultIsNamedOnce holds the other half. Six commands take -j
// and each repeated the literal 8, so the default was six facts that happened
// to agree; nothing made them agree, and a reader editing one saw no sign of
// the other five.
//
// A named constant is the whole fix — deliberately NOT a cmd.ConcurrencyFlag
// helper mirroring JSONFlag. TestEveryDeclaredFlagIsDocumented finds a
// declaration by matching a selector literally named String/Bool/Int, so
// wrapping the call would hide all six from it; and unlike --json, whose footer
// scope is the `every command` bypass, -j's scope names its commands, so the
// backward half would then report every one of them as documented-but-undeclared.
// Swapping only the default argument keeps the declaration visible to that
// guard, which reads call.Args[0] and nothing else.
func TestConcurrencyDefaultIsNamedOnce(t *testing.T) {
	t.Parallel()

	decls := flagDeclarations(t, "j")
	if len(decls) == 0 {
		t.Fatal("no -j declaration found anywhere; the walk is broken, not the code")
	}
	for pos, def := range decls {
		if _, isLiteral := def.(*ast.BasicLit); isLiteral {
			t.Errorf("%s spells the -j default as a literal; use cmd.DefaultConcurrency so the "+
				"number is one fact rather than six that happen to agree", pos)
		}
	}
}

// TestTheFooterQuotesTheRealConcurrencyDefault ties the one number a reader
// sees in `dsx help` to the one the binary uses. usageFooter spells it as
// prose, which is a copy no compiler checks — and the failure is silent in the
// direction that matters: the footer keeps promising the old default after the
// constant moves.
func TestTheFooterQuotesTheRealConcurrencyDefault(t *testing.T) {
	t.Parallel()

	want := "(default " + strconv.Itoa(cmd.DefaultConcurrency) + ")"
	if !strings.Contains(usageFooter, want) {
		t.Errorf("usageFooter does not say %q, so `dsx help` and the binary disagree "+
			"about how many workers -j starts", want)
	}
}
