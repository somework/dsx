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
)

func cmdImportNames(file *ast.File) map[string]bool {
	const cmdPath = "github.com/somework/dsx/internal/cmd"
	names := map[string]bool{}
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != cmdPath {
			continue
		}
		if imp.Name != nil {
			names[imp.Name.Name] = true
		} else {
			names["cmd"] = true
		}
	}
	return names
}

// wantUsage is hand-written: a golden regenerated from renderUsage would only
// prove the generator equals itself. This is dsx's most-read output.
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

func TestUsageIsGeneratedByteForByte(t *testing.T) {
	t.Parallel()
	if usage == wantUsage {
		return
	}

	got, want := strings.Split(usage, "\n"), strings.Split(wantUsage, "\n")
	for i := 0; i < len(got) && i < len(want); i++ {
		if got[i] != want[i] {
			t.Fatalf("usage line %d differs\n got %q\nwant %q", i+1, got[i], want[i])
		}
	}
	t.Fatalf("usage has %d lines, want %d", len(got), len(want))
}

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

func TestEveryDeclaredGroupIsRegistered(t *testing.T) {
	t.Parallel()

	declared := map[string]string{}
	registered := map[string]bool{}

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

			cmdNames := cmdImportNames(file)
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

					if sel, ok := lit.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "Group" {
						if pkg, ok := sel.X.(*ast.Ident); ok && cmdNames[pkg.Name] {
							// Resolve the alias: a package's real name (from its own
							// package clause) can differ from its directory name —
							// cmd/sync declares `package synccmd` — so key off that,
							// not off the directory qualifier.
							key := vs.Names[0].Name
							if qualifier != "" {
								key = file.Name.Name + "." + key
							}
							declared[key] = filepath.Join(dir, name)
						}
					}
					if qualifier == "" && vs.Names[0].Name == "groups" {
						for _, elt := range lit.Elts {
							switch v := elt.(type) {
							case *ast.Ident:
								registered[v.Name] = true
							case *ast.SelectorExpr:
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
