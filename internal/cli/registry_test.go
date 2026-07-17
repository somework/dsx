package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wantUsage is `dsx help`, written out rather than generated.
//
// It is the fixture for the same reason ledger_golden_test.go's is: a golden
// built from renderUsage would only prove the code equals itself. This text
// traces to the hand-written const that preceded the registry -- it was diffed
// against it byte for byte, and the one deliberate change is noted below.
//
// This is dsx's most-read output and an agent-facing contract, so a change here
// is a change to the product, not to a detail. If this test goes red, look at
// the diff before touching the fixture: output width is a token budget.
const wantUsage = `dsx — Claude Design sync. Reads Claude Code's own OAuth token; never writes it.

SYNC (etag-aware; unchanged files cost no request at all)
  dsx pull  [<project>] [<dir>] [--prune] [--force] [-n] [-j N]
  dsx push  [<project>] [<dir>] [--prune] [--force] [-n] [-j N]
  dsx status [<project>] [<dir>]        what a sync would do; transfers nothing
  The project id is optional once <dir> holds a ledger; <dir> defaults to "."
  .dsxignore excludes paths from the sync, in both directions.

PROJECTS
  dsx projects                          list projects
  dsx project <id>                      project detail
  dsx new <name> [--ds <id>]            create project
  dsx systems                           list design systems

FILES
  dsx ls <project> [path]               list one directory
  dsx tree <project>                    every file, recursive, with etags
  dsx cat <project> <path> [--out f]    read a file (stdout by default)
  dsx put <project> <path> [file]       write a file (stdin when file is omitted)
  dsx rm <project> <path...>            delete files
  dsx cp <project> <src> <dst> [--from <project>]

PLANS / PREVIEW
  dsx plan <project> [--writes a,b] [--deletes c,d] [--scope project]
  dsx preview <project> <path> [--render] [--validators a,b]
  dsx support-js <project> [--path p]

CONVERSATION
  dsx conv <project> [--chat id]
  dsx conv-put <project> --messages <file.json> [--chat id] [--title t] [--append]

MEMBERS / SHARING
  dsx members <project>
  dsx member-add <project> --role <r> [--email e] [--uuid u]
  dsx member-rm <project> <uuid>
  dsx member-role <project> <uuid> <role>
  dsx sharing <project> [--scope s] [--link-permission p]

ESCAPE HATCH
  dsx prompt [--project id] [--ds id]   the server's own Claude Design prompt
  dsx tools                             tool names and schemas from the server
  dsx raw <tool> '<json-args>'          call any tool verbatim

DIAGNOSTICS
  dsx help
  dsx auth                              token scopes and expiry (never the token)
  dsx doctor                            token, endpoint, clock skew
  dsx version                           version, revision, platform
  dsx completion <bash|zsh|fish>

GLOBAL
  --json      machine-readable output      -q  suppress the summary line
  -j N        concurrency (default 8)      -n  dry run

EXIT CODES
  0 ok   1 failed   2 usage   3 conflict (needs a human)
  4 transport (retry may help)   5 auth (run any ` + "`claude`" + ` command)

Env: DSX_TOKEN overrides the stored credential. DSX_ENDPOINT overrides the MCP URL.`

// The generated text is byte-identical to the const it replaced, save one line:
// `dsx prompt` had its description at column 41 where every other line in the
// file sits at 40. The generator cannot reproduce a one-off hand slip, and
// should not; this records that the difference was seen and wanted.
func TestUsageIsGeneratedByteForByte(t *testing.T) {
	t.Parallel()
	if usage == wantUsage {
		return
	}
	// A whole-string diff of ~70 lines is unreadable. Report the first line that
	// moved, which is the one the author changed.
	got, want := strings.Split(usage, "\n"), strings.Split(wantUsage, "\n")
	for i := 0; i < len(got) && i < len(want); i++ {
		if got[i] != want[i] {
			t.Fatalf("usage line %d differs\n got %q\nwant %q", i+1, got[i], want[i])
		}
	}
	t.Fatalf("usage has %d lines, want %d", len(got), len(want))
}

// A command with neither shape dispatches to a nil func and panics; one with
// both silently ignores Run, because dispatch checks Tool first. Neither is
// reachable through the type system, so it is asserted here.
func TestEveryCommandHasExactlyOneShape(t *testing.T) {
	t.Parallel()
	for _, g := range groups {
		for _, c := range g.Cmds {
			switch {
			case c.Tool == nil && c.Run == nil:
				t.Errorf("%q declares neither Tool nor Run; dispatching it panics", c.Name)
			case c.Tool != nil && c.Run != nil:
				t.Errorf("%q declares both Tool and Run; dispatch takes Tool and Run is dead", c.Name)
			}
		}
	}
}

// Form is what usage prints after "dsx "; Name is what run() dispatches on. A
// Form that starts with a different word documents one command and runs
// another, and `dsx help` is the only place a user would find out.
//
// This is the half of the old anti-drift pair that survives derivation: usage
// and commandNames now come from the same slice and cannot disagree about which
// commands exist, but they can still disagree about what one is called.
func TestEveryCommandFormStartsWithItsName(t *testing.T) {
	t.Parallel()
	for _, g := range groups {
		for _, c := range g.Cmds {
			first, _, _ := strings.Cut(c.Form, " ")
			if first != c.Name {
				t.Errorf("%q has Form %q, which documents `dsx %s`", c.Name, c.Form, first)
			}
		}
	}
}

// An alias is dispatched but never listed, so one that collides with a real
// command silently shadows it in commandIndex -- and the shadowed command keeps
// appearing in usage and in every shell.
func TestNoAliasShadowsACommand(t *testing.T) {
	t.Parallel()
	names := map[string]bool{}
	for _, g := range groups {
		for _, c := range g.Cmds {
			names[c.Name] = true
		}
	}
	for _, g := range groups {
		for _, c := range g.Cmds {
			for _, a := range c.Aliases {
				if names[a] {
					t.Errorf("%q lists alias %q, which is another command's name", c.Name, a)
				}
			}
		}
	}
}

func TestNoGroupIsEmptyOrDuplicated(t *testing.T) {
	t.Parallel()
	titles := map[string]bool{}
	for _, g := range groups {
		if titles[g.Title] {
			t.Errorf("two groups share the title %q; usage would print it twice", g.Title)
		}
		titles[g.Title] = true
		if len(g.Cmds) == 0 {
			t.Errorf("group %q holds no commands; usage would print a bare heading", g.Title)
		}
	}
}

// This is the one drift the registry cannot rule out by construction: a group
// declared in a file but never added to `groups` compiles clean, tests clean,
// and is simply absent -- no `dsx help` section, and every command in it
// rejected as unknown. Deriving the three lists from `groups` cannot help,
// because the omission is upstream of all three.
//
// The declared set is parsed rather than restated. A hand-kept list here would
// be the same list under test: whoever forgot `groups` would forget this too,
// and it would pass. That mistake has already been made once in this repo --
// see survey_test.go's comment -- and it passed for exactly that reason.
func TestEveryDeclaredGroupIsRegistered(t *testing.T) {
	t.Parallel()

	declared := map[string]string{} // how `groups` must name it -> where it lives
	registered := map[string]bool{}

	// A group is declared in one of two places, and both have to be swept: as
	// `var xGroup = cmd.Group{...}` here, or as `var Group = cmd.Group{...}` in
	// its own package under internal/cmd. A sweep of only one of them would go
	// quiet for every group in the other -- which is what happened the first time
	// members moved out, and only the count check below caught it.
	scan := func(dir, qualifier string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
			if err != nil {
				t.Fatalf("parsing %s: %v", name, err)
			}
			for _, d := range file.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok || gd.Tok != token.VAR {
					continue
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
						continue
					}
					lit, ok := vs.Values[0].(*ast.CompositeLit)
					if !ok {
						continue
					}
					// The type is cmd.Group -- a SelectorExpr, not an Ident, now
					// that it comes from a package. Matching Ident silently found
					// nothing, and the guard below is the only reason that was
					// noticed rather than shipped.
					if sel, ok := lit.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "Group" {
						if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "cmd" {
							declared[qualifier+vs.Names[0].Name] = filepath.Join(dir, name)
						}
					}
					if qualifier == "" && vs.Names[0].Name == "groups" {
						for _, elt := range lit.Elts {
							switch v := elt.(type) {
							case *ast.Ident: // a group declared in this package
								registered[v.Name] = true
							case *ast.SelectorExpr: // members.Group and friends
								if pkg, ok := v.X.(*ast.Ident); ok {
									registered[pkg.Name+"."+v.Sel.Name] = true
								}
							}
						}
					}
				}
			}
		}
	}

	scan(".", "")
	pkgs, err := os.ReadDir(filepath.Join("..", "cmd"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pkgs {
		if p.IsDir() {
			scan(filepath.Join("..", "cmd", p.Name()), p.Name()+".")
		}
	}

	// Guard the guard. An extractor that found nothing would pass forever, which
	// is the failure mode this whole test exists to avoid repeating.
	if len(declared) == 0 {
		t.Fatal("no `= cmd.Group{...}` found anywhere; the parser is broken, not the code")
	}
	if len(registered) == 0 {
		t.Fatal("no `var groups = []cmd.Group{...}` found; the parser is broken, not the code")
	}
	if len(declared) != len(groups) {
		t.Errorf("%d groups are declared but `groups` holds %d", len(declared), len(groups))
	}

	for name, where := range declared {
		if !registered[name] {
			t.Errorf("%s declares %s, but it is missing from `groups`: its section is absent from "+
				"`dsx help` and every command in it is rejected as unknown", where, name)
		}
	}
}
