package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// readmeInlineCode pulls the content out of a single-backtick span. Applied
// per line, so it never crosses a fenced block boundary by accident.
var readmeInlineCode = regexp.MustCompile("`([^`]+)`")

// readmeDsxWord finds a `dsx <word>` invocation inside an already-isolated
// code snippet (a fenced line or an inline span) — not anchored, so
// "cd design && dsx pull" still yields "pull". It requires a literal space
// after "dsx", which is what keeps ".dsx/state.json" and ".dsx-state.json"
// out of it: neither has one.
var readmeDsxWord = regexp.MustCompile(`\bdsx ([a-zA-Z][a-zA-Z0-9-]*)`)

// readmeFlagOnlySpan matches a code span that names nothing but a flag,
// optionally with a placeholder value ("--json", "--if-match", "-j N", "-n").
// It is deliberately narrow: a whole shell line such as `go test -race ./...`
// does not match, so the go-toolchain flags in the Development section are
// never mistaken for dsx's own.
var readmeFlagOnlySpan = regexp.MustCompile(`^-{1,2}[a-zA-Z][a-zA-Z0-9-]*( [A-Z][a-zA-Z0-9]*)?$`)

// TestReadmeNamesOnlyRealCommandsAndFlags is the naming half of the doc
// discipline CLAUDE.md's C11 commit asks for. It does not read English: it
// walks every `dsx <word>` and every standalone `--flag`/`-x` a backtick or
// fenced code block puts in front of a reader, and checks each against the
// real registry — commandIndex for commands, and the union of every Form's
// flags plus every usageFooter scope for flags. Reverting a README line to a
// dead command or flag name goes red here; nothing here proves the
// surrounding sentence is true.
func TestReadmeNamesOnlyRealCommandsAndFlags(t *testing.T) {
	t.Parallel()

	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}

	documentedFlags := map[string]bool{}
	for _, c := range commandIndex {
		for _, f := range formFlagTokens(c.Form) {
			documentedFlags[f] = true
		}
	}
	for f := range footerFlagScopes(t) {
		documentedFlags[f] = true
	}

	checkSnippet := func(snippet string) {
		if m := readmeDsxWord.FindStringSubmatch(snippet); m != nil {
			name := m[1]
			if _, ok := commandIndex[name]; !ok {
				t.Errorf("README names `dsx %s`, which is not a registered command (in %q)", name, snippet)
			}
			for _, f := range flagTokens(snippet) {
				if !documentedFlags[f] {
					t.Errorf("README's %q uses --%s, which no Form or usageFooter scope documents", snippet, f)
				}
			}
			return
		}
		if readmeFlagOnlySpan.MatchString(snippet) {
			for _, f := range flagTokens(snippet) {
				if !documentedFlags[f] {
					t.Errorf("README names flag %q, which no Form or usageFooter scope documents", snippet)
				}
			}
		}
	}

	checked := 0
	inFence := false
	for _, line := range strings.Split(string(readme), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			s := strings.TrimSpace(line)
			if s == "" {
				continue
			}
			checked++
			checkSnippet(s)
			continue
		}
		for _, m := range readmeInlineCode.FindAllStringSubmatch(line, -1) {
			checked++
			checkSnippet(m[1])
		}
	}
	if checked == 0 {
		t.Fatal("no code spans found in README.md; the reader is broken, not the doc")
	}
}
