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
// out of it: neither has one. The optional second word carries the verb when
// the first is a noun; a flat command simply never has one to check.
var readmeDsxWord = regexp.MustCompile(`\bdsx ([a-zA-Z][a-zA-Z0-9-]*)(?: ([a-zA-Z][a-zA-Z0-9-]*))?`)

// readmeFlagOnlySpan matches a code span that names nothing but a flag,
// optionally with a placeholder value ("--json", "--if-match", "-j N", "-n").
// It is deliberately narrow: a whole shell line such as `go test -race ./...`
// does not match, so the go-toolchain flags in the Development section are
// never mistaken for dsx's own.
var readmeFlagOnlySpan = regexp.MustCompile(`^-{1,2}[a-zA-Z][a-zA-Z0-9-]*( [A-Z][a-zA-Z0-9]*)?$`)

// publishedDocs are the documents that tell a reader how to DRIVE dsx. README
// was the only one guarded while it was the only one that existed; a dead
// command name is exactly as wrong in CONTRIBUTING or an issue template, and
// the walker below reads a backticked span the same way wherever it finds one,
// YAML included.
//
// CLAUDE.md and PROTOCOL.md are deliberately NOT here, and that was measured
// rather than assumed: adding them fails on seven spans, every one of them
// correct. They quote flags dsx REMOVED (`--render`, kept as the record of why
// it went), flags belonging to other programs (`-X` is the Go linker's,
// `--exit-code` is git's), the `-C` global that footerFlagScopes skips by
// design, and a refusal string quoted precisely as an example of what dsx must
// NOT print (`dsx cat: flag provided but not defined`). A guard that fires on
// an accurate historical record is one people switch off, so the line is drawn
// at documents that instruct rather than document — the same narrowing
// TestEveryDsxInvocationInSourceNamesARealCommand made for source comments.
var publishedDocs = []string{
	"README.md",
	"CONTRIBUTING.md",
	"SECURITY.md",
	"CHANGELOG.md",
	filepath.Join(".github", "PULL_REQUEST_TEMPLATE.md"),
	filepath.Join(".github", "ISSUE_TEMPLATE", "bug_report.yml"),
	filepath.Join(".github", "ISSUE_TEMPLATE", "feature_request.yml"),
	filepath.Join(".github", "ISSUE_TEMPLATE", "protocol_drift.yml"),
}

// TestPublishedDocsNameOnlyRealCommandsAndFlags is the naming half of the doc
// discipline CLAUDE.md's C11 commit asks for. It does not read English: it
// walks every `dsx <word>` and every standalone `--flag`/`-x` a backtick or
// fenced code block puts in front of a reader, and checks each against the
// real registry — commandIndex for commands, and the union of every Form's
// flags plus every usageFooter scope for flags. Reverting a documented line to
// a dead command or flag name goes red here; nothing here proves the
// surrounding sentence is true.
func TestPublishedDocsNameOnlyRealCommandsAndFlags(t *testing.T) {
	t.Parallel()

	for _, doc := range publishedDocs {
		t.Run(doc, func(t *testing.T) {
			t.Parallel()
			checkDocNames(t, doc)
		})
	}
}

func checkDocNames(t *testing.T, doc string) {
	t.Helper()

	readme, err := os.ReadFile(filepath.Join("..", "..", doc))
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
		// A shell pipeline is several programs, and only the first is dsx.
		// Reading past the pipe made `… --json | jq -r …` fail on jq's own -r,
		// which meant README could never demonstrate a pipeline at all — and
		// the whole point of conv's --json shape is that `| jq` works on it.
		// Flags belonging to another program are that program's business.
		if i := strings.IndexByte(snippet, '|'); i >= 0 {
			snippet = snippet[:i]
		}
		if m := readmeDsxWord.FindStringSubmatch(snippet); m != nil {
			name := m[1]
			if g, isNoun := nounIndex[name]; isNoun {
				// A bare noun in prose is a real invocation — it lists the
				// verbs — so only a named verb is checked, and it is checked
				// against the full address rather than against the bare word.
				if m[2] != "" {
					name = g.Noun + " " + m[2]
				}
			}
			if _, ok := commandIndex[name]; !ok {
				if _, isNoun := nounIndex[name]; !isNoun {
					t.Errorf("%s names `dsx %s`, which is not a registered command (in %q)", doc, name, snippet)
				}
			}
			for _, f := range flagTokens(snippet) {
				if !documentedFlags[f] {
					t.Errorf("%s's %q uses --%s, which no Form or usageFooter scope documents", doc, snippet, f)
				}
			}
			return
		}
		if readmeFlagOnlySpan.MatchString(snippet) {
			for _, f := range flagTokens(snippet) {
				if !documentedFlags[f] {
					t.Errorf("%s names flag %q, which no Form or usageFooter scope documents", doc, snippet)
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
		t.Fatalf("no code spans found in %s; the reader is broken, not the doc", doc)
	}
}
