package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// Cover for main.go's dispatch/wiring and util.go's argument plumbing.
//
// What is worth pinning here is not that the functions run, but the three
// places where main.go can quietly mislead its caller:
//
//   - resolveSyncTarget invented an argument form. The old two-argument form is
//     a compatibility guarantee, and the ledger lookup it added must not fire
//     when the caller was explicit.
//   - conflictOutcome decides the exit code. Exit 0 on a real run that refused
//     to move bytes would let a caller carry on over work that exists nowhere
//     else.
//   - jsonSafe backs the "--json stdout is exactly one JSON document" promise. A
//     guarantee with exceptions is not one an agent can use.
//
// The fake endpoint here proves nothing about the protocol (see fake_test.go);
// it is only a way to reach emit/emitFlagged/cmdSync without a network.
//
// Every helper is prefixed `maincli` so it cannot collide with another area's.

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// maincliNoLedger is a `bound` func that fails the test if it is consulted.
// resolveSyncTarget's two-argument form must answer from its arguments alone.
func maincliNoLedger(t *testing.T) func(string) (string, error) {
	t.Helper()
	return func(dir string) (string, error) {
		t.Fatalf("the ledger was consulted for %q although the caller named the project explicitly", dir)
		return "", nil
	}
}

// maincliUnbound answers as a directory that carries no ledger.
func maincliUnbound(string) (string, error) { return "", nil }

// maincliCaptureStderr runs fn with os.Stderr redirected and returns what it
// wrote. It exists to prove flag's own output never reaches the real stderr;
// like captureStdout it swaps a process-global, so callers must not run in
// parallel.
func maincliCaptureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = w

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fn()

	os.Stderr = saved
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}

// maincliFake wires a client to a fake endpoint answering every tool with text.
func maincliFake(t *testing.T, text string) (*fakeMCP, *client) {
	t.Helper()
	f := newFakeMCP(t, func(string, map[string]any) fakeReply {
		return fakeReply{Text: text}
	})
	return f, f.client()
}

func maincliKind(t *testing.T, err error) errKind {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	return classify(err).Kind
}

// maincliWriteFile drops a file under dir, creating parents.
func maincliWriteFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// resolveSyncTarget — the compatibility guarantee
// ---------------------------------------------------------------------------

// The ledger lookup is new. Every caller that already typed both arguments must
// keep getting exactly what it typed, and must not pay a ledger read for it:
// a directory bound to another project, or holding a corrupt ledger, would
// otherwise start failing an invocation that used to work.
func TestSyncTargetWithBothArgumentsKeepsItsOldMeaningAndSkipsTheLedger(t *testing.T) {
	project, dir, err := resolveSyncTarget("pull", []string{"proj-uuid", "some/dir"}, maincliNoLedger(t))
	if err != nil {
		t.Fatalf("two explicit arguments failed: %v", err)
	}
	if project != "proj-uuid" || dir != "some/dir" {
		t.Fatalf("resolveSyncTarget = (%q, %q), want (%q, %q) verbatim", project, dir, "proj-uuid", "some/dir")
	}
}

func TestSyncTargetWithOneArgumentTakesItAsTheDirAndTheProjectFromTheLedger(t *testing.T) {
	var asked string
	project, dir, err := resolveSyncTarget("push", []string{"design"}, func(d string) (string, error) {
		asked = d
		return "from-ledger", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if dir != "design" {
		t.Errorf("dir = %q, want the single argument %q", dir, "design")
	}
	if project != "from-ledger" {
		t.Errorf("project = %q, want the ledger's", project)
	}
	if asked != "design" {
		t.Errorf("the ledger was read for %q, want %q — a lookup against the wrong directory answers about the wrong project", asked, "design")
	}
}

func TestSyncTargetWithNoArgumentsDefaultsToTheWorkingDirectory(t *testing.T) {
	var asked string
	project, dir, err := resolveSyncTarget("status", nil, func(d string) (string, error) {
		asked = d
		return "from-ledger", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if dir != "." {
		t.Errorf("dir = %q, want %q", dir, ".")
	}
	if project != "from-ledger" {
		t.Errorf("project = %q, want the ledger's", project)
	}
	if asked != "." {
		t.Errorf("the ledger was read for %q, want %q", asked, ".")
	}
}

func TestSyncTargetOnAnUnboundDirIsAUsageErrorThatSaysHowToBindIt(t *testing.T) {
	_, _, err := resolveSyncTarget("pull", []string{"fresh"}, maincliUnbound)
	if got := maincliKind(t, err); got != kindUsage {
		t.Fatalf("unbound dir classified %q, want %q: retrying the same command cannot help", got, kindUsage)
	}
	// "Errors say what to do next": the message has to carry the directory, the
	// mode, and the fact that naming the project once is enough.
	msg := err.Error()
	for _, want := range []string{"fresh", "ledger", "dsx pull <project> fresh"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not tell the user how to recover (missing %q): %q", want, msg)
		}
	}
}

func TestSyncTargetRefusesMoreThanTwoPositionalArguments(t *testing.T) {
	// Three arguments cannot be an abbreviation of anything; guessing which two
	// were meant would sync the wrong pair.
	_, _, err := resolveSyncTarget("pull", []string{"a", "b", "c"}, maincliNoLedger(t))
	if got := maincliKind(t, err); got != kindUsage {
		t.Fatalf("three arguments classified %q, want %q", got, kindUsage)
	}
}

// A ledger that cannot be read is not a directory without one. Collapsing the
// two would tell a user with a corrupt .dsx-state.json to "run `dsx pull
// <project> <dir>` once" — advice that overwrites the very file that would
// explain the failure.
func TestSyncTargetPropagatesALedgerReadFailureInsteadOfCallingItUnbound(t *testing.T) {
	boom := errors.New(".dsx-state.json is corrupt: unexpected end of JSON input")
	_, _, err := resolveSyncTarget("pull", []string{"design"}, func(string) (string, error) {
		return "", boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("a ledger read failure was swallowed: got %v, want it to carry %v", err, boom)
	}
	if strings.Contains(err.Error(), "carries no dsx ledger") {
		t.Error("a broken ledger was reported as an absent one")
	}
}

// ---------------------------------------------------------------------------
// conflictOutcome — the exit code
// ---------------------------------------------------------------------------

func TestConflictsOnARealRunAreExitThreeCarryingTheSortedPaths(t *testing.T) {
	err := conflictOutcome([]string{"z.css", "a.css", "m.css"}, false, "local differs; --force to overwrite")
	if err == nil {
		t.Fatal("a real run that refused to move bytes reported success; a caller reading exit 0 would carry on over work that exists nowhere else")
	}
	de := classify(err)
	if de.Kind != kindConflict {
		t.Errorf("kind = %q, want %q", de.Kind, kindConflict)
	}
	if got := exitCodeFor(err); got != exitConflict {
		t.Errorf("exit code = %d, want %d", got, exitConflict)
	}
	// Sorted so a caller diffing two runs sees a stable list.
	want := []string{"a.css", "m.css", "z.css"}
	if !reflect.DeepEqual(de.Paths, want) {
		t.Errorf("paths = %v, want %v (present and sorted)", de.Paths, want)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("the hint was dropped: %q", err.Error())
	}
}

// A dry run was asked to move nothing, so refusing to move something is the
// answer it wanted. `dsx status` runs through here on every invocation: exiting
// 3 would make "there is a conflict" indistinguishable from "status failed".
func TestConflictsOnADryRunAreNotAFailure(t *testing.T) {
	if err := conflictOutcome([]string{"a.css"}, true, "hint"); err != nil {
		t.Fatalf("a dry run reporting a conflict failed with %v; it did exactly what it was told", err)
	}
}

func TestNoConflictsIsSuccessInEitherMode(t *testing.T) {
	for _, dry := range []bool{false, true} {
		if err := conflictOutcome(nil, dry, "hint"); err != nil {
			t.Errorf("conflictOutcome(nil, %v) = %v, want nil", dry, err)
		}
		if err := conflictOutcome([]string{}, dry, "hint"); err != nil {
			t.Errorf("conflictOutcome([], %v) = %v, want nil", dry, err)
		}
	}
}

// ---------------------------------------------------------------------------
// jsonSafe — the one-JSON-document guarantee
// ---------------------------------------------------------------------------

func TestJSONSafePassesValidJSONThroughByteIdentical(t *testing.T) {
	// Most tools already answer in JSON. Re-encoding would reorder keys and
	// change numbers; a caller diffing two runs would see churn we invented.
	for _, doc := range []string{
		`{"files":[{"path":"a.css","size":12}]}`,
		`[]`,
		`{}`,
		`null`,
		`123`,
		`"a bare string is a JSON document"`,
		`{"nested":{"deep":[1,2,{"x":null}]}}`,
	} {
		if got := jsonSafe(doc, true); got != doc {
			t.Errorf("jsonSafe(%q) = %q, want it byte-identical", doc, got)
		}
	}
}

func TestJSONSafeWrapsProseSoStdoutStaysParseable(t *testing.T) {
	const prose = "Deleted 3 files from the project."
	got := jsonSafe(prose, true)

	if !json.Valid([]byte(got)) {
		t.Fatalf("prose was handed to a parser unwrapped: %q", got)
	}
	var wrapped struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(got), &wrapped); err != nil {
		t.Fatal(err)
	}
	if wrapped.Text != prose {
		t.Errorf("text = %q, want the original prose %q", wrapped.Text, prose)
	}
}

// The load-bearing case. A cheaper implementation — sniffing a leading `{` or
// `[` — passes every test above and fails this one, emitting a truncated
// document to a caller that is about to run a parser over it.
func TestJSONSafeWrapsAStringThatOnlyLooksLikeJSON(t *testing.T) {
	cases := []string{
		`{"almost": true`,        // unterminated object
		`[1, 2,`,                 // unterminated array
		`{'single': 'quotes'}`,   // not JSON at all
		`{"a":1}{"b":2}`,         // two documents, not one
		`{"trailing": "comma",}`, // trailing comma
		"{\n  broken\n}",         // braces around prose
	}
	for _, s := range cases {
		got := jsonSafe(s, true)
		if got == s {
			t.Errorf("jsonSafe(%q) passed it through unwrapped; it is not valid JSON", s)
			continue
		}
		if !json.Valid([]byte(got)) {
			t.Errorf("jsonSafe(%q) = %q, which is still not valid JSON", s, got)
		}
	}
}

func TestJSONSafeLeavesProseAloneWhenJSONWasNotAsked(t *testing.T) {
	const prose = "just words"
	if got := jsonSafe(prose, false); got != prose {
		t.Errorf("jsonSafe(%q, false) = %q; the human lane must not be wrapped", prose, got)
	}
	// Even a valid JSON document is untouched in prose mode.
	const doc = `{"a":1}`
	if got := jsonSafe(doc, false); got != doc {
		t.Errorf("jsonSafe(%q, false) = %q", doc, got)
	}
}

// ---------------------------------------------------------------------------
// usage / commandNames — anti-drift, both directions
// ---------------------------------------------------------------------------

// maincliUsageCommands returns every token that appears immediately after the
// word "dsx" in `usage`.
//
// Exact token equality, not a substring search: "rm" occurs inside "member-rm",
// "project" inside "projects", and "conv" inside "conv-put", so a
// strings.Contains check would report a command as documented on the strength
// of another command's name. Word boundaries alone are not enough either —
// `\brm\b` still matches inside "member-rm", because "-" is not a word
// character.
func maincliUsageCommands() map[string]bool {
	out := map[string]bool{}
	fields := strings.Fields(usage)
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] == "dsx" {
			out[fields[i+1]] = true
		}
	}
	return out
}

func TestUsageDocumentsEveryCommand(t *testing.T) {
	documented := maincliUsageCommands()

	// Guard the guard: an extractor that silently found nothing would make this
	// test pass by accident forever.
	if len(documented) < len(commandNames) {
		t.Fatalf("only %d `dsx <cmd>` forms found in usage but commandNames has %d — the extractor is broken, not the docs",
			len(documented), len(commandNames))
	}

	for _, name := range commandNames {
		if !documented[name] {
			t.Errorf("command %q is completable but undocumented — add it to `usage` in main.go", name)
		}
	}

	// Prove the match is exact. "member-r" is a prefix of the real "member-role"
	// and a near-miss of "member-rm"; if either satisfies it, the matcher is a
	// substring search and every assertion above is decorative.
	for _, fake := range []string{"member-r", "roject", "onv-put", "ul", "m"} {
		if documented[fake] {
			t.Errorf("the matcher accepted %q as a documented command; it is matching substrings", fake)
		}
	}
}

// maincliDispatchedCommands reads main.go and reports every literal `run`
// branches on: the case clauses of `switch cmd` plus the `cmd == "x"` tests.
//
// A switch cannot be reflected over, so the source is parsed. Doing it this way
// rather than restating the list by hand is the whole point: a case added to
// run() is checked without anyone remembering to update this test.
func maincliDispatchedCommands(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing main.go: %v", err)
	}
	var runFn *ast.FuncDecl
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == "run" {
			runFn = fd
			break
		}
	}
	if runFn == nil {
		t.Fatal("func run not found in main.go; this test parses it to enumerate the dispatch")
	}

	seen := map[string]bool{}
	add := func(e ast.Expr) {
		lit, ok := e.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return
		}
		v, err := strconv.Unquote(lit.Value)
		if err != nil {
			return
		}
		// "-h"/"--version" are flag spellings of a command, not command names;
		// nobody completes them as subcommands.
		if v == "" || strings.HasPrefix(v, "-") {
			return
		}
		seen[v] = true
	}
	ast.Inspect(runFn, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.SwitchStmt:
			if id, ok := s.Tag.(*ast.Ident); !ok || id.Name != "cmd" {
				return true
			}
			for _, stmt := range s.Body.List {
				if cc, ok := stmt.(*ast.CaseClause); ok {
					for _, e := range cc.List {
						add(e)
					}
				}
			}
		case *ast.BinaryExpr:
			// `if cmd == "auth"` — auth and doctor are dispatched this way.
			if s.Op == token.EQL {
				if id, ok := s.X.(*ast.Ident); ok && id.Name == "cmd" {
					add(s.Y)
				}
			}
		}
		return true
	})

	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

// The reverse of TestUsageDocumentsEveryCommand. That test can only see
// commands someone already added to commandNames; a command that run()
// dispatches but the list never learned about is invisible to Tab and to any
// agent that discovers subcommands by asking the shell.
func TestEveryDispatchedCommandIsCompletable(t *testing.T) {
	// This test was born recording a real defect: `put` was dispatched and
	// documented but missing from commandNames, so no shell ever offered it.
	// The exemption that held it is gone because the defect is fixed, and
	// run() now refuses anything the list does not name -- so a command
	// drifting out of it fails loudly instead of merely disappearing from Tab.
	dispatched := maincliDispatchedCommands(t)
	if len(dispatched) < 25 {
		t.Fatalf("only %d dispatched commands parsed out of run(); the parser is broken: %v", len(dispatched), dispatched)
	}

	completable := map[string]bool{}
	for _, n := range commandNames {
		completable[n] = true
	}
	for _, cmd := range dispatched {
		if !completable[cmd] {
			t.Errorf("run() dispatches %q but commandNames omits it: run() will reject it as unknown, "+
				"and no shell will offer it", cmd)
		}
	}
}

func TestCommandNamesHasNoDuplicatesOrEmptyEntries(t *testing.T) {
	seen := map[string]bool{}
	for _, n := range commandNames {
		if n == "" {
			t.Error("commandNames holds an empty entry; it would complete to nothing")
		}
		if seen[n] {
			t.Errorf("commandNames lists %q twice; completion would offer it twice", n)
		}
		seen[n] = true
	}
}

// ---------------------------------------------------------------------------
// need1 / need2
// ---------------------------------------------------------------------------

func TestNeed1AndNeed2ClassifyTooFewArgumentsAsUsage(t *testing.T) {
	// Exit 2 is the contract for "running it again will not help". A missing
	// argument reported as a generic failure invites a retry loop.
	if _, _, err := need1(nil, "project <id>"); maincliKind(t, err) != kindUsage {
		t.Errorf("need1(nil) classified %q, want %q", maincliKind(t, err), kindUsage)
	}
	if _, _, err := need1([]string{}, "project <id>"); err == nil {
		t.Error("need1 accepted zero arguments")
	}
	for _, args := range [][]string{nil, {"only-one"}} {
		_, _, _, err := need2(args, "cp <src> <dst>")
		if maincliKind(t, err) != kindUsage {
			t.Errorf("need2(%v) classified %q, want %q", args, maincliKind(t, err), kindUsage)
		}
	}
	if !strings.Contains(func() string { _, _, err := need1(nil, "project <id>"); return err.Error() }(), "project <id>") {
		t.Error("the usage error must echo the form the caller should have typed")
	}
}

func TestNeed1AndNeed2ReturnTheRestForTheNextParser(t *testing.T) {
	first, rest, err := need1([]string{"a", "b", "c"}, "f")
	if err != nil || first != "a" || !reflect.DeepEqual(rest, []string{"b", "c"}) {
		t.Fatalf("need1 = (%q, %v, %v)", first, rest, err)
	}
	a, b, rest2, err := need2([]string{"a", "b", "c"}, "f")
	if err != nil || a != "a" || b != "b" || !reflect.DeepEqual(rest2, []string{"c"}) {
		t.Fatalf("need2 = (%q, %q, %v, %v)", a, b, rest2, err)
	}
}

// ---------------------------------------------------------------------------
// parseArgs
// ---------------------------------------------------------------------------

// Go's flag package stops at the first non-flag token. Left alone it would take
// `dsx tree <project> --json` and silently hand the caller prose while it waited
// for JSON — no error, no clue.
func TestParseArgsFindsFlagsBeforeBetweenAndAfterPositionals(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantPos []string
	}{
		{"no flags", []string{"proj", "path"}, []string{"proj", "path"}},
		{"flag first", []string{"--json", "proj", "path"}, []string{"proj", "path"}},
		{"flag last", []string{"proj", "path", "--json"}, []string{"proj", "path"}},
		{"flag between", []string{"proj", "--json", "path"}, []string{"proj", "path"}},
		{"flag only", []string{"--json"}, nil},
		{"nothing at all", nil, nil},
		{"explicitly false, still parsed", []string{"proj", "--json=false"}, []string{"proj"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFlagSet("tree")
			asJSON := jsonFlag(fs)
			pos, err := parseArgs(fs, tc.args)
			if err != nil {
				t.Fatalf("parseArgs(%v): %v", tc.args, err)
			}
			if !reflect.DeepEqual(pos, tc.wantPos) {
				t.Errorf("positionals = %v, want %v", pos, tc.wantPos)
			}
			want := strings.Contains(strings.Join(tc.args, " "), "--json") &&
				!strings.Contains(strings.Join(tc.args, " "), "--json=false")
			if *asJSON != want {
				t.Errorf("--json = %v, want %v: the flag was not seen where it landed", *asJSON, want)
			}
		})
	}
}

func TestParseArgsKeepsPositionalOrder(t *testing.T) {
	// `dsx cp <src> <dst>` means something different reversed.
	fs := newFlagSet("cp")
	_ = jsonFlag(fs)
	pos, err := parseArgs(fs, []string{"src.css", "--json", "dst.css"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pos, []string{"src.css", "dst.css"}) {
		t.Fatalf("positionals = %v, want them in the order they were typed", pos)
	}
}

func TestParseArgsTreatsAnUnknownFlagAsUsageNotAsAPositional(t *testing.T) {
	// A typo'd flag swallowed as a positional would be handed to the server as a
	// path. Retrying it verbatim cannot help, so it is exit 2.
	fs := newFlagSet("tree")
	_ = jsonFlag(fs)
	pos, err := parseArgs(fs, []string{"proj", "--bogus"})
	if err == nil {
		t.Fatalf("an unknown flag was accepted, positionals = %v", pos)
	}
	if got := maincliKind(t, err); got != kindUsage {
		t.Errorf("unknown flag classified %q, want %q", got, kindUsage)
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("the message does not name the offending flag: %q", err.Error())
	}
}

// newFlagSet discards flag's own output. If it did not, flag would print its
// error and a full usage dump straight to stderr, ahead of and around dsx's
// single classified line — breaking the --json contract for every command that
// mistypes a flag.
func TestNewFlagSetKeepsFlagsOwnChatterOffStderr(t *testing.T) {
	var err error
	leaked := maincliCaptureStderr(t, func() {
		fs := newFlagSet("tree")
		_ = jsonFlag(fs)
		_, err = parseArgs(fs, []string{"--bogus"})
	})
	if err == nil {
		t.Fatal("expected a usage error")
	}
	if leaked != "" {
		t.Errorf("flag wrote to stderr despite SetOutput(io.Discard): %q", leaked)
	}

	// Control, so the assertion above cannot pass vacuously: a FlagSet built
	// without newFlagSet does leak. (cmdSync in main.go builds its FlagSet with
	// flag.NewFlagSet directly and is subject to exactly this.)
	control := maincliCaptureStderr(t, func() {
		raw := flag.NewFlagSet("raw", flag.ContinueOnError)
		_ = raw.Bool("json", false, "")
		_, _ = parseArgs(raw, []string{"--bogus"})
	})
	if control == "" {
		t.Skip("flag no longer writes to stderr by default; the discard assertion above has lost its teeth")
	}
}

// ---------------------------------------------------------------------------
// humanBytes
// ---------------------------------------------------------------------------

func TestHumanBytesAcrossEveryUnitBoundary(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{512, "512 B"},
		{1023, "1023 B"}, // last value before the unit switches
		{1024, "1.0 KB"}, // first value after it
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{1024 * 1024 * 3 / 2, "1.5 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{1024 * 1024 * 1024 * 1024, "1.0 TB"},
	}
	for _, tc := range cases {
		if got := humanBytes(tc.n); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// KNOWN DEFECT, recorded rather than fixed (source is out of scope here):
// humanBytes indexes "KMGT" with an exponent it never clamps, so any total of
// 1 PiB or more (n >= 1<<50) panics with "index out of range [4] with length 4".
// 1<<50 - 1 is the last safe value. It is not reachable from a Design project's
// file sizes today, which is the only reason this is not urgent — but the guard
// is missing, not unnecessary, and rep.Bytes flows here on every sync summary.
//
// This test pins the range that does work, up to and including that ceiling. If
// a clamp is added, extend it past 1<<50 and drop this comment.
func TestHumanBytesStaysWithinItsUnitTableForEveryReachableTotal(t *testing.T) {
	for _, n := range []int64{1 << 40, 1 << 45, 1<<50 - 1} {
		got := humanBytes(n)
		if got == "" {
			t.Errorf("humanBytes(%d) returned nothing", n)
		}
		if !strings.HasSuffix(got, "B") {
			t.Errorf("humanBytes(%d) = %q, want a unit suffix", n, got)
		}
	}
}

// ---------------------------------------------------------------------------
// splitList
// ---------------------------------------------------------------------------

// splitList feeds `dsx plan --deletes a,b`. An empty string surviving into that
// list is a delete request for the path "", and a stray space makes " a.css" a
// path the project does not have — both are silent, both are the server's
// problem by the time anyone notices.
func TestSplitListDropsEmptiesAndTrimsEveryEntry(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace only", "   \t ", nil},
		{"single", "a.css", []string{"a.css"}},
		{"plain pair", "a.css,b.css", []string{"a.css", "b.css"}},
		{"trailing comma", "a.css,b.css,", []string{"a.css", "b.css"}},
		{"leading comma", ",a.css", []string{"a.css"}},
		{"doubled comma", "a.css,,b.css", []string{"a.css", "b.css"}},
		{"spaces around entries", " a.css , b.css ", []string{"a.css", "b.css"}},
		{"only commas", ",,,", nil},
		{"space between commas", "a.css, ,b.css", []string{"a.css", "b.css"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitList(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("splitList(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("splitList(%q) = %v, want %v", tc.in, got, tc.want)
				}
			}
			for _, p := range got {
				if p == "" {
					t.Errorf("splitList(%q) kept an empty entry: %v", tc.in, got)
				}
				if p != strings.TrimSpace(p) {
					t.Errorf("splitList(%q) kept untrimmed entry %q", tc.in, p)
				}
			}
		})
	}
}

func TestSplitListKeepsInnerSpacesInAName(t *testing.T) {
	// Trimming the ends is not licence to rewrite a path the user actually has.
	got := splitList(" my file.css ,b.css")
	if len(got) != 2 || got[0] != "my file.css" {
		t.Fatalf("splitList = %v, want the inner space preserved", got)
	}
}

// ---------------------------------------------------------------------------
// emit / emitFlagged
// ---------------------------------------------------------------------------

func TestEmitPrintsTheToolsTextVerbatimInProseMode(t *testing.T) {
	_, c := maincliFake(t, "Deleted 3 files.")
	out, err := captureStdout(t, func() error {
		return emit(context.Background(), c, "delete_files", map[string]any{"x": 1}, false)
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "Deleted 3 files.\n" {
		t.Errorf("stdout = %q, want the tool's text verbatim", out)
	}
}

func TestEmitUnderJSONAlwaysPrintsExactlyOneJSONDocument(t *testing.T) {
	// The contract an agent relies on: it must not have to learn which tools
	// happen to answer in JSON and which answer in prose.
	for _, text := range []string{
		`{"projects":[]}`,
		"Deleted 3 files.",
		`{"truncated": `,
	} {
		out, err := captureStdout(t, func() error {
			_, c := maincliFake(t, text)
			return emit(context.Background(), c, "list_projects", map[string]any{}, true)
		})
		if err != nil {
			t.Fatal(err)
		}
		line := strings.TrimSuffix(out, "\n")
		if !json.Valid([]byte(line)) {
			t.Errorf("tool text %q emitted %q, which is not JSON", text, line)
		}
		if strings.Contains(line, "\n") {
			t.Errorf("tool text %q emitted more than one line: %q", text, line)
		}
	}
}

func TestEmitPrintsNothingWhenTheToolFails(t *testing.T) {
	// Half a document is worse than none: a caller parsing stdout would take the
	// empty line as a successful empty answer.
	f := newFakeMCP(t, func(string, map[string]any) fakeReply {
		return fakeReply{Text: "project not found", IsError: true}
	})
	c := f.client()
	out, err := captureStdout(t, func() error {
		return emit(context.Background(), c, "get_project", map[string]any{"project_id": "nope"}, true)
	})
	if err == nil {
		t.Fatal("a tool error was reported as success")
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing printed on failure", out)
	}
}

func TestEmitFlaggedAcceptsJSONAfterThePositionalsEveryCommandTakesIt(t *testing.T) {
	f, c := maincliFake(t, "plain prose")
	var gotPos []string
	out, err := captureStdout(t, func() error {
		return emitFlagged(context.Background(), c, "project", []string{"proj-uuid", "--json"},
			func(pos []string) (string, map[string]any, error) {
				gotPos = pos
				return "get_project", map[string]any{"project_id": pos[0]}, nil
			})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotPos, []string{"proj-uuid"}) {
		t.Errorf("build saw positionals %v, want the flag stripped out", gotPos)
	}
	if !json.Valid([]byte(strings.TrimSuffix(out, "\n"))) {
		t.Errorf("--json after the positional was ignored: %q", out)
	}
	call := syncFirstCall(t, f, "get_project")
	if call.Args["project_id"] != "proj-uuid" {
		t.Errorf("project_id = %v, want %q", call.Args["project_id"], "proj-uuid")
	}
}

func TestEmitFlaggedTouchesNoNetworkWhenTheArgumentsAreWrong(t *testing.T) {
	// build's error has to stop the call. Calling the tool anyway would send the
	// server a request assembled from arguments we already rejected.
	f, c := maincliFake(t, "unreachable")
	err := emitFlagged(context.Background(), c, "project", nil, func(pos []string) (string, map[string]any, error) {
		_, _, err := need1(pos, "project <id>")
		return "", nil, err
	})
	if got := maincliKind(t, err); got != kindUsage {
		t.Errorf("kind = %q, want %q", got, kindUsage)
	}
	if n := f.countTool("get_project"); n != 0 {
		t.Errorf("%d tool calls made despite a usage error", n)
	}
}

func TestEmitFlaggedRejectsAnUnknownFlagBeforeCallingTheTool(t *testing.T) {
	f, c := maincliFake(t, "unreachable")
	err := emitFlagged(context.Background(), c, "projects", []string{"--bogus"}, func([]string) (string, map[string]any, error) {
		t.Fatal("build ran despite an unparseable flag")
		return "", nil, nil
	})
	if got := maincliKind(t, err); got != kindUsage {
		t.Errorf("kind = %q, want %q", got, kindUsage)
	}
	if len(f.recorded()) != 0 {
		t.Errorf("the endpoint was contacted: %v", f.recorded())
	}
}

// ---------------------------------------------------------------------------
// cmdAuth — the one thing this binary must never do
// ---------------------------------------------------------------------------

// maincliFakeLogin points credential resolution at a temp dir holding a
// Claude-Code-shaped credentials file, and returns the token it planted.
//
// The config dir is overridden, which makes keychainServiceName hash it into a
// service name no keychain holds — so the chain falls through to this file and
// the real login is never read. See auth.go.
func maincliFakeLogin(t *testing.T, token string, scopes []string, expiresAt int64) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	writeCreds(t, dir, oauthCreds{AccessToken: token, Scopes: scopes, ExpiresAt: expiresAt})
	return token
}

const maincliFarFuture = 1900000000000 // 2030-03-17, well past any test run

func TestAuthNeverPrintsTheTokenInEitherMode(t *testing.T) {
	// `dsx auth` is the command most likely to be run with a terminal being
	// recorded. It reports the credential's metadata and must never render the
	// credential.
	const secret = "sk-ant-oat01-SECRET-DO-NOT-PRINT"
	maincliFakeLogin(t, secret, []string{"user:inference", "user:profile"}, maincliFarFuture)

	// DSX_TOKEN is set as a second secret that must not leak either.
	//
	// Note while here — KNOWN DEFECT, recorded rather than fixed: cmdAuth goes
	// through tokenInfo(), which reads the *stored* credential and never
	// consults DSX_TOKEN. usage says "DSX_TOKEN overrides the stored
	// credential", and every other command honours it, so with DSX_TOKEN set
	// `dsx auth` reports the scopes and expiry of a credential the next request
	// will not use. That misleads the exact caller who runs `dsx auth` to
	// explain a 401.
	t.Setenv("DSX_TOKEN", "sk-ant-oat01-ENV-SECRET")

	for _, args := range [][]string{nil, {"--json"}} {
		out, err := captureStdout(t, func() error { return cmdAuth(args) })
		if err != nil {
			t.Fatalf("cmdAuth(%v): %v", args, err)
		}
		if out == "" {
			t.Fatalf("cmdAuth(%v) printed nothing", args)
		}
		if strings.Contains(out, secret) {
			t.Errorf("cmdAuth(%v) printed the stored token:\n%s", args, out)
		}
		if strings.Contains(out, "sk-ant-oat01-ENV-SECRET") {
			t.Errorf("cmdAuth(%v) printed DSX_TOKEN:\n%s", args, out)
		}
		// A partial leak is still a leak: no fragment of the secret may appear.
		if strings.Contains(out, "sk-ant") {
			t.Errorf("cmdAuth(%v) printed something token-shaped:\n%s", args, out)
		}
	}
}

func TestAuthJSONIsOneParseableDocumentCarryingScopesAndExpiry(t *testing.T) {
	maincliFakeLogin(t, "sk-ant-oat01-x", []string{"user:inference", "user:profile"}, maincliFarFuture)

	out, err := captureStdout(t, func() error { return cmdAuth([]string{"--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSuffix(out, "\n")
	if strings.Contains(line, "\n") {
		t.Fatalf("--json output is more than one line: %q", out)
	}
	var got struct {
		Scopes  []string `json:"scopes"`
		Expires string   `json:"expires"`
	}
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("--json output is not JSON: %v\n%s", err, line)
	}
	if !reflect.DeepEqual(got.Scopes, []string{"user:inference", "user:profile"}) {
		t.Errorf("scopes = %v, want the stored ones", got.Scopes)
	}
	if got.Expires == "" {
		t.Error("expiry missing; it is the field a caller checks before blaming the network")
	}
}

func TestAuthProseModeReportsScopesAndExpiryWithoutJSON(t *testing.T) {
	maincliFakeLogin(t, "sk-ant-oat01-x", []string{"user:mcp_servers"}, maincliFarFuture)

	out, err := captureStdout(t, func() error { return cmdAuth(nil) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "user:mcp_servers") {
		t.Errorf("prose output dropped the scopes: %q", out)
	}
	if !strings.Contains(out, "expires") {
		t.Errorf("prose output dropped the expiry: %q", out)
	}
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("prose mode emitted JSON: %q", out)
	}
}

func TestAuthReportsAMissingLoginAsAuthNotAsSuccess(t *testing.T) {
	dir := t.TempDir() // no credentials file written
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	out, err := captureStdout(t, func() error { return cmdAuth(nil) })
	if err == nil {
		t.Fatalf("cmdAuth with no stored login succeeded, printing %q", out)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing when there is nothing to report", out)
	}
}

func TestAuthRejectsAnUnknownFlagWithoutReadingAnyCredential(t *testing.T) {
	err := cmdAuth([]string{"--bogus"})
	if got := maincliKind(t, err); got != kindUsage {
		t.Errorf("kind = %q, want %q", got, kindUsage)
	}
}

// ---------------------------------------------------------------------------
// boundProject
// ---------------------------------------------------------------------------

func TestBoundProjectReadsTheLedgerAndIsSilentWhenThereIsNone(t *testing.T) {
	dir := t.TempDir()
	got, err := boundProject(dir)
	if err != nil {
		t.Fatalf("a directory without a ledger is not an error: %v", err)
	}
	if got != "" {
		t.Errorf("boundProject on a fresh dir = %q, want \"\"", got)
	}

	syncSeedState(t, dir, syncState{ProjectID: "proj-uuid"})
	got, err = boundProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "proj-uuid" {
		t.Errorf("boundProject = %q, want %q", got, "proj-uuid")
	}
}

func TestBoundProjectSurfacesACorruptLedgerRatherThanReportingUnbound(t *testing.T) {
	// Reported as unbound, a corrupt ledger sends the user to re-run
	// `dsx pull <project> <dir>` — which rewrites the evidence.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, stateFileName), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := boundProject(dir); err == nil {
		t.Fatal("a corrupt ledger read as an unbound directory")
	}
}

// ---------------------------------------------------------------------------
// cmdSync — conflictOutcome wired end to end
// ---------------------------------------------------------------------------

// maincliConflictedPull sets up a directory where a pull must refuse: the file
// on disk was edited locally *and* the server moved on. Per invariant 2 the
// refusal keys off the bytes, not the etag — an etag test alone cannot see this
// case.
func maincliConflictedPull(t *testing.T) (*fakeMCP, *client, string) {
	t.Helper()
	dir := t.TempDir()
	const project = "proj-uuid"

	maincliWriteFile(t, dir, "a.css", "LOCAL EDIT")
	syncSeedState(t, dir, syncState{
		ProjectID: project,
		Files: map[string]fileState{
			"a.css": {Etag: "e1", Size: 3, SHA: sha256hex([]byte("old"))},
		},
	})

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("a.css", "e2", 9))}
		case "read_file":
			return fakeReply{Text: envelopeFor("a.css", "e2", "SERVER!!!")}
		}
		return fakeReply{Text: "{}", IsError: true}
	})
	return f, f.client(), dir
}

func TestPullThatRefusedToMoveBytesExitsThreeNotZero(t *testing.T) {
	_, c, dir := maincliConflictedPull(t)

	out, err := captureStdout(t, func() error {
		return cmdSync(context.Background(), c, "pull", []string{"proj-uuid", dir})
	})
	if err == nil {
		t.Fatalf("a pull that refused every file reported success; output was %q", out)
	}
	if got := exitCodeFor(err); got != exitConflict {
		t.Fatalf("exit code = %d, want %d — a caller reading 0 would carry on over the local edit", got, exitConflict)
	}
	if paths := classify(err).Paths; len(paths) != 1 || paths[0] != "a.css" {
		t.Errorf("conflict paths = %v, want [a.css] so the caller knows what to look at", paths)
	}
	// The summary still goes out: the caller is told what happened as well as
	// that it failed.
	if !strings.Contains(out, "conflicts 1") {
		t.Errorf("summary did not mention the conflict: %q", out)
	}
	// The local edit is the only copy of that work.
	if b, readErr := os.ReadFile(filepath.Join(dir, "a.css")); readErr != nil || string(b) != "LOCAL EDIT" {
		t.Fatalf("the local edit was overwritten: %q, %v", b, readErr)
	}
}

func TestDryRunPullReportsTheSameConflictAndStillExitsZero(t *testing.T) {
	// -n was asked to move nothing. Refusing to move something is the answer it
	// wanted, and `dsx status` runs through this path on every invocation.
	_, c, dir := maincliConflictedPull(t)

	out, err := captureStdout(t, func() error {
		return cmdSync(context.Background(), c, "pull", []string{"-n", "proj-uuid", dir})
	})
	if err != nil {
		t.Fatalf("a dry run reporting a conflict failed with %v", err)
	}
	if !strings.Contains(out, "conflicts 1") {
		t.Errorf("the dry run did not report the conflict it found: %q", out)
	}
}

func TestSyncResolvesTheProjectFromTheLedgerWhenOnlyTheDirIsGiven(t *testing.T) {
	f, c, dir := maincliConflictedPull(t)

	// No project argument: the ledger seeded above is the only place the binding
	// is known.
	_, err := captureStdout(t, func() error {
		return cmdSync(context.Background(), c, "status", []string{dir})
	})
	if err != nil {
		t.Fatal(err)
	}
	call := syncFirstCall(t, f, "list_files")
	if call.Args["project_id"] != "proj-uuid" {
		t.Errorf("list_files project_id = %v, want the ledger's %q", call.Args["project_id"], "proj-uuid")
	}
}

func TestSyncOnAnUnboundDirFailsBeforeTouchingTheNetwork(t *testing.T) {
	f, c := maincliFake(t, "unreachable")
	dir := t.TempDir()

	err := cmdSync(context.Background(), c, "pull", []string{dir})
	if got := maincliKind(t, err); got != kindUsage {
		t.Fatalf("kind = %q, want %q", got, kindUsage)
	}
	if len(f.recorded()) != 0 {
		t.Errorf("the endpoint was contacted for a directory with no known project: %v", f.recorded())
	}
	// It must not have created the directory's ledger as a side effect either.
	if syncLedgerExists(t, dir) {
		t.Error("a failed resolve left a ledger behind")
	}
}

func TestStatusReportsBothDirectionsAndTransfersNothing(t *testing.T) {
	f, c, dir := maincliConflictedPull(t)

	out, err := captureStdout(t, func() error {
		return cmdSync(context.Background(), c, "status", []string{"proj-uuid", dir})
	})
	if err != nil {
		t.Fatalf("status reported a conflict as a failure: %v", err)
	}
	if !strings.Contains(out, "pull:") || !strings.Contains(out, "push:") {
		t.Errorf("status must report both directions: %q", out)
	}
	if n := f.countTool("read_file"); n != 0 {
		t.Errorf("status fetched %d file(s); it transfers nothing", n)
	}
	if n := f.countTool("write_files"); n != 0 {
		t.Errorf("status wrote %d file(s); it transfers nothing", n)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.css")); string(b) != "LOCAL EDIT" {
		t.Errorf("status modified the working tree: %q", b)
	}
}

func TestStatusJSONIsOneDocumentHoldingBothReports(t *testing.T) {
	_, c, dir := maincliConflictedPull(t)

	out, err := captureStdout(t, func() error {
		return cmdSync(context.Background(), c, "status", []string{"proj-uuid", dir, "--json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSuffix(out, "\n")
	var got struct {
		Pull *pullReport `json:"pull"`
		Push *pushReport `json:"push"`
	}
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("status --json is not one JSON document: %v\n%s", err, line)
	}
	if got.Pull == nil || got.Push == nil {
		t.Fatalf("status --json must carry both directions: %s", line)
	}
	if len(got.Pull.Conflicts) != 1 {
		t.Errorf("pull conflicts = %v, want the one we set up", got.Pull.Conflicts)
	}
}

func TestSyncQuietPrintsNothingButStillReportsTheConflict(t *testing.T) {
	// -q suppresses the summary, not the exit code: output width is a token
	// budget, but a caller must still learn it did not get what it asked for.
	_, c, dir := maincliConflictedPull(t)

	out, err := captureStdout(t, func() error {
		return cmdSync(context.Background(), c, "pull", []string{"-q", "proj-uuid", dir})
	})
	if out != "" {
		t.Errorf("-q printed %q", out)
	}
	if got := exitCodeFor(err); got != exitConflict {
		t.Errorf("exit code under -q = %d, want %d", got, exitConflict)
	}
}

func TestStatusQuietPrintsNothing(t *testing.T) {
	_, c, dir := maincliConflictedPull(t)
	out, err := captureStdout(t, func() error {
		return cmdSync(context.Background(), c, "status", []string{"-q", "proj-uuid", dir})
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Errorf("-q printed %q", out)
	}
}

func TestSyncClampsConcurrencyBelowOneToOneInsteadOfHanging(t *testing.T) {
	// -j 0 would otherwise mean a worker pool that never starts.
	_, c, dir := maincliConflictedPull(t)
	_, err := captureStdout(t, func() error {
		return cmdSync(context.Background(), c, "status", []string{"-j", "0", "proj-uuid", dir})
	})
	if err != nil {
		t.Fatalf("-j 0: %v", err)
	}
}

func TestSyncRejectsAnUnknownFlagAsUsage(t *testing.T) {
	f, c := maincliFake(t, "unreachable")
	err := cmdSync(context.Background(), c, "pull", []string{"proj", ".", "--bogus"})
	if got := maincliKind(t, err); got != kindUsage {
		t.Errorf("kind = %q, want %q", got, kindUsage)
	}
	if len(f.recorded()) != 0 {
		t.Errorf("the endpoint was contacted despite a bad flag: %v", f.recorded())
	}
}

func TestPullCreatesTheTargetDirectoryButPushDoesNot(t *testing.T) {
	// A pull is allowed to make the place it is pulling into. A push inventing
	// an empty directory would then push nothing and, with --prune, read that as
	// "delete everything on the server".
	_, c := maincliFake(t, "unreachable")
	base := t.TempDir()

	// Both runs name the project explicitly, so both get past resolve and fail
	// later against the fake's unusable listing. What is asserted is the
	// side effect each one left behind on the way.
	missing := filepath.Join(base, "made-by-pull")
	_ = cmdSync(context.Background(), c, "pull", []string{"proj", missing})
	if _, err := os.Stat(missing); err != nil {
		t.Errorf("pull did not create its target directory: %v", err)
	}

	never := filepath.Join(base, "not-made-by-push")
	_ = cmdSync(context.Background(), c, "push", []string{"proj", never})
	if _, err := os.Stat(never); err == nil {
		t.Error("push created a directory that did not exist; an empty tree pushed with --prune deletes the project")
	}
}

// ---------------------------------------------------------------------------
// run() — dispatch, and the errors that escape before a client exists
// ---------------------------------------------------------------------------

// maincliRun drives run() with a fabricated argv.
func maincliRun(t *testing.T, argv ...string) (string, error) {
	t.Helper()
	saved := os.Args
	os.Args = append([]string{"dsx"}, argv...)
	t.Cleanup(func() { os.Args = saved })
	return captureStdout(t, run)
}

func TestRunWithNoArgumentsPrintsUsageAndSucceeds(t *testing.T) {
	out, err := maincliRun(t)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "SYNC") || !strings.Contains(out, "EXIT CODES") {
		t.Errorf("bare `dsx` did not print usage: %q", out)
	}
}

func TestRunHelpAndVersionAnswerWithoutACredential(t *testing.T) {
	// Neither needs a login. Requiring one would make `dsx help` fail exactly
	// when a confused user most needs it.
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	for _, spelling := range []string{"help", "-h", "--help"} {
		out, err := maincliRun(t, spelling)
		if err != nil {
			t.Errorf("dsx %s: %v", spelling, err)
		}
		if !strings.Contains(out, "SYNC") {
			t.Errorf("dsx %s did not print usage: %q", spelling, out)
		}
	}
	for _, spelling := range []string{"version", "-v", "--version"} {
		out, err := maincliRun(t, spelling)
		if err != nil {
			t.Errorf("dsx %s: %v", spelling, err)
		}
		if strings.TrimSpace(out) == "" {
			t.Errorf("dsx %s printed nothing", spelling)
		}
	}
}

func TestRunUnknownCommandIsAUsageErrorNamingTheCommand(t *testing.T) {
	// DSX_TOKEN is set only to get past loadToken(), which run() calls before it
	// reaches the switch's default case.
	//
	// KNOWN DEFECT, recorded rather than fixed (source is out of scope here):
	// that ordering means `dsx pulll` on a machine with no login reports exit 5
	// "no Claude Code login found" instead of exit 2 "unknown command pulll" —
	// dsx blames the user's credentials for their typo. Measured, not inferred.
	// Once the default case is reachable without a token, drop this Setenv and
	// the test still passes.
	t.Setenv("DSX_TOKEN", "test-token")
	_, err := maincliRun(t, "pulll")
	if got := maincliKind(t, err); got != kindUsage {
		t.Fatalf("unknown command classified %q, want %q", got, kindUsage)
	}
	if !strings.Contains(err.Error(), "pulll") {
		t.Errorf("the message does not name the command: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "dsx help") {
		t.Errorf("errors say what to do next: %q", err.Error())
	}
}

func TestRunCompletionAnswersForEveryShellAndRefusesOthers(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir()) // completion must not need a login

	for _, shell := range []string{"bash", "zsh", "fish"} {
		out, err := maincliRun(t, "completion", shell)
		if err != nil {
			t.Errorf("dsx completion %s: %v", shell, err)
		}
		if !strings.Contains(out, "dsx") {
			t.Errorf("dsx completion %s produced nothing usable: %q", shell, out)
		}
	}
	if _, err := maincliRun(t, "completion", "powershell"); maincliKind(t, err) != kindUsage {
		t.Errorf("an unsupported shell must be a usage error")
	}
	if _, err := maincliRun(t, "completion"); maincliKind(t, err) != kindUsage {
		t.Errorf("a missing shell argument must be a usage error")
	}
}

// ---------------------------------------------------------------------------
// exitCodeFor / renderError, one per kind, end to end
// ---------------------------------------------------------------------------

func TestEveryKindRendersAndExitsConsistently(t *testing.T) {
	cases := []struct {
		err      error
		wantCode int
	}{
		{&dsxError{Kind: kindFailure, Msg: "something broke"}, exitFailure},
		{usageError("cp <src> <dst>"), exitUsage},
		{conflictError([]string{"b.css", "a.css"}, "local differs"), exitConflict},
		{&dsxError{Kind: kindTransport, Msg: "http 503"}, exitTransport},
		{&dsxError{Kind: kindAuth, Msg: "token expired"}, exitAuth},
		{&dsxError{Kind: kindProtocol, Msg: "unparseable reply"}, exitFailure},
		{&dsxError{Kind: kindLocal, Msg: "disk full"}, exitFailure},
	}
	for _, tc := range cases {
		kind := classify(tc.err).Kind
		t.Run(string(kind), func(t *testing.T) {
			if got := exitCodeFor(tc.err); got != tc.wantCode {
				t.Errorf("exit code = %d, want %d", got, tc.wantCode)
			}

			// Prose: readable, prefixed, never JSON.
			prose := renderError(tc.err, false)
			if !strings.HasPrefix(prose, "dsx: ") {
				t.Errorf("prose = %q, want the dsx: prefix", prose)
			}

			// JSON: one line, and the kind token is what an agent matches on.
			line := renderError(tc.err, true)
			if strings.Contains(line, "\n") {
				t.Errorf("--json error spans lines: %q", line)
			}
			var got errorPayload
			if err := json.Unmarshal([]byte(line), &got); err != nil {
				t.Fatalf("--json error is not JSON: %v\n%s", err, line)
			}
			if got.Error != kind {
				t.Errorf("error token = %q, want %q", got.Error, kind)
			}
			if got.Message == "" {
				t.Errorf("message dropped: %s", line)
			}
		})
	}
}

// kindProtocol and kindLocal both collapse onto exit 1, so the JSON token is the
// only thing that still tells them apart. Losing it would leave a caller unable
// to distinguish "the server said something we cannot parse" from "the disk is
// full" — one is worth retrying elsewhere, the other is not.
func TestKindsSharingAnExitCodeStayDistinctInJSON(t *testing.T) {
	seen := map[errKind]bool{}
	for _, k := range []errKind{kindFailure, kindProtocol, kindLocal} {
		line := renderError(&dsxError{Kind: k, Msg: "x"}, true)
		var got errorPayload
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatal(err)
		}
		if seen[got.Error] {
			t.Errorf("kind %q is not distinguishable in --json", k)
		}
		seen[got.Error] = true
		if exitCodeFor(&dsxError{Kind: k}) != exitFailure {
			t.Errorf("kind %q no longer exits %d", k, exitFailure)
		}
	}
}

// The renderer runs outside every FlagSet so that a failure raised before flags
// were parsed still honours --json. This pins the pair main() actually calls.
func TestErrorsRaisedBeforeFlagParsingStillHonourJSON(t *testing.T) {
	argv := []string{"pull", "--json", "a", "b", "c"}
	_, _, err := resolveSyncTarget("pull", argv[2:], maincliNoLedger(t))
	if err == nil {
		t.Fatal("expected a usage error")
	}
	line := renderError(err, jsonRequested(argv))
	if !json.Valid([]byte(line)) {
		t.Fatalf("--json was on the command line but the error rendered as prose: %q", line)
	}
	if exitCodeFor(err) != exitUsage {
		t.Errorf("exit code = %d, want %d", exitCodeFor(err), exitUsage)
	}
}

func TestRenderErrorOnNilIsEmpty(t *testing.T) {
	if got := renderError(nil, false); got != "" {
		t.Errorf("renderError(nil) = %q", got)
	}
	if got := renderError(nil, true); got != "" {
		t.Errorf("renderError(nil, json) = %q", got)
	}
}
