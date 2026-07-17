package synccmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/clitest"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/mcp"
	"github.com/somework/dsx/internal/syncer"
)

// These tests drive cmdSync/resolveSyncTarget/boundProject directly, so they
// must be internal (package synccmd) tests — the wrappers are unexported on
// purpose. The fake endpoint lives in internal/clitest, shared by every command
// package; these aliases keep the moved tests spelled as they were in
// internal/cli. See internal/cli/fake_test.go for why the adapter lives in
// internal/clitest rather than being reimplemented.
type (
	fakeMCP   = clitest.Server
	fakeReply = clitest.Reply
)

var (
	newFakeMCP    = clitest.New
	fakeClient    = clitest.Client
	captureStdout = clitest.CaptureStdout
	listingFor    = clitest.ListingFor
	fileEntry     = clitest.FileEntry
	envelopeFor   = clitest.EnvelopeFor
	syncSeedState = clitest.SeedState

	syncLedgerExists = clitest.LedgerExists
)

func syncFirstCall(t *testing.T, f *fakeMCP, tool string) clitest.Call {
	t.Helper()
	return clitest.FirstCall(t, f, tool)
}

// maincliFake wires a client to a fake endpoint answering every tool with text.
//
// maincliFake and maincliKind are duplicated from internal/cli on purpose: they
// are generic helpers still used by cli's own tests, and a shared home would
// have to be imported by both — but syncer/clitest cannot depend on cli. Per the
// fake_test.go precedent, each package keeps the pieces it needs.
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
	if got := maincliKind(t, err); got != dsxerr.KindUsage {
		t.Fatalf("unbound dir classified %q, want %q: retrying the same command cannot help", got, dsxerr.KindUsage)
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
	if got := maincliKind(t, err); got != dsxerr.KindUsage {
		t.Fatalf("three arguments classified %q, want %q", got, dsxerr.KindUsage)
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

// The renderer runs outside every FlagSet so that a failure raised before flags
// were parsed still honours --json. This pins the pair main() actually calls.
// resolveSyncTarget is only the vehicle: the contract — an error raised before
// any FlagSet still renders as JSON when --json is on the line — is shared by
// every command.
func TestErrorsRaisedBeforeFlagParsingStillHonourJSON(t *testing.T) {
	argv := []string{"pull", "--json", "a", "b", "c"}
	_, _, err := resolveSyncTarget("pull", argv[2:], maincliNoLedger(t))
	if err == nil {
		t.Fatal("expected a usage error")
	}
	line := dsxerr.Render(err, dsxerr.JSONRequested(argv))
	if !json.Valid([]byte(line)) {
		t.Fatalf("--json was on the command line but the error rendered as prose: %q", line)
	}
	if dsxerr.ExitCodeFor(err) != dsxerr.ExitUsage {
		t.Errorf("exit code = %d, want %d", dsxerr.ExitCodeFor(err), dsxerr.ExitUsage)
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

	syncSeedState(t, dir, syncer.State{ProjectID: "proj-uuid"})
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
	if err := os.WriteFile(filepath.Join(dir, syncer.StateFileName), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := boundProject(dir); err == nil {
		t.Fatal("a corrupt ledger read as an unbound directory")
	}
}

// ---------------------------------------------------------------------------
// cmdSync — syncer.ConflictOutcome wired end to end
// ---------------------------------------------------------------------------

// maincliConflictedPull sets up a directory where a pull must refuse: the file
// on disk was edited locally *and* the server moved on. Per invariant 2 the
// refusal keys off the bytes, not the etag — an etag test alone cannot see this
// case.
func maincliConflictedPull(t *testing.T) (*fakeMCP, *mcp.Client, string) {
	t.Helper()
	dir := t.TempDir()
	const project = "proj-uuid"

	maincliWriteFile(t, dir, "a.css", "LOCAL EDIT")
	syncSeedState(t, dir, syncer.State{
		ProjectID: project,
		Files: map[string]syncer.FileState{
			"a.css": {Etag: "e1", Size: 3, SHA: syncer.SHA256Hex([]byte("old"))},
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
	return f, fakeClient(f), dir
}

func TestPullThatRefusedToMoveBytesExitsThreeNotZero(t *testing.T) {
	_, c, dir := maincliConflictedPull(t)

	out, err := captureStdout(t, func() error {
		return cmdSync(context.Background(), c, "pull", []string{"proj-uuid", dir})
	})
	if err == nil {
		t.Fatalf("a pull that refused every file reported success; output was %q", out)
	}
	if got := dsxerr.ExitCodeFor(err); got != dsxerr.ExitConflict {
		t.Fatalf("exit code = %d, want %d — a caller reading 0 would carry on over the local edit", got, dsxerr.ExitConflict)
	}
	if paths := dsxerr.Classify(err).Paths; len(paths) != 1 || paths[0] != "a.css" {
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
	if got := maincliKind(t, err); got != dsxerr.KindUsage {
		t.Fatalf("kind = %q, want %q", got, dsxerr.KindUsage)
	}
	if len(f.Recorded()) != 0 {
		t.Errorf("the endpoint was contacted for a directory with no known project: %v", f.Recorded())
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
	if n := f.CountTool("read_file"); n != 0 {
		t.Errorf("status fetched %d file(s); it transfers nothing", n)
	}
	if n := f.CountTool("write_files"); n != 0 {
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
		Pull *syncer.PullReport `json:"pull"`
		Push *syncer.PushReport `json:"push"`
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
	if got := dsxerr.ExitCodeFor(err); got != dsxerr.ExitConflict {
		t.Errorf("exit code under -q = %d, want %d", got, dsxerr.ExitConflict)
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
	if got := maincliKind(t, err); got != dsxerr.KindUsage {
		t.Errorf("kind = %q, want %q", got, dsxerr.KindUsage)
	}
	if len(f.Recorded()) != 0 {
		t.Errorf("the endpoint was contacted despite a bad flag: %v", f.Recorded())
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
