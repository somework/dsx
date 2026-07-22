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

// syncBind binds dir to project without moving into it, for the fixtures that
// need a bound tree they are not standing in. The substitution is semantically
// neutral for planning: an absent ledger and a ledger holding only a project
// both reach planPull/planPush with Files empty and Endpoint empty, so first
// contact and both bind guards behave identically.
//
// It ensures rather than assigns. A fixture that already bound dir to the same
// project is left exactly as it is, endpoint and tracked files intact, because
// SeedState writes wholesale and a silent second call would erase what the
// fixture set on purpose. A ledger naming a DIFFERENT project is the one shape
// that fails loudly: under the old positional form that combination was a live
// guard test, and quietly overwriting it here would turn the very test that
// proves the guard into a test that cannot reach it.
func syncBind(t *testing.T, dir, project string) {
	t.Helper()
	switch st, err := syncer.LoadState(dir); {
	case err != nil:
		t.Fatalf("reading %s's ledger: %v", dir, err)
	case st.ProjectID == project:
		return
	case st.ProjectID != "":
		t.Fatalf("%s is bound to %s, not %s — a project reaches these verbs only through "+
			"the ledger now, so seed the one the test means", dir, st.ProjectID, project)
	}
	syncSeedState(t, dir, syncer.State{ProjectID: project})
}

// syncIn binds dir, moves into it, and returns the argument list a sync verb
// now takes — which is none. The chdir IS the replacement for the positional,
// not something the test does on the side: the verbs act on the tree the
// process is standing in, so a test that names a directory any other way is
// testing a path the binary does not have.
//
// t.Chdir restores on cleanup and refuses outright under t.Parallel, which is
// what keeps this from becoming an invisible dependency between tests.
func syncIn(t *testing.T, dir, project string) []string {
	t.Helper()
	syncBind(t, dir, project)
	t.Chdir(dir)
	return nil
}

func syncFirstCall(t *testing.T, f *fakeMCP, tool string) clitest.Call {
	t.Helper()
	return clitest.FirstCall(t, f, tool)
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

func maincliUnbound(string) (string, string, error) { return "", "", nil }

// TestSyncTargetRefusesAnyPositional replaces the pair that asserted first two
// positionals and then one. A sync verb takes none: inside a tree the ledger is
// found by walking up, outside one `dsx -C <dir>` is the way in, and the
// refusal has to name that replacement or it strands whoever typed the old
// form.
func TestSyncTargetRefusesAnyPositional(t *testing.T) {
	dir := t.TempDir()
	_, _, err := resolveSyncTarget("pull", []string{dir}, maincliUnbound)
	if got := maincliKind(t, err); got != dsxerr.KindUsage {
		t.Fatalf("a positional classified %q, want %q", got, dsxerr.KindUsage)
	}
	msg := err.Error()
	if !strings.Contains(msg, "takes no arguments") {
		t.Errorf("the refusal does not say the verb takes none: %q", msg)
	}
	if !strings.Contains(msg, "dsx -C "+dir+" pull") {
		t.Errorf("the refusal does not name the replacement: %q", msg)
	}
}

// TestAPathPositionalIsAnsweredWithDashCNotWithCreation: the missing-directory
// check left resolveSyncTarget with the positional, so a path that is not there
// is no longer a special case — it is simply an argument a verb does not take.
// What must not come back is the old advice, which named a form that created
// the directory.
func TestAPathPositionalIsAnsweredWithDashCNotWithCreation(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	_, _, err := resolveSyncTarget("pull", []string{missing}, func(string) (string, string, error) {
		t.Fatal("the ledger was read although the invocation could not parse")
		return "", "", nil
	})
	if got := maincliKind(t, err); got != dsxerr.KindUsage {
		t.Fatalf("kind=%q, want %q", got, dsxerr.KindUsage)
	}
	if !strings.Contains(err.Error(), "dsx -C "+missing) {
		t.Errorf("refusal does not name -C: %q", err)
	}
	if strings.Contains(err.Error(), "to create it") {
		t.Errorf("refusal still advises creating the directory: %q", err)
	}
}

func TestSyncTargetWithNoArgumentsDefaultsToTheWorkingDirectory(t *testing.T) {
	var asked string
	project, dir, err := resolveSyncTarget("status", nil, func(d string) (string, string, error) {
		asked = d
		return "from-ledger", d, nil
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
	t.Chdir(t.TempDir())
	_, _, err := resolveSyncTarget("pull", nil, maincliUnbound)
	if got := maincliKind(t, err); got != dsxerr.KindUsage {
		t.Fatalf("unbound dir classified %q, want %q: retrying the same command cannot help", got, dsxerr.KindUsage)
	}

	// The two repairs, and nothing that no longer parses: naming the verb the
	// caller typed used to be the whole advice, and is now the one thing that
	// cannot work.
	msg := err.Error()
	for _, want := range []string{"ledger", "dsx pin <project>", "dsx clone <project>"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not tell the user how to recover (missing %q): %q", want, msg)
		}
	}
	if strings.Contains(msg, "dsx pull <project>") {
		t.Errorf("the refusal advises a form that no longer parses: %q", msg)
	}
}

func TestSyncTargetRefusesMoreThanOnePositionalArgument(t *testing.T) {
	_, _, err := resolveSyncTarget("pull", []string{"a", "b", "c"}, maincliUnbound)
	if got := maincliKind(t, err); got != dsxerr.KindUsage {
		t.Fatalf("three arguments classified %q, want %q", got, dsxerr.KindUsage)
	}
}

func TestSyncTargetPropagatesALedgerReadFailureInsteadOfCallingItUnbound(t *testing.T) {
	boom := errors.New(".dsx-state.json is corrupt: unexpected end of JSON input")
	t.Chdir(t.TempDir())
	_, _, err := resolveSyncTarget("pull", nil, func(string) (string, string, error) {
		return "", "", boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("a ledger read failure was swallowed: got %v, want it to carry %v", err, boom)
	}
	if strings.Contains(err.Error(), "carries no dsx ledger") {
		t.Error("a broken ledger was reported as an absent one")
	}
}

func TestErrorsRaisedBeforeFlagParsingStillHonourJSON(t *testing.T) {
	argv := []string{"pull", "--json", "a", "b", "c"}
	_, _, err := resolveSyncTarget("pull", argv[2:], maincliUnbound)
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

func TestBoundProjectReadsTheLedgerAndIsSilentWhenThereIsNone(t *testing.T) {
	// Under a directory nothing above is bound to: t.TempDir() sits under the
	// system temp root, which no test binds, so the upward walk genuinely comes
	// back empty rather than finding a stray ledger.
	dir := t.TempDir()
	got, root, err := boundProject(dir)
	if err != nil {
		t.Fatalf("a directory without a ledger is not an error: %v", err)
	}
	if got != "" || root != "" {
		t.Errorf("boundProject on a fresh dir = (%q, %q), want two empty strings", got, root)
	}

	syncSeedState(t, dir, syncer.State{ProjectID: "proj-uuid"})
	got, root, err = boundProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "proj-uuid" {
		t.Errorf("boundProject = %q, want %q", got, "proj-uuid")
	}
	if root != dir {
		t.Errorf("root = %q, want the caller's own spelling %q", root, dir)
	}
}

func TestBoundProjectSurfacesACorruptLedgerRatherThanReportingUnbound(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(syncer.StateDir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(syncer.StatePath(dir), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := boundProject(dir); err == nil {
		t.Fatal("a corrupt ledger read as an unbound directory")
	}
}

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
		return cmdPull(context.Background(), c, syncIn(t, dir, "proj-uuid"))
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

	if !strings.Contains(out, "conflicts 1") {
		t.Errorf("summary did not mention the conflict: %q", out)
	}

	if b, readErr := os.ReadFile(filepath.Join(dir, "a.css")); readErr != nil || string(b) != "LOCAL EDIT" {
		t.Fatalf("the local edit was overwritten: %q, %v", b, readErr)
	}
}

// maincliUnverifiedCollision is maincliConflictedPull's untracked twin: the
// local file was never named by a ledger entry, so the collision with the
// server's copy is unverified (case c), not a proven divergence.
func maincliUnverifiedCollision(t *testing.T) (*fakeMCP, *mcp.Client, string) {
	t.Helper()
	dir := t.TempDir()

	maincliWriteFile(t, dir, "a.css", "LOCAL, NEVER COMPARED")

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("a.css", "e1", 11))}
		case "read_file":
			return fakeReply{Text: envelopeFor("a.css", "e1", "SERVER COPY")}
		}
		return fakeReply{Text: "{}", IsError: true}
	})
	return f, fakeClient(f), dir
}

// TestExitCodeIsUnchangedForAnUnverifiedConflict is the cmd-layer safety
// guard for the Unverified split: PullReport/PushReport fold Unverified back
// into Conflicts (pull.go, push.go), so an untracked collision must still
// exit ExitConflict through cmdSync exactly as a tracked one does — softening
// the wording must not soften the exit code.
func TestExitCodeIsUnchangedForAnUnverifiedConflict(t *testing.T) {
	t.Run("pull", func(t *testing.T) {
		_, c, dir := maincliUnverifiedCollision(t)

		out, err := captureStdout(t, func() error {
			return cmdPull(context.Background(), c, syncIn(t, dir, "proj-uuid"))
		})
		if err == nil {
			t.Fatalf("an unverified collision reported success; output was %q", out)
		}
		if got := dsxerr.ExitCodeFor(err); got != dsxerr.ExitConflict {
			t.Fatalf("exit code = %d, want %d — a caller reading 0 would carry on over bytes nobody compared", got, dsxerr.ExitConflict)
		}
		if paths := dsxerr.Classify(err).Paths; len(paths) != 1 || paths[0] != "a.css" {
			t.Errorf("conflict paths = %v, want [a.css]", paths)
		}
		if !strings.Contains(out, "conflicts 1") {
			t.Errorf("summary did not count the unverified path as a conflict: %q", out)
		}
		if b, readErr := os.ReadFile(filepath.Join(dir, "a.css")); readErr != nil || string(b) != "LOCAL, NEVER COMPARED" {
			t.Fatalf("the local file was overwritten: %q, %v", b, readErr)
		}
	})

	t.Run("push", func(t *testing.T) {
		_, c, dir := maincliUnverifiedCollision(t)

		out, err := captureStdout(t, func() error {
			return cmdPush(context.Background(), c, syncIn(t, dir, "proj-uuid"))
		})
		if err == nil {
			t.Fatalf("an unverified collision reported success; output was %q", out)
		}
		if got := dsxerr.ExitCodeFor(err); got != dsxerr.ExitConflict {
			t.Fatalf("exit code = %d, want %d — a caller reading 0 would carry on over bytes nobody compared", got, dsxerr.ExitConflict)
		}
		if paths := dsxerr.Classify(err).Paths; len(paths) != 1 || paths[0] != "a.css" {
			t.Errorf("conflict paths = %v, want [a.css]", paths)
		}
		if !strings.Contains(out, "conflicts 1") {
			t.Errorf("summary did not count the unverified path as a conflict: %q", out)
		}
	})
}

// maincliSeedBaseline writes .dsx/baseline.json directly, the way
// clitest.SeedState seeds the ledger: syncer.Baseline.save is unexported, so
// a caller outside the package builds the same bytes by hand.
func maincliSeedBaseline(t *testing.T, dir string, bl syncer.Baseline) {
	t.Helper()
	if bl.Verified == nil {
		bl.Verified = map[string]syncer.BaselineEntry{}
	}
	b, err := json.Marshal(bl)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(syncer.StateDir(dir), 0o755); err != nil {
		t.Fatalf("seeding baseline: %v", err)
	}
	if err := os.WriteFile(syncer.BaselinePath(dir), b, 0o644); err != nil {
		t.Fatalf("seeding baseline: %v", err)
	}
}

// maincliStaleProofCollision is maincliUnverifiedCollision's stale-baseline
// twin: a baseline once proved these exact bytes against the server, but the
// listing etag has since moved on (the measured fact: every write rotates
// the etag, content-identical or not) while the local file has not changed.
// The collision belongs to StaleProof, not Unverified.
func maincliStaleProofCollision(t *testing.T) (*fakeMCP, *mcp.Client, string) {
	t.Helper()
	dir := t.TempDir()
	const body = "PROVEN AGAINST AN OLDER REVISION"

	maincliWriteFile(t, dir, "a.css", body)

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("a.css", "e2", int64(len(body))))}
		case "read_file":
			return fakeReply{Text: envelopeFor("a.css", "e2", body)}
		}
		return fakeReply{Text: "{}", IsError: true}
	})
	c := fakeClient(f)

	maincliSeedBaseline(t, dir, syncer.Baseline{
		ProjectID: "proj-uuid",
		Endpoint:  c.Endpoint(),
		Verified: map[string]syncer.BaselineEntry{
			"a.css": {Etag: "e1", Size: int64(len(body)), SHA: syncer.SHA256Hex([]byte(body))},
		},
	})

	return f, c, dir
}

// TestExitCodeIsUnchangedForAStaleProofConflict is StaleProof's cmd-layer
// safety guard, the sibling of TestExitCodeIsUnchangedForAnUnverifiedConflict:
// PullReport/PushReport fold StaleProof back into Conflicts too, so this
// collision must still exit ExitConflict exactly the same way. The wording a
// real user sees on stdout must name the earlier-revision proof, never "never
// verified" — this is the cmd-layer proof that the new wording actually
// reaches a real command invocation through the fake endpoint, not just a
// report struct built by hand.
func TestExitCodeIsUnchangedForAStaleProofConflict(t *testing.T) {
	t.Run("pull", func(t *testing.T) {
		_, c, dir := maincliStaleProofCollision(t)

		out, err := captureStdout(t, func() error {
			return cmdPull(context.Background(), c, syncIn(t, dir, "proj-uuid"))
		})
		if err == nil {
			t.Fatalf("a stale-proof collision reported success; output was %q", out)
		}
		if got := dsxerr.ExitCodeFor(err); got != dsxerr.ExitConflict {
			t.Fatalf("exit code = %d, want %d — a caller reading 0 would carry on over bytes nobody re-checked", got, dsxerr.ExitConflict)
		}
		if paths := dsxerr.Classify(err).Paths; len(paths) != 1 || paths[0] != "a.css" {
			t.Errorf("conflict paths = %v, want [a.css]", paths)
		}
		if !strings.Contains(out, "conflicts 1") {
			t.Errorf("summary did not count the stale-proof path as a conflict: %q", out)
		}
		if strings.Contains(out, "never verified") {
			t.Errorf("stdout claims dsx never checked, but a baseline proved these bytes once: %q", out)
		}
		if !strings.Contains(out, "verified, but against an earlier revision") {
			t.Errorf("stdout does not say what actually happened: %q", out)
		}
		if b, readErr := os.ReadFile(filepath.Join(dir, "a.css")); readErr != nil || string(b) != "PROVEN AGAINST AN OLDER REVISION" {
			t.Fatalf("the local file was overwritten: %q, %v", b, readErr)
		}
	})

	t.Run("push", func(t *testing.T) {
		_, c, dir := maincliStaleProofCollision(t)

		out, err := captureStdout(t, func() error {
			return cmdPush(context.Background(), c, syncIn(t, dir, "proj-uuid"))
		})
		if err == nil {
			t.Fatalf("a stale-proof collision reported success; output was %q", out)
		}
		if got := dsxerr.ExitCodeFor(err); got != dsxerr.ExitConflict {
			t.Fatalf("exit code = %d, want %d — a caller reading 0 would carry on over bytes nobody re-checked", got, dsxerr.ExitConflict)
		}
		if paths := dsxerr.Classify(err).Paths; len(paths) != 1 || paths[0] != "a.css" {
			t.Errorf("conflict paths = %v, want [a.css]", paths)
		}
		if !strings.Contains(out, "conflicts 1") {
			t.Errorf("summary did not count the stale-proof path as a conflict: %q", out)
		}
		if strings.Contains(out, "never verified") {
			t.Errorf("stdout claims dsx never checked, but a baseline proved these bytes once: %q", out)
		}
		if !strings.Contains(out, "verified, but against an earlier revision") {
			t.Errorf("stdout does not say what actually happened: %q", out)
		}
	})
}

func TestDryRunPullReportsTheSameConflictAndStillExitsZero(t *testing.T) {
	_, c, dir := maincliConflictedPull(t)

	out, err := captureStdout(t, func() error {
		return cmdPull(context.Background(), c, append(syncIn(t, dir, "proj-uuid"), "-n"))
	})
	if err != nil {
		t.Fatalf("a dry run reporting a conflict failed with %v", err)
	}
	if !strings.Contains(out, "conflicts 1") {
		t.Errorf("the dry run did not report the conflict it found: %q", out)
	}
}

func TestSyncResolvesTheProjectFromTheLedgerOfTheTreeItStandsIn(t *testing.T) {
	f, c, dir := maincliConflictedPull(t)
	t.Chdir(dir)

	// pull -n, not status: status resolves the same way but never reaches
	// list_files, so it cannot witness which project id went out.
	_, err := captureStdout(t, func() error {
		return cmdPull(context.Background(), c, []string{"-n"})
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

	err := cmdPull(context.Background(), c, []string{dir})
	if got := maincliKind(t, err); got != dsxerr.KindUsage {
		t.Fatalf("kind = %q, want %q", got, dsxerr.KindUsage)
	}
	if len(f.Recorded()) != 0 {
		t.Errorf("the endpoint was contacted for a directory with no known project: %v", f.Recorded())
	}

	if syncLedgerExists(t, dir) {
		t.Error("a failed resolve left a ledger behind")
	}
}

func TestSyncQuietPrintsNothingButStillReportsTheConflict(t *testing.T) {
	_, c, dir := maincliConflictedPull(t)

	out, err := captureStdout(t, func() error {
		return cmdPull(context.Background(), c, append(syncIn(t, dir, "proj-uuid"), "-q"))
	})
	if out != "" {
		t.Errorf("-q printed %q", out)
	}
	if got := dsxerr.ExitCodeFor(err); got != dsxerr.ExitConflict {
		t.Errorf("exit code under -q = %d, want %d", got, dsxerr.ExitConflict)
	}
}

func TestSyncClampsConcurrencyBelowOneToOneInsteadOfHanging(t *testing.T) {
	_, c, dir := maincliConflictedPull(t)
	_, err := captureStdout(t, func() error {
		return cmdPull(context.Background(), c, append(syncIn(t, dir, "proj-uuid"), "-n", "-j", "0"))
	})
	if err != nil {
		t.Fatalf("-j 0: %v", err)
	}
}

func TestSyncRejectsAnUnknownFlagAsUsage(t *testing.T) {
	f, c := maincliFake(t, "unreachable")
	err := cmdPull(context.Background(), c, []string{"proj", ".", "--bogus"})
	if got := maincliKind(t, err); got != dsxerr.KindUsage {
		t.Errorf("kind = %q, want %q", got, dsxerr.KindUsage)
	}
	if len(f.Recorded()) != 0 {
		t.Errorf("the endpoint was contacted despite a bad flag: %v", f.Recorded())
	}
}

func TestPushThatFailsMidBatchStillPrintsThePartialReportBeforeTheError(t *testing.T) {
	dir := t.TempDir()
	const project = "proj-uuid"

	maincliWriteFile(t, dir, "keep.txt", "same")
	maincliWriteFile(t, dir, "new.txt", "fresh")

	syncSeedState(t, dir, syncer.State{
		ProjectID: project,
		Files: map[string]syncer.FileState{
			"keep.txt": {Etag: "e1", Size: int64(len("same")), SHA: syncer.SHA256Hex([]byte("same"))},
		},
	})

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(fileEntry("keep.txt", "e1", int64(len("same"))))}
		case "write_files":
			// Malformed on purpose: writeBatch cannot unmarshal it, so Push
			// returns an error after the plan (Unchanged etc.) is already set.
			return fakeReply{Text: "not json"}
		}
		return fakeReply{Text: "{}", IsError: true}
	})
	c := fakeClient(f)

	out, err := captureStdout(t, func() error {
		return cmdPush(context.Background(), c, syncIn(t, dir, project))
	})
	if err == nil {
		t.Fatalf("push with a malformed write_files reply reported success; output was %q", out)
	}
	if !strings.Contains(out, "unchanged 1") {
		t.Errorf("the partial report was not printed before the error returned: %q", out)
	}
}

func TestPullThatFailsMidFetchStillPrintsThePartialReportBeforeTheError(t *testing.T) {
	dir := t.TempDir()
	const project = "proj-uuid"

	maincliWriteFile(t, dir, "keep.txt", "same")
	syncSeedState(t, dir, syncer.State{
		ProjectID: project,
		Files: map[string]syncer.FileState{
			"keep.txt": {Etag: "e1", Size: int64(len("same")), SHA: syncer.SHA256Hex([]byte("same"))},
		},
	})

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(
				fileEntry("keep.txt", "e1", int64(len("same"))),
				// bad.txt's advertised size disagrees with the body read_file
				// actually serves below, tripping the decoded-length guard.
				fileEntry("bad.txt", "e2", 999),
			)}
		case "read_file":
			return fakeReply{Text: envelopeFor("bad.txt", "e2", "short")}
		}
		return fakeReply{Text: "{}", IsError: true}
	})
	c := fakeClient(f)

	out, err := captureStdout(t, func() error {
		return cmdPull(context.Background(), c, syncIn(t, dir, project))
	})
	if err == nil {
		t.Fatalf("pull with a size mismatch reported success; output was %q", out)
	}
	if !strings.Contains(out, "unchanged 1") {
		t.Errorf("the partial report was not printed before the error returned: %q", out)
	}
}

func TestNeitherPullNorPushCreatesTheTargetDirectory(t *testing.T) {
	_, c := maincliFake(t, "unreachable")
	base := t.TempDir()

	// pull used to create its target, which was reachable only by naming the
	// project as a second positional. With that gone, a directory pull could
	// create is a directory pull cannot resolve a project for, so both verbs
	// now refuse alike and clone is the only way to start one. What push's
	// half protected still holds, and now holds for pull too: an empty local
	// scan never reaches a plan, so --prune can never read the whole server
	// tree as user deletions.
	for _, tc := range []struct {
		mode string
		run  func(context.Context, *mcp.Client, []string) error
	}{{"pull", cmdPull}, {"push", cmdPush}} {
		mode := tc.mode
		t.Run(mode, func(t *testing.T) {
			never := filepath.Join(base, "not-made-by-"+mode)
			err := tc.run(context.Background(), c, []string{never})
			if err == nil {
				t.Fatalf("%s accepted a directory that does not exist", mode)
			}
			if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
				t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
			}
			if _, sErr := os.Stat(never); sErr == nil {
				t.Errorf("%s created the directory it refused", mode)
			}
		})
	}
}
