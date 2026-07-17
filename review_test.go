package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// Defects found by the adversarial review, each reproduced by three independent
// skeptics who were paid to refute it and could not. Written red first.
//
// Each test says what the defect would have cost, because that is the part a
// later reader needs in order not to undo the fix.

// ---------------------------------------------------------------------------
// data loss
// ---------------------------------------------------------------------------

func TestPullSavesTheLedgerWhenAPruneDeleteFails(t *testing.T) {
	// Found independently by three axes. The prune loop returned bare, skipping
	// the st.save nine lines below, so a single failed os.Remove discarded the
	// etag and sha of every file the run had just written.
	//
	// The next pull then sees bytes it has no record of, calls its own download
	// a conflict, and tells the user "--force to overwrite" — for every file.
	// That is invariant 5's stated harm, exactly.
	if runtime.GOOS == "windows" {
		t.Skip("permission semantics differ")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the write bit")
	}
	dir := t.TempDir()

	// A tracked file inside a directory we will make unwritable, so its removal
	// fails; and a fresh file the pull will fetch.
	mkfile(t, dir, "locked/old.css", "old")
	syncSeedState(t, dir, syncState{
		ProjectID: "p1",
		Files: map[string]fileState{
			"locked/old.css": {Etag: "e0", Size: 3, SHA: sha256hex([]byte("old"))},
		},
	})

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			if args["path"] == nil {
				return fakeReply{Text: listingFor(fileEntry("new.css", "e1", 3))}
			}
			return fakeReply{Text: listingFor()}
		case "read_file":
			return fakeReply{Text: envelopeFor("new.css", "e1", "new")}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	locked := filepath.Join(dir, "locked")
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	_, err := runPull(t.Context(), fakeClient(f), pullOpts{
		projectID: "p1", dir: dir, concurrency: 2, prune: true,
	})
	if err == nil {
		t.Fatal("a failed prune delete was reported as success")
	}

	st, loadErr := loadState(dir)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if _, ok := st.Files["new.css"]; !ok {
		t.Fatalf("the ledger lost the file the run had already written; the next pull would "+
			"call it a conflict and demand --force. ledger: %+v", st.Files)
	}
}

func TestPushRefusesToBlindlyOverwriteAFileItCouldNeverHaveRead(t *testing.T) {
	// A path tracked as binary has no SHA (read_file will not serve those bytes,
	// so dsx never held them). planPush computed localChanged from that empty
	// SHA — always true — and then matched no if_match case at all, because the
	// entry is both tracked-as-binary and on the server. The write went out with
	// no guard and no conflict.
	//
	// The server's copy is the only copy: dsx cannot pull it back, the API has no
	// `resources` lane, and there is no encoding parameter. Overwriting it is
	// unrecoverable, which is the worst outcome in this codebase.
	remote := map[string]remoteEntry{
		"assets/hero.png": {Path: "assets/hero.png", Etag: "e1", Size: 2 << 20},
	}
	local := map[string]localFile{
		"assets/hero.png": {Path: "assets/hero.png", Size: 12, SHA: sha256hex([]byte("placeholder!"))},
	}
	st := syncState{Files: map[string]fileState{
		"assets/hero.png": {Etag: "e1", Binary: true}, // no SHA: we never held it
	}}

	d := planPush(remote, local, st, false, false)
	for _, c := range d.Write {
		if c.Path == "assets/hero.png" && c.IfMatch == "" {
			t.Fatal("push would overwrite an unreadable server file with no if_match and no conflict")
		}
	}
	// Its own class since round three: the ordinary "server moved ahead; `dsx
	// pull` first" advice is false here and `dsx pull` cannot resolve it.
	if len(d.BinaryConflicts) != 1 || d.BinaryConflicts[0] != "assets/hero.png" {
		t.Fatalf("binaryConflicts = %v, want the binary path: dsx never held those bytes, so it "+
			"cannot tell a replacement from an accident", d.BinaryConflicts)
	}

	// --force still means force: the user said so explicitly.
	forced := planPush(remote, local, st, true, false)
	if len(forced.Write) != 1 {
		t.Errorf("--force must still write: %+v", forced)
	}
	if len(forced.Conflicts) != 0 || len(forced.BinaryConflicts) != 0 {
		t.Errorf("--force must not report conflicts: %v %v", forced.Conflicts, forced.BinaryConflicts)
	}
}

func TestPushDoesNotPruneAPathThatStoppedBeingARegularFile(t *testing.T) {
	// scanLocal silently drops anything that is not a regular file — the right
	// call for a symlink, since dsx must not upload whatever it points at. But
	// the path stayed in the server listing, so planPush's prune read "absent
	// from the scan" as "the user deleted it" and deleted it from the server,
	// with a matching if_match so the server complied.
	//
	// Invariant 4: --prune deletes only what we can prove was ours and
	// unmodified. A symlink is not proof of a deletion; it is proof of nothing.
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ")
	}
	dir := t.TempDir()
	mkfile(t, dir, "keep.css", "k")
	target := filepath.Join(t.TempDir(), "shared.svg")
	if err := os.WriteFile(target, []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "logo.svg")); err != nil {
		t.Fatal(err)
	}

	ig, err := loadIgnore(dir)
	if err != nil {
		t.Fatal(err)
	}
	local, err := scanLocal(dir, ig)
	if err != nil {
		t.Fatal(err)
	}
	remote := map[string]remoteEntry{
		"keep.css": {Path: "keep.css", Etag: "e0", Size: 1},
		"logo.svg": {Path: "logo.svg", Etag: "e1", Size: 6},
	}
	st := syncState{Files: map[string]fileState{
		"keep.css": {Etag: "e0", Size: 1, SHA: sha256hex([]byte("k"))},
		"logo.svg": {Etag: "e1", Size: 6, SHA: sha256hex([]byte("<svg/>"))},
	}}

	d := planPush(remote, local, st, false, true)
	for _, p := range d.Delete {
		if p == "logo.svg" {
			t.Fatal("push --prune deleted a file from the server because it became a symlink here")
		}
	}
	for _, c := range d.Write {
		if c.Path == "logo.svg" {
			t.Fatal("a symlink's target was uploaded")
		}
	}
	// Reported in its own class, not as a conflict: round two established that
	// calling a symlink a conflict means exit 3 forever under advice no user can
	// act on. See TestIrregularPathsDoNotBlockASyncForever.
	if len(d.Irregular) != 1 || d.Irregular[0] != "logo.svg" {
		t.Errorf("irregular = %v, want logo.svg reported rather than silently skipped", d.Irregular)
	}
}

func TestPullDoesNotClobberAPathThatStoppedBeingARegularFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ")
	}
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "shared.svg")
	if err := os.WriteFile(target, []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "logo.svg")); err != nil {
		t.Fatal(err)
	}

	ig, err := loadIgnore(dir)
	if err != nil {
		t.Fatal(err)
	}
	local, err := scanLocal(dir, ig)
	if err != nil {
		t.Fatal(err)
	}
	remote := map[string]remoteEntry{"logo.svg": {Path: "logo.svg", Etag: "e2", Size: 6}}
	st := syncState{Files: map[string]fileState{"logo.svg": {Etag: "e1", Size: 6, SHA: "x"}}}

	d := planPull(remote, local, st, false, false)
	for _, p := range d.Fetch {
		if p == "logo.svg" {
			t.Fatal("pull would write through a symlink the user put there deliberately")
		}
	}
	if len(d.Irregular) != 1 || d.Irregular[0] != "logo.svg" {
		t.Errorf("irregular = %v, want logo.svg named", d.Irregular)
	}
}

func TestPullTellsTheTruthAboutWhatForceWillDoToAPrunedConflict(t *testing.T) {
	// Both kinds of conflict printed "--force to overwrite". For a file deleted
	// on the server and edited here, --force does not overwrite: it DELETES, and
	// the bytes it destroys exist nowhere else. The user reaches for --force to
	// resolve some other file and loses this one.
	// Conflicts carries EVERY path a human must look at; PruneConflicts is the
	// subset --force would DELETE rather than overwrite. runPull builds it that
	// way, because narrowing `conflicts` made `status --json` report zero for
	// exactly the destructive case.
	rep := pullReport{
		Conflicts:      []string{"hero.css", "scratch.css"},
		PruneConflicts: []string{"scratch.css"},
	}
	out := rep.render(false)
	if !strings.Contains(out, "scratch.css") || !strings.Contains(out, "hero.css") {
		t.Fatalf("both conflicts must be named: %q", out)
	}
	// The line about the file --force would delete must not promise an overwrite.
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "scratch.css") {
			continue
		}
		if !strings.Contains(strings.ToUpper(line), "DELETE") {
			t.Errorf("the line for a file --force would DELETE says: %q", line)
		}
		if strings.Contains(line, "overwrite") {
			t.Errorf("the line promises an overwrite but --force would delete: %q", line)
		}
	}
}

// ---------------------------------------------------------------------------
// security
// ---------------------------------------------------------------------------

func TestRemotePathGuardIsCaseInsensitiveBecauseTheFilesystemIs(t *testing.T) {
	// macOS's default APFS is case-insensitive, so ".GIT/config" IS ".git/config".
	// Both the guard and the built-in ignore rules compared case-sensitively, so
	// a project we do not control could name a path that walks straight past
	// invariant 7 and into the real .git — or over dsx's own ledger.
	for _, rel := range []string{
		".GIT/config", ".Git/hooks/pre-commit", "a/NODE_MODULES/x.js",
		".DSX-STATE.JSON", ".DSXignore", "sub/.Git/HEAD",
	} {
		if err := checkRemotePath(rel); err == nil {
			t.Errorf("checkRemotePath(%q) allowed it; on a case-insensitive volume that is the real thing", rel)
		}
	}
	// The ordinary paths must still be allowed.
	for _, rel := range []string{"styles.css", "git.md", "src/gitignore-notes.txt", "nodes/x.js"} {
		if err := checkRemotePath(rel); err != nil {
			t.Errorf("checkRemotePath(%q) refused a legitimate path: %v", rel, err)
		}
	}
}

func TestBuiltinIgnoresAreCaseInsensitiveToo(t *testing.T) {
	s := mustParseIgnore(t, "")
	for _, p := range []string{".GIT/config", "NODE_MODULES/x.js", ".DS_STORE", ".DSXIGNORE"} {
		if !s.match(p) {
			t.Errorf("built-in exclusion missed %q; on a case-insensitive volume it is the real path", p)
		}
	}
	// A user rule stays case-sensitive, as gitignore's are.
	u := mustParseIgnore(t, "dist/\n")
	if u.match("DIST/app.js") {
		t.Error("a user rule became case-insensitive; gitignore's are not")
	}
}

// ---------------------------------------------------------------------------
// the contract with an agent
// ---------------------------------------------------------------------------

func TestUnclassifiedErrorRendersItsMessageOnce(t *testing.T) {
	// dsxerr.Classify() set Msg AND Err to the same error, and both renderers
	// concatenate the two, so every unclassified failure said everything twice.
	err := dsxerr.Classify(errNoCredentialsSentinel{})
	if got := err.Error(); strings.Count(got, "boom") != 1 {
		t.Errorf("prose says it %d times: %q", strings.Count(got, "boom"), got)
	}
	line := dsxerr.Render(errNoCredentialsSentinel{}, true)
	if strings.Count(line, "boom") != 1 {
		t.Errorf("--json says it %d times: %q", strings.Count(line, "boom"), line)
	}
	if strings.Count(dsxerr.Render(errNoCredentialsSentinel{}, false), "boom") != 1 {
		t.Errorf("prose renderer doubled it: %q", dsxerr.Render(errNoCredentialsSentinel{}, false))
	}
}

type errNoCredentialsSentinel struct{}

func (errNoCredentialsSentinel) Error() string { return "boom" }

func TestDoctorDiagnosesTheCredentialDsxWillActuallySend(t *testing.T) {
	// runDoctor read the stored credential while every request uses DSX_TOKEN,
	// so a working install was reported "fail credentials" and exited 1 — on the
	// same run whose endpoint check had just authenticated with that very token.
	// doctor is the command people run to find out why something is broken; it
	// must not invent the breakage.
	t.Setenv("DSX_TOKEN", "sk-ant-oat01-SENTINEL")
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir()) // no stored login at all

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: "{}"}
	})
	rep := runDoctor(t.Context(), fakeClient(f))

	for _, c := range rep.Checks {
		if c.Name == "credentials" && c.Status == checkFail {
			t.Errorf("doctor called a working DSX_TOKEN setup broken: %s", c.Detail)
		}
		if strings.Contains(c.Detail, "SENTINEL") {
			t.Fatalf("doctor printed the token: %s", c.Detail)
		}
	}
	if !rep.OK {
		t.Errorf("doctor failed a healthy install: %s", rep.render(false))
	}
	if !strings.Contains(rep.render(false), "DSX_TOKEN") {
		t.Errorf("doctor did not say where the token came from: %s", rep.render(false))
	}
}

func TestServerDetectedConflictExitsThreeLikeALocallyDetectedOne(t *testing.T) {
	// A stale if_match is the exact race the guard exists to catch, and the
	// server is the only party that can see it. dsx classified the refusal as a
	// generic failure: exit 1, {"error":"error"} — so an agent that branches on
	// exit 3 to fetch a human sails past the one case that needs one.
	//
	// The reply shape below is copied from the live endpoint (2026-07-17), not
	// guessed: a finder claimed a {status:"conflict"} marker that does not exist,
	// and a skeptic was right to refuse it. This is what the server really sends.
	body := `{"conflicts":[{"path":"a.css","etag":"1784268009093847",` +
		`"current_content":"<untrusted-project-content path=\"a.css\" etag=\"1784268009093847\">\nhello\n\n</untrusted-project-content>"}],` +
		`"message":"write_files: refused — the user (or another writer) changed one or more of these files since your if_match etag. Nothing was written."}`

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(fileEntry("a.css", "e1", 1))}
		}
		return fakeReply{Text: body, IsError: true}
	})

	dir := t.TempDir()
	mkfile(t, dir, "a.css", "local edit")
	syncSeedState(t, dir, syncState{
		ProjectID: "p1",
		Files:     map[string]fileState{"a.css": {Etag: "e1", Size: 1, SHA: sha256hex([]byte("x"))}},
	})

	_, err := runPush(t.Context(), fakeClient(f), pushOpts{projectID: "p1", dir: dir, concurrency: 1})
	if err == nil {
		t.Fatal("the server refused the write and dsx reported success")
	}
	de := dsxerr.Classify(err)
	if de.Kind != dsxerr.KindConflict {
		t.Fatalf("a server-detected conflict classified %q (exit %d), want %q (exit %d)",
			de.Kind, de.Kind.ExitCode(), dsxerr.KindConflict, dsxerr.ExitConflict)
	}
	if len(de.Paths) != 1 || de.Paths[0] != "a.css" {
		t.Errorf("paths = %v, want the conflicting path so --json can name it", de.Paths)
	}
}

func TestPutSelfAuthorisesLikePushDoes(t *testing.T) {
	// push recovers from needs_project_grant by minting a path-scoped
	// plan_token. put, cp and support-js went through emit and did not, so the
	// same write that push completes left `dsx put` at exit 1 with
	// {"error":"error"} and no next step — on a project that has no standing
	// grant, which is the default.
	var sawPlan bool
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "write_files":
			if _, ok := args["plan_token"]; !ok {
				return fakeReply{
					HTTPStatus: 403,
					HTTPBody:   `{"error":"needs_project_grant","project_id":"p1","prompt":"Allow Claude to edit this project?"}`,
				}
			}
			return fakeReply{Text: `{"etags":{"a.css":"e9"},"written":1}`}
		case "finalize_plan":
			sawPlan = true
			return fakeReply{Text: `{"plan_token":"plan_abc"}`}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	dir := t.TempDir()
	mkfile(t, dir, "a.css", "body{}")
	err := cmdPut(t.Context(), fakeClient(f), []string{"p1", "a.css", filepath.Join(dir, "a.css")})
	if err != nil {
		t.Fatalf("put did not recover from needs_project_grant the way push does: %v", err)
	}
	if !sawPlan {
		t.Error("put never called finalize_plan")
	}
}

func TestVersionHonoursJSON(t *testing.T) {
	// --json is documented as making stdout one JSON document, with no carve-out.
	// version was dispatched before any FlagSet and printed prose, so a caller
	// discovering the binary's version had to special-case it.
	out, err := captureStdout(t, func() error { return cmdVersion([]string{"--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("version --json printed prose: %q", out)
	}
	var got struct {
		Version string `json:"version"`
		OS      string `json:"os"`
		Arch    string `json:"arch"`
		Go      string `json:"go"`
	}
	if err := jsonUnmarshalString(out, &got); err != nil {
		t.Fatalf("version --json is not JSON: %v\n%s", err, out)
	}
	if got.OS != runtime.GOOS || got.Arch != runtime.GOARCH {
		t.Errorf("os/arch = %s/%s, want %s/%s", got.OS, got.Arch, runtime.GOOS, runtime.GOARCH)
	}
	if got.Go == "" || got.Version == "" {
		t.Errorf("version JSON is missing fields: %+v", got)
	}

	// Prose still works and stays one line.
	prose, err := captureStdout(t, func() error { return cmdVersion(nil) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(prose, "dsx ") || strings.Count(strings.TrimSpace(prose), "\n") != 0 {
		t.Errorf("prose version = %q", prose)
	}
}

// ---------------------------------------------------------------------------
// protocol
// ---------------------------------------------------------------------------

func jsonUnmarshalString(s string, v any) error { return json.Unmarshal([]byte(s), v) }

func TestCaseInsensitiveBuiltinsDoNotSwallowLegitimatePaths(t *testing.T) {
	// Making the built-ins case-insensitive is the kind of change that quietly
	// widens a rule. `.git` must not start matching `.gitignore`, and a project
	// legitimately holding `.github/workflows/ci.yml` must still sync it.
	//
	// The rules are anchored (^(?:.*/)?\.git$), so they do not; this test is here
	// so that stays true if anyone touches compileIgnorePattern.
	s := mustParseIgnore(t, "")
	for _, p := range []string{
		".gitignore", ".gitattributes", ".github/workflows/ci.yml", "a/.gitkeep",
		"digit.css", "legit.md", "node_modules.md", "src/gitignore.txt", "DS_Store.md",
	} {
		if s.match(p) {
			t.Errorf("%q is excluded from the sync; it is a legitimate project file", p)
		}
	}
	for _, p := range []string{".git/config", ".GIT/config", "node_modules/x.js", ".DS_Store"} {
		if !s.match(p) {
			t.Errorf("%q is not excluded", p)
		}
	}
}
