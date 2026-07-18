package cli

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/auth"
	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/mcp"
	"github.com/somework/dsx/internal/syncer"
)

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

func maincliFake(t *testing.T, text string) (*fakeMCP, *mcp.Client) {
	t.Helper()
	f := newFakeMCP(t, func(string, map[string]any) fakeReply {
		return fakeReply{Text: text}
	})
	return f, fakeClient(f)
}

func maincliKind(t *testing.T, err error) dsxerr.Kind {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	return dsxerr.Classify(err).Kind
}

func TestConflictsOnARealRunAreExitThreeCarryingTheSortedPaths(t *testing.T) {
	err := syncer.ConflictOutcome([]string{"z.css", "a.css", "m.css"}, false, "local differs; --force to overwrite")
	if err == nil {
		t.Fatal("a real run that refused to move bytes reported success; a caller reading exit 0 would carry on over work that exists nowhere else")
	}
	de := dsxerr.Classify(err)
	if de.Kind != dsxerr.KindConflict {
		t.Errorf("kind = %q, want %q", de.Kind, dsxerr.KindConflict)
	}
	if got := dsxerr.ExitCodeFor(err); got != dsxerr.ExitConflict {
		t.Errorf("exit code = %d, want %d", got, dsxerr.ExitConflict)
	}

	want := []string{"a.css", "m.css", "z.css"}
	if !reflect.DeepEqual(de.Paths, want) {
		t.Errorf("paths = %v, want %v (present and sorted)", de.Paths, want)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("the hint was dropped: %q", err.Error())
	}
}

func TestConflictsOnADryRunAreNotAFailure(t *testing.T) {
	if err := syncer.ConflictOutcome([]string{"a.css"}, true, "hint"); err != nil {
		t.Fatalf("a dry run reporting a conflict failed with %v; it did exactly what it was told", err)
	}
}

func TestNoConflictsIsSuccessInEitherMode(t *testing.T) {
	for _, dry := range []bool{false, true} {
		if err := syncer.ConflictOutcome(nil, dry, "hint"); err != nil {
			t.Errorf("syncer.ConflictOutcome(nil, %v) = %v, want nil", dry, err)
		}
		if err := syncer.ConflictOutcome([]string{}, dry, "hint"); err != nil {
			t.Errorf("syncer.ConflictOutcome([], %v) = %v, want nil", dry, err)
		}
	}
}

func TestJSONSafePassesValidJSONThroughByteIdentical(t *testing.T) {
	for _, doc := range []string{
		`{"files":[{"path":"a.css","size":12}]}`,
		`[]`,
		`{}`,
		`null`,
		`123`,
		`"a bare string is a JSON document"`,
		`{"nested":{"deep":[1,2,{"x":null}]}}`,
	} {
		if got := cmd.JSONSafe(doc, true); got != doc {
			t.Errorf("jsonSafe(%q) = %q, want it byte-identical", doc, got)
		}
	}
}

func TestJSONSafeWrapsProseSoStdoutStaysParseable(t *testing.T) {
	const prose = "Deleted 3 files from the project."
	got := cmd.JSONSafe(prose, true)

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

func TestJSONSafeWrapsAStringThatOnlyLooksLikeJSON(t *testing.T) {
	cases := []string{
		`{"almost": true`,
		`[1, 2,`,
		`{'single': 'quotes'}`,
		`{"a":1}{"b":2}`,
		`{"trailing": "comma",}`,
		"{\n  broken\n}",
	}
	for _, s := range cases {
		got := cmd.JSONSafe(s, true)
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
	if got := cmd.JSONSafe(prose, false); got != prose {
		t.Errorf("jsonSafe(%q, false) = %q; the human lane must not be wrapped", prose, got)
	}

	const doc = `{"a":1}`
	if got := cmd.JSONSafe(doc, false); got != doc {
		t.Errorf("jsonSafe(%q, false) = %q", doc, got)
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

func TestNeed1AndNeed2ClassifyTooFewArgumentsAsUsage(t *testing.T) {
	if _, _, err := cmd.Need1(nil, "project <id>"); maincliKind(t, err) != dsxerr.KindUsage {
		t.Errorf("need1(nil) classified %q, want %q", maincliKind(t, err), dsxerr.KindUsage)
	}
	if _, _, err := cmd.Need1([]string{}, "project <id>"); err == nil {
		t.Error("need1 accepted zero arguments")
	}
	for _, args := range [][]string{nil, {"only-one"}} {
		_, _, _, err := cmd.Need2(args, "cp <src> <dst>")
		if maincliKind(t, err) != dsxerr.KindUsage {
			t.Errorf("need2(%v) classified %q, want %q", args, maincliKind(t, err), dsxerr.KindUsage)
		}
	}
	if !strings.Contains(func() string { _, _, err := cmd.Need1(nil, "project <id>"); return err.Error() }(), "project <id>") {
		t.Error("the usage error must echo the form the caller should have typed")
	}
}

func TestNeed1AndNeed2ReturnTheRestForTheNextParser(t *testing.T) {
	first, rest, err := cmd.Need1([]string{"a", "b", "c"}, "f")
	if err != nil || first != "a" || !reflect.DeepEqual(rest, []string{"b", "c"}) {
		t.Fatalf("need1 = (%q, %v, %v)", first, rest, err)
	}
	a, b, rest2, err := cmd.Need2([]string{"a", "b", "c"}, "f")
	if err != nil || a != "a" || b != "b" || !reflect.DeepEqual(rest2, []string{"c"}) {
		t.Fatalf("need2 = (%q, %q, %v, %v)", a, b, rest2, err)
	}
}

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
			fs := cmd.NewFlagSet("tree")
			asJSON := cmd.JSONFlag(fs)
			pos, err := cmd.ParseArgs(fs, tc.args)
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
	fs := cmd.NewFlagSet("cp")
	_ = cmd.JSONFlag(fs)
	pos, err := cmd.ParseArgs(fs, []string{"src.css", "--json", "dst.css"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pos, []string{"src.css", "dst.css"}) {
		t.Fatalf("positionals = %v, want them in the order they were typed", pos)
	}
}

func TestParseArgsTreatsAnUnknownFlagAsUsageNotAsAPositional(t *testing.T) {
	fs := cmd.NewFlagSet("tree")
	_ = cmd.JSONFlag(fs)
	pos, err := cmd.ParseArgs(fs, []string{"proj", "--bogus"})
	if err == nil {
		t.Fatalf("an unknown flag was accepted, positionals = %v", pos)
	}
	if got := maincliKind(t, err); got != dsxerr.KindUsage {
		t.Errorf("unknown flag classified %q, want %q", got, dsxerr.KindUsage)
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("the message does not name the offending flag: %q", err.Error())
	}
}

func TestNewFlagSetKeepsFlagsOwnChatterOffStderr(t *testing.T) {
	var err error
	leaked := maincliCaptureStderr(t, func() {
		fs := cmd.NewFlagSet("tree")
		_ = cmd.JSONFlag(fs)
		_, err = cmd.ParseArgs(fs, []string{"--bogus"})
	})
	if err == nil {
		t.Fatal("expected a usage error")
	}
	if leaked != "" {
		t.Errorf("flag wrote to stderr despite SetOutput(io.Discard): %q", leaked)
	}

	control := maincliCaptureStderr(t, func() {
		raw := flag.NewFlagSet("raw", flag.ContinueOnError)
		_ = raw.Bool("json", false, "")
		_, _ = cmd.ParseArgs(raw, []string{"--bogus"})
	})
	if control == "" {
		t.Skip("flag no longer writes to stderr by default; the discard assertion above has lost its teeth")
	}
}

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
			got := cmd.SplitList(tc.in)
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
	got := cmd.SplitList(" my file.css ,b.css")
	if len(got) != 2 || got[0] != "my file.css" {
		t.Fatalf("splitList = %v, want the inner space preserved", got)
	}
}

func TestEmitPrintsTheToolsTextVerbatimInProseMode(t *testing.T) {
	_, c := maincliFake(t, "Deleted 3 files.")
	out, err := captureStdout(t, func() error {
		return cmd.Emit(context.Background(), c, "delete_files", map[string]any{"x": 1}, false)
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "Deleted 3 files.\n" {
		t.Errorf("stdout = %q, want the tool's text verbatim", out)
	}
}

func TestEmitUnderJSONAlwaysPrintsExactlyOneJSONDocument(t *testing.T) {
	for _, text := range []string{
		`{"projects":[]}`,
		"Deleted 3 files.",
		`{"truncated": `,
	} {
		out, err := captureStdout(t, func() error {
			_, c := maincliFake(t, text)
			return cmd.Emit(context.Background(), c, "list_projects", map[string]any{}, true)
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
	f := newFakeMCP(t, func(string, map[string]any) fakeReply {
		return fakeReply{Text: "project not found", IsError: true}
	})
	c := fakeClient(f)
	out, err := captureStdout(t, func() error {
		return cmd.Emit(context.Background(), c, "get_project", map[string]any{"project_id": "nope"}, true)
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
		return cmd.EmitFlagged(context.Background(), c, "project", []string{"proj-uuid", "--json"},
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
	f, c := maincliFake(t, "unreachable")
	err := cmd.EmitFlagged(context.Background(), c, "project", nil, func(pos []string) (string, map[string]any, error) {
		_, _, err := cmd.Need1(pos, "project <id>")
		return "", nil, err
	})

	if got := maincliKind(t, err); got != dsxerr.KindUsage {
		t.Errorf("kind = %q, want %q", got, dsxerr.KindUsage)
	}
	if n := f.CountTool("get_project"); n != 0 {
		t.Errorf("%d tool calls made despite a usage error", n)
	}
}

func TestEmitFlaggedRejectsAnUnknownFlagBeforeCallingTheTool(t *testing.T) {
	f, c := maincliFake(t, "unreachable")
	err := cmd.EmitFlagged(context.Background(), c, "projects", []string{"--bogus"}, func([]string) (string, map[string]any, error) {
		t.Fatal("build ran despite an unparseable flag")
		return "", nil, nil
	})

	if got := maincliKind(t, err); got != dsxerr.KindUsage {
		t.Errorf("kind = %q, want %q", got, dsxerr.KindUsage)
	}
	if len(f.Recorded()) != 0 {
		t.Errorf("the endpoint was contacted: %v", f.Recorded())
	}
}

func maincliFakeLogin(t *testing.T, token string, scopes []string, expiresAt int64) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	writeCreds(t, dir, auth.Creds{AccessToken: token, Scopes: scopes, ExpiresAt: expiresAt})
	return token
}

const maincliFarFuture = 1900000000000

func TestAuthNeverPrintsTheTokenInEitherMode(t *testing.T) {
	const secret = "sk-ant-oat01-SECRET-DO-NOT-PRINT"
	maincliFakeLogin(t, secret, []string{"user:inference", "user:profile"}, maincliFarFuture)

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
	t.Setenv("DSX_TOKEN", "")
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	out, err := captureStdout(t, func() error { return cmdAuth(nil) })
	if err == nil {
		t.Fatalf("cmdAuth with no stored login succeeded, printing %q", out)
	}
	if got := maincliKind(t, err); got != dsxerr.KindAuth {
		t.Errorf("kind = %q, want %q — a missing login must map to auth, the same as every authenticated command", got, dsxerr.KindAuth)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing when there is nothing to report", out)
	}
}

func TestAuthRejectsAnUnknownFlagWithoutReadingAnyCredential(t *testing.T) {
	err := cmdAuth([]string{"--bogus"})
	if got := maincliKind(t, err); got != dsxerr.KindUsage {
		t.Errorf("kind = %q, want %q", got, dsxerr.KindUsage)
	}
}

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
	for _, tc := range []struct {
		name  string
		setup func(*testing.T)
	}{
		{"no login available", func(t *testing.T) {
			t.Setenv("DSX_TOKEN", "")
			t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
		}},

		{"token present", func(t *testing.T) {
			t.Setenv("DSX_TOKEN", "test-token")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(t)
			_, err := maincliRun(t, "pulll")
			if got := maincliKind(t, err); got != dsxerr.KindUsage {
				t.Fatalf("unknown command classified %q, want %q: %v", got, dsxerr.KindUsage, err)
			}
			if !strings.Contains(err.Error(), "pulll") {
				t.Errorf("the message does not name the command: %q", err.Error())
			}
			if !strings.Contains(err.Error(), "dsx help") {
				t.Errorf("errors say what to do next: %q", err.Error())
			}
		})
	}
}

func TestRunCompletionAnswersForEveryShellAndRefusesOthers(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	for _, shell := range []string{"bash", "zsh", "fish"} {
		out, err := maincliRun(t, "completion", shell)
		if err != nil {
			t.Errorf("dsx completion %s: %v", shell, err)
		}
		if !strings.Contains(out, "dsx") {
			t.Errorf("dsx completion %s produced nothing usable: %q", shell, out)
		}
	}
	if _, err := maincliRun(t, "completion", "powershell"); maincliKind(t, err) != dsxerr.KindUsage {
		t.Errorf("an unsupported shell must be a usage error")
	}
	if _, err := maincliRun(t, "completion"); maincliKind(t, err) != dsxerr.KindUsage {
		t.Errorf("a missing shell argument must be a usage error")
	}
}

type wireError struct {
	Error   dsxerr.Kind `json:"error"`
	Message string      `json:"message,omitempty"`
	Paths   []string    `json:"paths,omitempty"`
}

func TestEveryKindRendersAndExitsConsistently(t *testing.T) {
	cases := []struct {
		err      error
		wantCode int
	}{
		{&dsxerr.Error{Kind: dsxerr.KindFailure, Msg: "something broke"}, dsxerr.ExitFailure},
		{dsxerr.Usage("cp <src> <dst>"), dsxerr.ExitUsage},
		{dsxerr.Conflict([]string{"b.css", "a.css"}, "local differs"), dsxerr.ExitConflict},
		{&dsxerr.Error{Kind: dsxerr.KindTransport, Msg: "http 503"}, dsxerr.ExitTransport},
		{&dsxerr.Error{Kind: dsxerr.KindAuth, Msg: "token expired"}, dsxerr.ExitAuth},
		{&dsxerr.Error{Kind: dsxerr.KindProtocol, Msg: "unparseable reply"}, dsxerr.ExitFailure},
		{&dsxerr.Error{Kind: dsxerr.KindLocal, Msg: "disk full"}, dsxerr.ExitFailure},
	}
	for _, tc := range cases {
		kind := dsxerr.Classify(tc.err).Kind
		t.Run(string(kind), func(t *testing.T) {
			if got := dsxerr.ExitCodeFor(tc.err); got != tc.wantCode {
				t.Errorf("exit code = %d, want %d", got, tc.wantCode)
			}

			prose := dsxerr.Render(tc.err, false)
			if !strings.HasPrefix(prose, "dsx: ") {
				t.Errorf("prose = %q, want the dsx: prefix", prose)
			}

			line := dsxerr.Render(tc.err, true)
			if strings.Contains(line, "\n") {
				t.Errorf("--json error spans lines: %q", line)
			}
			var got wireError
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

func TestKindsSharingAnExitCodeStayDistinctInJSON(t *testing.T) {
	seen := map[dsxerr.Kind]bool{}
	for _, k := range []dsxerr.Kind{dsxerr.KindFailure, dsxerr.KindProtocol, dsxerr.KindLocal} {
		line := dsxerr.Render(&dsxerr.Error{Kind: k, Msg: "x"}, true)
		var got wireError
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatal(err)
		}
		if seen[got.Error] {
			t.Errorf("kind %q is not distinguishable in --json", k)
		}
		seen[got.Error] = true
		if dsxerr.ExitCodeFor(&dsxerr.Error{Kind: k}) != dsxerr.ExitFailure {
			t.Errorf("kind %q no longer exits %d", k, dsxerr.ExitFailure)
		}
	}
}

func TestRenderErrorOnNilIsEmpty(t *testing.T) {
	if got := dsxerr.Render(nil, false); got != "" {
		t.Errorf("dsxerr.Render(nil) = %q", got)
	}
	if got := dsxerr.Render(nil, true); got != "" {
		t.Errorf("dsxerr.Render(nil, json) = %q", got)
	}
}
