package syncer

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// baselineTaintedIdents names every identifier that can only appear if code
// is reading or writing a Baseline. Seeds the data-flow taint tracking below
// — a local variable or range key/value derived from one of these is tainted
// too, however it is spelled, which is what closes the loop-variable-renaming
// bypass (a range over Baseline.Verified taints its key/value idents even
// when they are not literally named "baseline").
var baselineTaintedIdents = map[string]bool{
	"Baseline": true, "BaselineEntry": true, "Verified": true, "baseline": true,
}

// isTainted reports whether n contains an identifier that is baseline-tainted
// — either one of baselineTaintedIdents directly, or a local name that
// taintedLocals proved is derived from one.
func isTainted(n ast.Node, extra map[string]bool) bool {
	found := false
	ast.Inspect(n, func(x ast.Node) bool {
		if id, ok := x.(*ast.Ident); ok && (baselineTaintedIdents[id.Name] || extra[id.Name]) {
			found = true
		}
		return true
	})
	return found
}

// baseIdent returns the identifier a plain or single-index assignment target
// resolves to: x for both "x = ..." and "x[k] = ...". Used so that writing a
// tainted value into one element of a local map (converted[p] = tainted)
// taints the map variable itself, the way `st.Files[p] = ...` and
// `st.Files = ...` are already treated the same by selectsFiles.
func baseIdent(e ast.Expr) *ast.Ident {
	switch x := e.(type) {
	case *ast.Ident:
		return x
	case *ast.IndexExpr:
		return baseIdent(x.X)
	}
	return nil
}

// taintedLocals runs a fixed-point pass over body, propagating taint through
// plain assignment, single-element map/slice assignment, and range: `e :=
// b.Verified[p]` taints e; `for _, e := range b.Verified` taints e too,
// regardless of what e is called; `converted[p] = FileState{Etag: e.Etag}`
// taints converted. Without this a taint check keyed on identifier spelling
// is defeated by renaming the loop variable away from "baseline", or by
// building an intermediate map one element at a time before handing it to
// something like maps.Copy(st.Files, converted).
func taintedLocals(body ast.Node) map[string]bool {
	tainted := map[string]bool{}
	for {
		changed := false
		ast.Inspect(body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.AssignStmt:
				rhsTainted := false
				for _, r := range x.Rhs {
					// planPull/planPush are the one sanctioned place a
					// baseline argument is consumed (C7): their returned
					// decision must not itself count as tainted merely
					// because a tainted argument was passed in, or every
					// caller assigning `d := planPull(..., baseline, ...)`
					// would taint d, then d.Fetch, then the range variable
					// ranging over it, all the way into withFile and
					// st.Files — a false positive, not a real leak. That
					// leak is what the per-function .Files-write scan below
					// (run over planPull's and planPush's own bodies too)
					// and the behavioural half independently prove absent.
					if isPlanDecisionCall(r) {
						continue
					}
					if isTainted(r, tainted) {
						rhsTainted = true
					}
				}
				if rhsTainted {
					for _, l := range x.Lhs {
						if id := baseIdent(l); id != nil && id.Name != "_" && !tainted[id.Name] {
							tainted[id.Name] = true
							changed = true
						}
					}
				}
			case *ast.RangeStmt:
				if isTainted(x.X, tainted) {
					if id, ok := x.Key.(*ast.Ident); ok && id.Name != "_" && !tainted[id.Name] {
						tainted[id.Name] = true
						changed = true
					}
					if id, ok := x.Value.(*ast.Ident); ok && id.Name != "_" && !tainted[id.Name] {
						tainted[id.Name] = true
						changed = true
					}
				}
			}
			return true
		})
		if !changed {
			return tainted
		}
	}
}

// isPlanDecisionCall reports whether e is a call to planPull or planPush.
func isPlanDecisionCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name == "planPull" || fn.Name == "planPush"
	case *ast.SelectorExpr:
		return fn.Sel.Name == "planPull" || fn.Sel.Name == "planPush"
	}
	return false
}

// selectsFiles reports whether e is (or indexes) a selector ending in
// ".Files" — the shape of both a whole-map assignment (st.Files = ...) and a
// single-key one (st.Files[p] = ...).
func selectsFiles(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.SelectorExpr:
		return x.Sel.Name == "Files"
	case *ast.IndexExpr:
		return selectsFiles(x.X)
	}
	return false
}

// callIsWithFile reports whether call invokes withFile, by selector
// (st.withFile(...)) or by plain name. withFile is the package's only real
// route into State.Files — production never writes st.Files[...] directly —
// so a direct-assignment check alone is blind to every actual ledger write.
func callIsWithFile(call *ast.CallExpr) bool {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fn.Sel.Name == "withFile"
	case *ast.Ident:
		return fn.Name == "withFile"
	}
	return false
}

// callWritesFilesFromTainted reports whether call receives both a ".Files"
// operand and a baseline-tainted one — the shape of e.g. maps.Copy(st.Files,
// converted), which is a write into .Files that is neither an AssignStmt nor
// a withFile call.
func callWritesFilesFromTainted(call *ast.CallExpr, tainted map[string]bool) bool {
	hasFiles := false
	hasTainted := false
	for _, a := range call.Args {
		if selectsFiles(a) {
			hasFiles = true
		}
		if isTainted(a, tainted) {
			hasTainted = true
		}
	}
	return hasFiles && hasTainted
}

// TestBaselineNeverBecomesTracked has two halves. The structural half proves,
// by walking every non-test file's AST, that nothing in this package ever
// writes a baseline-tainted value into a State.Files — whether by direct
// assignment, through withFile, or through a call taking both a .Files
// operand and a tainted one — and that neither planPull's nor planPush's
// prune loop reads a baseline identifier. The behavioural half is the
// positive control the structural half cannot be: it replays the measured
// fact (§ DESIGN-dsxdir.md 4) both ways in one function, so a future patch
// that merges Baseline.Verified into State.Files before planPush turns this
// red with the exact output that fact recorded.
func TestBaselineNeverBecomesTracked(t *testing.T) {
	t.Run("structural", func(t *testing.T) {
		entries, err := os.ReadDir(".")
		if err != nil {
			t.Fatal(err)
		}

		planPullFound := false
		planPushFound := false
		withFileCallSites := map[string]int{}

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
				if !ok || fn.Body == nil {
					continue
				}
				switch fn.Name.Name {
				case "planPull":
					planPullFound = true
				case "planPush":
					planPushFound = true
				}

				tainted := taintedLocals(fn.Body)

				ast.Inspect(fn.Body, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.AssignStmt:
						for _, lhs := range x.Lhs {
							if !selectsFiles(lhs) {
								continue
							}
							for _, rhs := range x.Rhs {
								if isTainted(rhs, tainted) {
									t.Errorf("%s: %s assigns a baseline-tainted value into .Files", name, fn.Name.Name)
								}
							}
						}
					case *ast.CallExpr:
						if callIsWithFile(x) {
							withFileCallSites[name]++
							for _, arg := range x.Args {
								if isTainted(arg, tainted) {
									t.Errorf("%s: %s calls withFile with a baseline-tainted argument", name, fn.Name.Name)
								}
							}
						}
						if callWritesFilesFromTainted(x, tainted) {
							t.Errorf("%s: %s passes .Files and a baseline-tainted value to the same call", name, fn.Name.Name)
						}
					}
					return true
				})

				if fn.Name.Name == "planPull" || fn.Name.Name == "planPush" {
					ast.Inspect(fn.Body, func(n ast.Node) bool {
						ifs, ok := n.(*ast.IfStmt)
						if !ok {
							return true
						}
						id, ok := ifs.Cond.(*ast.Ident)
						if !ok || id.Name != "prune" {
							return true
						}
						if isTainted(ifs.Body, tainted) {
							t.Errorf("%s's prune loop reads a baseline identifier", fn.Name.Name)
						}
						return true
					})
				}
			}
		}

		if !planPullFound {
			t.Fatal("planPull was not found in any non-test file: this guard cannot tell " +
				"'nothing forbidden' from 'I looked in the wrong place', so it refuses to pass")
		}
		if !planPushFound {
			t.Fatal("planPush was not found in any non-test file: this guard cannot tell " +
				"'nothing forbidden' from 'I looked in the wrong place', so it refuses to pass")
		}

		// A fourth withFile call site (or a missing known one) is a build-time
		// signal that a new ledger writer appeared and must be checked by hand
		// for baseline taint — this test only checks the ones it knows about.
		wantWithFileCallSites := map[string]int{"pull.go": 2, "push.go": 1}
		if !maps.Equal(withFileCallSites, wantWithFileCallSites) {
			t.Fatalf("withFile is called from %v, want exactly %v", withFileCallSites, wantWithFileCallSites)
		}
	})

	t.Run("behavioural", func(t *testing.T) {
		// F3's fixture: two remote-only paths ("other1.css", "other2.png") plus
		// one locally-present unchanged path ("keep.css"). --prune with these
		// two tracked in State.Files reproduces the measured
		// Delete=[other1.css other2.png] PruneConflicts=[] Write=[] Unchanged=1.
		remote := remoteOf(
			RemoteEntry{Path: "other1.css", Etag: "e1"},
			RemoteEntry{Path: "other2.png", Etag: "e2"},
			RemoteEntry{Path: "keep.css", Etag: "ek"},
		)
		local := localOf(localFile{Path: "keep.css", SHA: "sk"})

		tracked := planPush(remote, local, stateOf(map[string]FileState{
			"other1.css": {Etag: "e1", SHA: "s1"},
			"other2.png": {Etag: "e2", SHA: "s2"},
			"keep.css":   {Etag: "ek", SHA: "sk"},
		}), nil, false, true)

		wantDelete := []string{"other1.css", "other2.png"}
		if !slices.Equal(tracked.Delete, wantDelete) {
			t.Fatalf("tracked in State.Files: Delete=%v, want %v — this is the measured fact "+
				"this whole feature exists around; if it stopped reproducing, the fixture drifted",
				tracked.Delete, wantDelete)
		}
		if len(tracked.PruneConflicts) != 0 || len(tracked.Write) != 0 || tracked.Unchanged != 1 {
			t.Fatalf("tracked in State.Files: got PruneConflicts=%v Write=%v Unchanged=%d, want none/none/1",
				tracked.PruneConflicts, tracked.Write, tracked.Unchanged)
		}

		// The identical two paths, this time recorded only in a Baseline, never
		// in State.Files, and now wired into planPush's baseline parameter
		// (C7). Neither path is present in local, so planPush's per-path loop
		// (which iterates SortedPaths(local)) never even reaches them — only
		// the prune loop, which is keyed off st.Files alone, ever visits a
		// remote-only path. Passing bl.Verified in makes that structural claim
		// observable rather than merely inert.
		bl := Baseline{
			ProjectID: "p",
			Verified: map[string]BaselineEntry{
				"other1.css": {Etag: "e1", SHA: "s1"},
				"other2.png": {Etag: "e2", SHA: "s2"},
			},
		}
		if len(bl.Verified) != 2 {
			t.Fatalf("test bug: baseline fixture holds %d entries, want 2", len(bl.Verified))
		}

		untracked := planPush(remote, local, stateOf(map[string]FileState{
			"keep.css": {Etag: "ek", SHA: "sk"},
		}), bl.Verified, false, true)

		if untracked.Delete != nil {
			t.Errorf("baselined-only paths: Delete=%v, want nil — a baseline entry must never "+
				"reach the prune loop", untracked.Delete)
		}
		if untracked.Unchanged != 1 {
			t.Errorf("baselined-only paths: Unchanged=%d, want 1", untracked.Unchanged)
		}
	})
}

// TestBaselineEntryIsADistinctType guards the design's compile-time claim —
// "st.Files = b.Verified does not compile" — which only holds while
// BaselineEntry is a genuinely distinct type. reflect.TypeOf resolves a type
// alias (type BaselineEntry = FileState) to the identical reflect.Type as
// FileState, so this catches an alias collapse even though the structural
// AST check above has nothing to see: BaselineEntry has zero consumers in
// C6, so no assignment exists yet for the walk to inspect either way.
func TestBaselineEntryIsADistinctType(t *testing.T) {
	if reflect.TypeOf(BaselineEntry{}) == reflect.TypeOf(FileState{}) {
		t.Fatal("BaselineEntry has collapsed into FileState (alias or otherwise); " +
			"the compile-time guard `st.Files = b.Verified` does not compile is gone")
	}
}

func TestBaselineSurvivesSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	want := Baseline{
		ProjectID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		Endpoint:  "https://claude.ai/api/organizations/o/mcp",
		Verified: map[string]BaselineEntry{
			"tokens/color.css": {Etag: `W/"7a1"`, Size: 412, SHA: strings.Repeat("9f", 32)},
			"README.md":        {Etag: `W/"1"`, Size: 42, SHA: strings.Repeat("ab", 32)},
		},
	}

	if err := want.save(dir); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := loadBaseline(dir)
	if err != nil {
		t.Fatalf("loadBaseline: %v", err)
	}

	if got.ProjectID != want.ProjectID {
		t.Errorf("ProjectID = %q, want %q", got.ProjectID, want.ProjectID)
	}
	if got.Endpoint != want.Endpoint {
		t.Errorf("Endpoint = %q, want %q", got.Endpoint, want.Endpoint)
	}
	if len(got.Verified) != len(want.Verified) {
		t.Fatalf("Verified has %d entries, want %d: %#v", len(got.Verified), len(want.Verified), got.Verified)
	}
	for path, wantEntry := range want.Verified {
		gotEntry, ok := got.Verified[path]
		if !ok {
			t.Errorf("Verified[%q] missing after round trip", path)
			continue
		}
		if gotEntry != wantEntry {
			t.Errorf("Verified[%q] = %#v, want %#v", path, gotEntry, wantEntry)
		}
	}
}

// TestLoadBaselineFixesUpANullVerifiedMap proves the nil-map fixup in
// loadBaseline is load-bearing: a baseline.json that decodes successfully
// but carries a null "verified" key must not hand back a nil map — a future
// consumer writing through the returned map would panic.
func TestLoadBaselineFixesUpANullVerifiedMap(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(StateDir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(BaselinePath(dir), []byte(`{"project_id":"p","verified":null}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := loadBaseline(dir)
	if err != nil {
		t.Fatalf("loadBaseline: %v", err)
	}
	if got.Verified == nil {
		t.Fatal("Verified is nil after decoding a null \"verified\" key")
	}
}

// TestBaselineSaveWritesIndentedJSONWithATrailingNewline pins baseline.json's
// byte shape the way ledger_golden_test.go pins state.json's: two-space
// indent via json.MarshalIndent, plus a trailing newline. Without this, a
// dropped `data = append(data, '\n')` is invisible to every other test —
// TestBaselineSurvivesSaveAndLoad only compares decoded field values.
func TestBaselineSaveWritesIndentedJSONWithATrailingNewline(t *testing.T) {
	dir := t.TempDir()
	b := Baseline{ProjectID: "p", Verified: map[string]BaselineEntry{
		"a.css": {Etag: "e1", Size: 3, SHA: "s1"},
	}}
	if err := b.save(dir); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := os.ReadFile(BaselinePath(dir))
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	want, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')

	if string(got) != string(want) {
		t.Errorf("baseline.json = %q, want %q", got, want)
	}
}

func TestBaselineLoadMissingFileIsEmpty(t *testing.T) {
	dir := t.TempDir()
	got, err := loadBaseline(dir)
	if err != nil {
		t.Fatalf("loadBaseline on a directory with no baseline: %v", err)
	}
	if got.Verified == nil || len(got.Verified) != 0 {
		t.Errorf("Verified = %#v, want a non-nil empty map", got.Verified)
	}
}

func TestBaselineBoundToProjectAndEndpoint(t *testing.T) {
	b := Baseline{ProjectID: "p1", Endpoint: "https://claude.ai/api/organizations/o/mcp"}

	if !b.bound("p1", "https://claude.ai/api/organizations/o/mcp") {
		t.Error("bound() = false for a matching project and endpoint")
	}
	if !b.bound("p1", "https://claude.ai/api/organizations/o/mcp/v2") {
		t.Error("bound() = false across a path-only endpoint move — invariant 13 compares scheme+host only")
	}
	if b.bound("p2", "https://claude.ai/api/organizations/o/mcp") {
		t.Error("bound() = true for a different project")
	}
	if b.bound("p1", "https://evil.example/mcp") {
		t.Error("bound() = true for a different host")
	}
}

func TestBaselinePathIsInsideDsxDir(t *testing.T) {
	dir := filepath.FromSlash("/tmp/x")
	want := filepath.Join(dir, ".dsx", "baseline.json")
	if got := BaselinePath(dir); got != want {
		t.Errorf("BaselinePath(%q) = %q, want %q", dir, got, want)
	}
}
