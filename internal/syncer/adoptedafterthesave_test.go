package syncer

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// Every other report field naming an act names one already durable by the time
// it is appended: Fetched follows a returned rename, Deleted a returned
// os.Remove. Adopted's act is the LEDGER ENTRY and nothing else — an adopted
// path had its bytes already and dsx wrote nothing for it — so claiming it
// before st.save returns nil prints "adopted N" over a ledger that never
// landed (invariant 12).
//
// This is a structural guard because the behavioural one is unreachable:
// checkLedgerHome probes the ledger directory with the same os.CreateTemp save
// will use, and refuses the run up front, so a save that fails after that
// probe passed needs the permissions or the disk to change mid-run. The guard
// is therefore narrow and says so — it pins WHERE the assignment sits, not
// what happens when the save fails.
func TestAdoptedIsClaimedOnlyOnceTheLedgerSaveReturnedNil(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "pull.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	var (
		assignments int
		guarded     int
	)

	// Walk if-statements first so the condition is in hand when the assignment
	// underneath is found.
	ast.Inspect(file, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		cond := exprText(fset, ifs.Cond)
		if !strings.Contains(cond, "saveErr") {
			return true
		}
		ast.Inspect(ifs.Body, func(m ast.Node) bool {
			if assignsRepAdopted(m) {
				guarded++
			}
			return true
		})
		return true
	})

	ast.Inspect(file, func(n ast.Node) bool {
		if assignsRepAdopted(n) {
			assignments++
		}
		return true
	})

	if assignments == 0 {
		t.Fatal("no assignment to rep.Adopted was found in pull.go: this guard cannot tell " +
			"'nothing forbidden' from 'I looked in the wrong place', so it refuses to pass")
	}
	if assignments != guarded {
		t.Fatalf("rep.Adopted is assigned %d time(s) but only %d of those sit under a saveErr "+
			"condition — Adopted names a ledger entry, and one claimed before st.save returns "+
			"nil is printed over a ledger that never landed", assignments, guarded)
	}
}

func assignsRepAdopted(n ast.Node) bool {
	as, ok := n.(*ast.AssignStmt)
	if !ok {
		return false
	}
	for _, lhs := range as.Lhs {
		sel, ok := lhs.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Adopted" {
			continue
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == "rep" {
			return true
		}
	}
	return false
}

func exprText(fset *token.FileSet, e ast.Expr) string {
	var sb strings.Builder
	ast.Inspect(e, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			sb.WriteString(id.Name)
			sb.WriteByte(' ')
		}
		return true
	})
	return sb.String()
}
