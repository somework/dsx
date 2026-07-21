package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
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
  dsx clone <project> <dir> [-j N]      first pull into a new directory
  dsx pull  [<dir>] [--prune] [--force] [-n] [-j N]
  dsx push  [<dir>] [--prune] [--force] [-n] [-j N]
  dsx status [<dir>]                    what a sync would do; transfers nothing
  dsx fetch [<dir>] [-j N]              record what the server holds; writes .dsx/, not the tree
  dsx pin <project> [<dir>]             bind an existing directory to a project; no round trip
  dsx unpin [<dir>]                     release a binding that has synced nothing
  dsx diff [<dir>] [--out <dir>] [-j N] classify each path: same, local-only, remote-only, differs
  Only clone and pin name a project; every other verb reads it from <dir>'s
  ledger, and <dir> defaults to ".". unpin releases a binding, clone starts one.
  .dsxignore excludes paths from the sync, in both directions.
  status accepts pull/push's flags and previews them: --force hides conflicts.
  clone is the first pull: both arguments, and <dir> must be empty.

PROJECTS
  dsx projects                          list projects
  dsx project <id>                      project detail
  dsx new <name> [--ds <id>]            create project
  dsx systems                           list design systems

FILES
  dsx ls <project> [path]               list one directory
  dsx tree [<project>]                  every file, recursive, with etags
  dsx cat [<project>] <path> [--out f]  read a file (stdout by default)
  dsx put <project> <path> [file]       write a file (stdin when file is omitted)
  dsx rm <project> <path...>            delete files
  dsx cp <project> <src> <dst> [--from <project>]
  tree and cat take the directory's project when run inside a synced directory.

PLANS / PREVIEW
  dsx plan <project> [--writes a,b] [--deletes c,d] [--scope project]
  dsx preview <project> <path> [--render] [--validators a,b]
  dsx support-js <project> [--path p]

CONVERSATION
  dsx conv <project> [--chat id]
  dsx conv-put <project> --messages <file.json> [--chat id] [--title t] [--append] [--synced-through-idx N]

MEMBERS / SHARING
  dsx members <project>
  dsx member-add <project> --role <r> (--email e | --uuid u)
  dsx member-rm <project> <uuid>
  dsx member-role <project> <uuid> <role>
  dsx sharing <project> [--scope s] [--link-permission p]

ESCAPE HATCH
  dsx prompt [--project id] [--ds id]   the server's own Claude Design prompt
  dsx tools [--schema]                  tool names and schemas from the server
  dsx raw <tool> '<json-args>'          call any tool verbatim

DIAGNOSTICS
  dsx help
  dsx auth                              token scopes and expiry (never the token)
  dsx doctor                            token, endpoint, clock skew
  dsx version                           version, revision, platform
  dsx completion <bash|zsh|fish>

FLAGS
  --json      machine-readable output — every command
  --prune     delete what the other side lacks — pull, push, status
  --force     overwrite conflicts — pull, push, status
  -q  -n      suppress the summary line, dry run — pull, push, status
  -j N        concurrency (default 8) — clone, pull, push, status, tree

WRITE GUARDS
  --if-match E  etag guard ("0" asserts new) — put, cp, support-js
  --plan T      plan_token from dsx plan — put, cp, support-js

GLOBAL
  dsx -C <dir> <command>  run as if dsx had been started in <dir>, like git's

EXIT CODES
  0 ok   1 failed   2 usage   3 conflict (needs a human)
  4 transport (retry may help)   5 auth (run any ` + "`claude`" + ` command)

Env: DSX_TOKEN overrides the stored credential. DSX_ENDPOINT overrides the MCP URL.
     DSX_PROGRESS=never|always overrides the pull/push transfer counter, which
     otherwise draws on stderr only when stderr is a terminal.`

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

// flagTokens pulls the flag names out of one line of help text: any
// whitespace-delimited token that begins with an ASCII '-', stripped of the
// brackets, parens and pipes a Form uses to mark it optional or alternative.
// The em dash that introduces a scope is not an ASCII '-', so it never reads
// as a flag.
func flagTokens(s string) []string {
	var out []string
	for _, f := range strings.Fields(s) {
		f = strings.Trim(f, "[]()|,'\"`")
		if len(f) < 2 || f[0] != '-' {
			continue
		}
		out = append(out, strings.TrimLeft(f, "-"))
	}
	return out
}

const everyCommand = "*"

// footerFlagScopes reads usageFooter as data: every line that names a flag must
// also name the commands that flag reaches, after an em dash.
func footerFlagScopes(t *testing.T) map[string]map[string]bool {
	t.Helper()
	scopes := map[string]map[string]bool{}
	global := false
	for _, line := range strings.Split(usageFooter, "\n") {
		// The GLOBAL block documents dsx's own options — the ones
		// splitGlobalFlags peels off before the command name is even read. They
		// have no per-command declaration to match, and demanding an em-dash
		// scope for them would force a lie in one direction or the other:
		// naming commands the binary would reject, or naming none. The block is
		// not an escape hatch — TestTheGlobalBlockDocumentsExactlyTheRealGlobals
		// ties it to the parser rather than to prose.
		if strings.HasPrefix(line, "GLOBAL") {
			global = true
			continue
		}
		if global {
			if strings.TrimSpace(line) == "" {
				global = false
			}
			continue
		}
		flags := flagTokens(line)
		if len(flags) == 0 {
			continue
		}
		i := strings.LastIndex(line, " — ")
		if i < 0 {
			t.Errorf("usageFooter documents %v without naming the commands it applies to; "+
				"append ` — cmd, cmd` or ` — every command`", flags)
			i = len(line) - len(" — ")
		}
		where := map[string]bool{}
		tail := strings.TrimSpace(line[min(i+len(" — "), len(line)):])
		if tail == "every command" {
			// everyCommand is a claim about the kernel, not a list of names:
			// EmitFlagged and cmd.JSONFlag give --json to commands that never
			// build a FlagSet of their own, so it has no per-command
			// declaration to match and is deliberately not name-checked.
			where[everyCommand] = true
		} else {
			for _, name := range strings.Split(tail, ",") {
				where[strings.TrimSpace(name)] = true
			}
		}
		for _, f := range flags {
			if scopes[f] == nil {
				scopes[f] = map[string]bool{}
			}
			for k := range where {
				scopes[f][k] = true
			}
		}
	}
	return scopes
}

// TestEveryDeclaredFlagIsDocumented closes the drift between the flags a
// command declares and the sentences a user can read, in both directions.
//
// Forward: Form is hand-written and nothing derives it from the FlagSet, so a
// flag added later is discoverable nowhere. Every declaration must appear
// either in its own command's Form or, for flags several commands share, in
// usageFooter under a scope that names the command.
//
// Backward: every command named in a footer scope must actually declare that
// flag. Without this half the footer's command lists were unchecked prose — a
// scope was only ever consulted to permit a declaration, so editing usage.go
// and the wantUsage golden together (exactly what adding a scope looks like)
// stayed green while documenting a flag for a command that has none.
//
// The two halves have different reach, and the difference is the guard's real
// limit. A FlagSet built from a non-literal name — synccmd's
// `cmd.NewFlagSet(mode)` serves pull, push and status from one body — cannot be
// attributed to a single command, so its flags are attributed to every command
// its package's Group declares. That is exact for synccmd and conservative
// elsewhere: it can only over-demand documentation, never let a flag through.
// The one scope not name-checked is `every command`; see footerFlagScopes.
func TestEveryDeclaredFlagIsDocumented(t *testing.T) {
	t.Parallel()

	scopes := footerFlagScopes(t)
	checked := 0
	declared := map[string]map[string]bool{}

	scan := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		var files []*ast.File
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
			if err != nil {
				t.Fatalf("parsing %s: %v", name, err)
			}
			files = append(files, file)
		}

		// A package's Group names every command it serves; that list is what a
		// FlagSet with a non-literal name falls back to.
		var pkgCommands []string
		for _, file := range files {
			pkgCommands = append(pkgCommands, groupCommandNames(file, cmdImportNames(file))...)
		}

		for _, file := range files {
			name := fset.Position(file.Pos()).Filename
			cmdNames := cmdImportNames(file)
			cmdNames["cmd"] = true // internal/cli calls cmd.NewFlagSet; so does every group

			for _, d := range file.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				owner := flagSetOwners(fn, cmdNames, pkgCommands)
				if len(owner) == 0 {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok || len(call.Args) == 0 {
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
					recv, ok := sel.X.(*ast.Ident)
					if !ok {
						return true
					}
					commands, ok := owner[recv.Name]
					if !ok {
						return true
					}
					flagName, ok := stringLit(call.Args[0])
					if !ok {
						return true
					}

					for _, command := range commands {
						checked++
						if declared[command] == nil {
							declared[command] = map[string]bool{}
						}
						declared[command][flagName] = true

						c, ok := commandIndex[command]
						if !ok {
							t.Errorf("%s declares a FlagSet for %q, which is not a registered command", name, command)
							continue
						}
						if slices.Contains(flagTokens(c.Form), flagName) {
							continue
						}
						where := scopes[flagName]
						if where[everyCommand] || where[command] {
							continue
						}
						t.Errorf("%s: %s declares --%s but neither `dsx %s`'s Form nor usageFooter "+
							"documents it for %s: the flag is undiscoverable", name, command, flagName, command, command)
					}
					return true
				})
			}
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

	if checked == 0 {
		t.Fatal("no flag declarations found anywhere; the parser is broken, not the code")
	}

	// The backward half: a footer scope is a promise about a command, so hold
	// it to the command's real declarations.
	for flagName, where := range scopes {
		if where[everyCommand] {
			continue
		}
		for command := range where {
			if _, ok := commandIndex[command]; !ok {
				t.Errorf("usageFooter scopes --%s to %q, which is not a registered command", flagName, command)
				continue
			}
			if declared[command][flagName] {
				continue
			}
			t.Errorf("usageFooter documents --%s for `dsx %s`, but %s declares no such flag: "+
				"the footer promises a flag that would be rejected as unknown", flagName, command, command)
		}
	}
}

// groupCommandNames reads the Name of every command a file's `cmd.Group`
// literals declare.
func groupCommandNames(file *ast.File, cmdNames map[string]bool) []string {
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Group" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || !cmdNames[pkg.Name] {
			return true
		}
		ast.Inspect(lit, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			if key, ok := kv.Key.(*ast.Ident); !ok || key.Name != "Name" {
				return true
			}
			if s, ok := stringLit(kv.Value); ok {
				out = append(out, s)
			}
			return true
		})
		return true
	})
	return out
}

// flagSetOwners maps a local variable holding a *flag.FlagSet to the commands it
// belongs to: the one its string-literal name identifies, or — when the name is
// an expression, as in synccmd's `cmd.NewFlagSet(mode)` — every command the
// package's Group declares.
func flagSetOwners(fn *ast.FuncDecl, cmdNames map[string]bool, pkgCommands []string) map[string][]string {
	owner := map[string][]string{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		lhs, ok := as.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "NewFlagSet" {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || !cmdNames[pkg.Name] {
			return true
		}
		// Appended, not assigned: one identifier can be assigned a FlagSet more
		// than once — a switch picking a literal name per mode is the obvious
		// shape — and assignment lets the last branch in AST order erase the
		// others, leaving every earlier command with no declarations at all.
		if lit, ok := stringLit(call.Args[0]); ok {
			owner[lhs.Name] = append(owner[lhs.Name], lit)
		} else if len(pkgCommands) > 0 {
			owner[lhs.Name] = append(owner[lhs.Name], pkgCommands...)
		}
		return true
	})
	return owner
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}
