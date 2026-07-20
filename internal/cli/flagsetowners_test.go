package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"testing"
)

// parseFuncDecl compiles one function out of a source fragment.
func parseFuncDecl(t *testing.T, src string) *ast.FuncDecl {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "x.go", "package p\n"+src, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok {
			return fn
		}
	}
	t.Fatal("no function in the fragment")
	return nil
}

// One identifier can be assigned a FlagSet more than once — a switch picking a
// literal name per mode is the obvious shape. Assignment let the last branch in
// AST order erase the others, so every earlier command lost its declarations
// and usageFooter's promises about them read as lies.
func TestFlagSetOwnersUnionsRepeatedAssignments(t *testing.T) {
	fn := parseFuncDecl(t, `
func f(mode string) {
	var fs *flag.FlagSet
	switch mode {
	case "pull":
		fs = cmd.NewFlagSet("pull")
	case "push":
		fs = cmd.NewFlagSet("push")
	default:
		fs = cmd.NewFlagSet("status")
	}
	_ = fs
}`)

	got := flagSetOwners(fn, map[string]bool{"cmd": true}, []string{"pull", "push", "status"})
	owners := got["fs"]
	slices.Sort(owners)
	want := []string{"pull", "push", "status"}
	if !slices.Equal(owners, want) {
		t.Errorf("owners=%v, want %v — a later assignment erased an earlier one, "+
			"which leaves the erased commands with no declarations to check", owners, want)
	}
}

// A single literal still names exactly one command; the union must not widen
// a precise attribution.
func TestFlagSetOwnersKeepsASingleLiteralPrecise(t *testing.T) {
	fn := parseFuncDecl(t, `
func f() {
	fs := cmd.NewFlagSet("clone")
	_ = fs
}`)

	got := flagSetOwners(fn, map[string]bool{"cmd": true}, []string{"pull", "push", "clone"})
	if !slices.Equal(got["fs"], []string{"clone"}) {
		t.Errorf("owners=%v, want [clone] — a literal names one command", got["fs"])
	}
}

// The expression fallback still covers the whole package: a FlagSet whose name
// dsx cannot read statically could belong to any of them.
func TestFlagSetOwnersFallsBackToThePackageForAnExpression(t *testing.T) {
	fn := parseFuncDecl(t, `
func f(mode string) {
	fs := cmd.NewFlagSet(mode)
	_ = fs
}`)

	got := flagSetOwners(fn, map[string]bool{"cmd": true}, []string{"pull", "push"})
	if !slices.Equal(got["fs"], []string{"pull", "push"}) {
		t.Errorf("owners=%v, want the whole package", got["fs"])
	}
}

// Two identifiers stay independent; the union is per variable.
func TestFlagSetOwnersKeepsDistinctVariablesApart(t *testing.T) {
	fn := parseFuncDecl(t, `
func f() {
	a := cmd.NewFlagSet("pull")
	b := cmd.NewFlagSet("push")
	_, _ = a, b
}`)

	got := flagSetOwners(fn, map[string]bool{"cmd": true}, nil)
	if !slices.Equal(got["a"], []string{"pull"}) || !slices.Equal(got["b"], []string{"push"}) {
		t.Errorf("a=%v b=%v, want [pull] and [push]", got["a"], got["b"])
	}
}
