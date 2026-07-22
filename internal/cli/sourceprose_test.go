package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// sourceDsxWord finds a `dsx <word>` (optionally `dsx <word> <word>`). A
// placeholder such as `dsx <cmd>` is not matched: the first character must be a
// letter. In a string literal, an invocation stands in command position: at the start,
// after a backtick or "$(", or after a colon that introduces it. "dsx" as the
// subject of a sentence — "dsx cannot judge an expiry", "# dsx bash completion"
// — never is. RE2 has no lookbehind, so the preceding character is captured and
// discarded.
var sourceDsxWord = regexp.MustCompile("(^|[`(:\n]) ?dsx ([a-z][a-z0-9-]*)(?: ([a-z][a-z0-9-]*))?")

// In a comment, "dsx" is as often the subject of a sentence as the start of an
// invocation — "every path dsx resolves", "as if dsx had been started". Only a
// backticked span is read there. A string literal needs no such narrowing:
// prose about dsx does not get printed to the user.
var sourceDsxSpan = regexp.MustCompile("`(dsx [^`]*)`")

// TestEveryDsxInvocationInSourceNamesARealCommand is invariant 18 read as data:
// a refusal must name a form that parses. The invariant was stated about the
// sync refusals and enforced there by hand, so nothing saw the same claim made
// from a flag's help text, a GrantError, or conflictHint — and the noun
// migration falsified all four at once. clone's refusal named `dsx projects`,
// which had become `dsx project ls`; conflictHint prescribed `dsx cat`, which
// no longer existed; put and cp both advertised `dsx plan`; the grant error
// spelled a whole invocation of it.
//
// Comments are read as well as strings. A comment naming a dead command is not
// a lie to the user, but it is the same drift, and the cost of covering it is
// one extra branch.
func TestEveryDsxInvocationInSourceNamesARealCommand(t *testing.T) {
	t.Parallel()

	// Prose is free to name a noun on its own — `dsx conv` lists the verbs —
	// so a bare noun is accepted. Anything else must be an address.
	known := func(first, second string) bool {
		if _, ok := commandIndex[first]; ok {
			return true
		}
		g, isNoun := nounIndex[first]
		if !isNoun {
			return false
		}
		if second == "" {
			return true
		}
		_, ok := commandIndex[g.Noun+" "+second]
		return ok
	}

	checked := 0
	check := func(where, text string) {
		for _, m := range sourceDsxWord.FindAllStringSubmatch(text, -1) {
			checked++
			if !known(m[2], m[3]) {
				t.Errorf("%s names `%s`, which dsx cannot run", where, strings.TrimSpace(m[0]))
			}
		}
	}

	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "design" || name == "reference" {
				return filepath.SkipDir
			}
			return nil
		}
		// Test files are excluded: they carry fixtures (`dsx fixture one`), quoted
		// history in the explanations above a guard, and expected outputs. None of
		// it is printed by the binary, which is what this test is about.
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		// usageFooter is dsx's own catalogue: TestUsageIsGeneratedByteForByte and
		// TestEveryDeclaredFlagIsDocumented already read every form in it, and it
		// legitimately spells one this reader cannot resolve — `dsx -C <dir> <command>`.
		if filepath.Base(path) == "usage.go" {
			return nil
		}
		ast.Inspect(file, func(n ast.Node) bool {
			bl, ok := n.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(bl.Value)
			if err != nil {
				return true
			}
			check(path+":"+strconv.Itoa(fset.Position(bl.Pos()).Line), s)
			return true
		})
		for _, group := range file.Comments {
			for _, c := range group.List {
				where := path + ":" + strconv.Itoa(fset.Position(c.Pos()).Line)
				for _, m := range sourceDsxSpan.FindAllStringSubmatch(c.Text, -1) {
					check(where, m[1])
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Without this the whole test passes by walking nothing — the failure mode
	// of every reader that finds its own input.
	if checked < 20 {
		t.Fatalf("only %d invocations found in the tree; the reader is broken, not the source", checked)
	}
}
