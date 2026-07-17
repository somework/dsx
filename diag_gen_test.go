package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/somework/dsx/internal/auth"
	"github.com/somework/dsx/internal/dsxerr"
)

// Tests for doctor.go and completion.go -- the two commands a user runs when
// something is already wrong. Both have the same failure mode: saying something
// reassuring and untrue. A doctor that reports ok on a broken login, or a
// completion that silently omits a command, is worse than one that does not
// exist, because it ends the investigation.

// ---------------------------------------------------------------------------
// Pinning the credential store
// ---------------------------------------------------------------------------

// diagPinCredentialStore points every credential lookup at a temp dir, so a
// doctor run reads what the test wrote rather than the developer's real login.
//
// CLAUDE_SECURESTORAGE_CONFIG_DIR dominates both credentialsPath and
// keychainServiceName. The service name it derives is sha256(dir)[:8], which no
// keychain holds, so readCredentialsChain falls through the keychain lane
// (auth.ErrNoCredentials) and lands on the file lane the test controls. The real
// "Claude Code-credentials" item is never queried.
func diagPinCredentialStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", dir)
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	// Assert the pin rather than trust it. If this ever resolved to the default
	// service name, every doctor test below would be reading the developer's
	// real login -- passing or failing on credentials no CI machine has.
	if svc := auth.KeychainServiceName(os.LookupEnv, auth.HomeDir()); svc == "Claude Code-credentials" {
		t.Fatalf("credential store is not pinned: service resolved to the real item %q", svc)
	}
	return dir
}

// writeCreds plants a Claude Code credentials file and returns its path.
//
// The JSON is spelled out here rather than marshalled through auth's own blob
// type. A test that builds the file with the producer's struct agrees with it by
// construction: rename a json tag and both sides move together, green, while
// every file Claude Code actually wrote stops decoding and dsx reports "no login
// found" to a user who is plainly logged in. That is the ledger's failure mode
// (invariant 5) in a different file, and the reason ledger_golden_test.go
// hand-writes its fixture too.
//
// The filename is hardcoded for the same reason: `.credentials.json` is the name
// Claude Code writes, not a name dsx gets to choose.
func writeCreds(t *testing.T, dir string, c auth.Creds) string {
	t.Helper()
	scopes, err := json.Marshal(c.Scopes)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"expiresAt":%d,"scopes":%s}}`,
		c.AccessToken, c.ExpiresAt, scopes)

	p := filepath.Join(dir, ".credentials.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// diagHealthyCreds is a login with nothing wrong with it.
func diagHealthyCreds() auth.Creds {
	return auth.Creds{
		AccessToken: "sk-ant-oat01-diag",
		ExpiresAt:   time.Now().Add(8 * time.Hour).UnixMilli(),
		Scopes:      []string{"user:inference", enforcedScope},
	}
}

func diagCheckNamed(t *testing.T, rep doctorReport, name string) check {
	t.Helper()
	for _, c := range rep.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q check in the report: %+v", name, rep.Checks)
	return check{}
}

// diagHealthyServer answers tools/list the way the endpoint does and fails the
// test if doctor reaches for anything else: every check it makes must be
// read-only, or running `dsx doctor` on a sick install could change something.
func diagHealthyServer(t *testing.T) *fakeMCP {
	t.Helper()
	return newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		t.Errorf("doctor called tool %q; it must diagnose with read-only calls only", name)
		return fakeReply{}
	})
}

// ---------------------------------------------------------------------------
// tokenExpiryCheck
// ---------------------------------------------------------------------------

func TestExpiredTokenFailsAndNamesTheFix(t *testing.T) {
	t.Parallel()
	got := tokenExpiryCheck(time.Now().Add(-90 * time.Minute).UnixMilli())

	if got.Status != checkFail {
		t.Errorf("expired token reported %q, want %q — this is the whole reason to run doctor", got.Status, checkFail)
	}
	// Errors say what to do next. dsx refuses to refresh the token itself
	// (auth.go), so the only exit is the user running `claude`.
	if !strings.Contains(got.Detail, "claude") {
		t.Errorf("detail does not tell the user to run claude: %q", got.Detail)
	}
	// The elapsed time must read forwards. `expired -1h30m0s ago` is the shape
	// of a sign bug, and it makes the one number in the line nonsense.
	if !strings.Contains(got.Detail, "1h30m0s") {
		t.Errorf("detail does not report how long ago: %q", got.Detail)
	}
	if strings.Contains(got.Detail, "-1h30m0s") {
		t.Errorf("elapsed time reported as negative: %q", got.Detail)
	}
}

func TestLiveTokenPassesAndReportsTimeLeft(t *testing.T) {
	t.Parallel()
	got := tokenExpiryCheck(time.Now().Add(2 * time.Hour).UnixMilli())

	if got.Status != checkOK {
		t.Errorf("a token good for two hours reported %q, want %q", got.Status, checkOK)
	}
	if !strings.Contains(got.Detail, "2h0m0s") {
		t.Errorf("detail does not report the time left: %q", got.Detail)
	}
}

// A credential with no expiry is not a healthy credential: it is one dsx cannot
// judge. Reporting ok would answer "is my login alive?" with a guess, and 0 is
// also what an unparsed or absent expiresAt looks like. Worse, feeding a
// non-positive value to time.UnixMilli renders it as 1970 -- doctor would claim
// the token expired decades ago, which is a lie in the opposite direction.
func TestATokenWithNoRecordedExpiryWarnsRatherThanBeingJudged(t *testing.T) {
	t.Parallel()
	for _, ms := range []int64{0, -1, -1 << 40} {
		got := tokenExpiryCheck(ms)
		if got.Status != checkWarn {
			t.Errorf("expiresAt=%d reported %q, want %q — dsx cannot judge an expiry it does not have",
				ms, got.Status, checkWarn)
		}
		if strings.Contains(got.Detail, "expired") {
			t.Errorf("expiresAt=%d claims an expiry it never read: %q", ms, got.Detail)
		}
	}
}

// ---------------------------------------------------------------------------
// scopeCheck
// ---------------------------------------------------------------------------

func TestScopeCheckPassesWhenTheEnforcedScopeIsPresent(t *testing.T) {
	t.Parallel()
	got := scopeCheck([]string{"user:profile", enforcedScope, "user:inference"})
	if got.Status != checkOK {
		t.Fatalf("scopes %v reported %q, want %q", []string{enforcedScope}, got.Status, checkOK)
	}
}

// This test deliberately does NOT assert a failure. enforcedScope was
// established by probing the endpoint, not from documentation, and the server
// is free to change its mind. Failing here would make dsx refuse a login the
// server would have accepted -- dsx guessing on the server's behalf about a
// scope list it does not recognise. A warn says what dsx noticed without
// overriding the only authority on the question.
func TestAnAbsentScopeWarnsAndNeverBlocksTheRun(t *testing.T) {
	t.Parallel()
	got := scopeCheck([]string{"user:inference", "user:profile"})

	if got.Status == checkFail {
		t.Fatalf("an unrecognised scope list was treated as fatal: %+v", got)
	}
	if got.Status != checkWarn {
		t.Fatalf("scopes reported %q, want %q", got.Status, checkWarn)
	}
	// The detail has to carry both halves: what dsx wanted and what it found.
	// Without the second, a user whose scopes changed cannot see what changed.
	if !strings.Contains(got.Detail, enforcedScope) {
		t.Errorf("detail does not name the scope dsx wanted: %q", got.Detail)
	}
	if !strings.Contains(got.Detail, "user:inference") {
		t.Errorf("detail does not list the scopes actually held: %q", got.Detail)
	}
}

func TestNoScopesAtAllStillOnlyWarns(t *testing.T) {
	t.Parallel()
	got := scopeCheck(nil)
	if got.Status != checkWarn {
		t.Fatalf("an empty scope list reported %q, want %q", got.Status, checkWarn)
	}
}

// ---------------------------------------------------------------------------
// clockCheck
// ---------------------------------------------------------------------------

// Skew is judged by magnitude, in both directions.
//
// This check exists because dsx compares the token's expiresAt against the
// LOCAL clock, so a machine far enough off calls a live token dead -- a failure
// indistinguishable from a real expiry, which sends the user to re-run `claude`
// forever. A local clock running behind the server's produces a negative skew,
// and comparing that signed value against the positive thresholds would report
// ok for every one of them: half of the failures the check exists to find would
// be invisible, and they are the half that matters (a slow clock is the one
// that calls a live token expired).
func TestClockSkewIsJudgedByMagnitudeInBothDirections(t *testing.T) {
	t.Parallel()
	server := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	nanos := server.UnixNano()

	cases := []struct {
		name string
		now  time.Time
		want checkStatus
	}{
		{"clocks in step", server, checkOK},
		{"local a second ahead", server.Add(time.Second), checkOK},
		{"local a second behind", server.Add(-time.Second), checkOK},
		{"just inside the warn threshold", server.Add(clockSkewWarn - time.Nanosecond), checkOK},
		{"exactly at the warn threshold", server.Add(clockSkewWarn), checkWarn},
		{"exactly at the warn threshold, behind", server.Add(-clockSkewWarn), checkWarn},
		{"local two minutes ahead", server.Add(2 * time.Minute), checkWarn},
		{"local two minutes behind", server.Add(-2 * time.Minute), checkWarn},
		{"just inside the fail threshold", server.Add(clockSkewFail - time.Nanosecond), checkWarn},
		{"exactly at the fail threshold", server.Add(clockSkewFail), checkFail},
		{"exactly at the fail threshold, behind", server.Add(-clockSkewFail), checkFail},
		{"local an hour ahead", server.Add(time.Hour), checkFail},
		{"local an hour behind", server.Add(-time.Hour), checkFail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := clockCheck(nanos, tc.now)
			if got.Status != tc.want {
				t.Errorf("skew %v reported %q (%s), want %q",
					tc.now.Sub(server), got.Status, got.Detail, tc.want)
			}
		})
	}
}

// 0 is mcp.go's sentinel for "no reply carried a Date header". Treating it as a
// timestamp would compare the local clock against 1970 and report a 56-year
// skew as a hard failure; treating it as ok would compare the local clock
// against itself and pass on precisely the skewed machine this check exists to
// find. Neither is an answer dsx has, so it says so.
func TestNoServerDateWarnsRatherThanInventingAVerdict(t *testing.T) {
	t.Parallel()
	got := clockCheck(0, time.Now())
	if got.Status != checkWarn {
		t.Fatalf("a missing Date header reported %q (%s), want %q", got.Status, got.Detail, checkWarn)
	}
	if got.Detail == "" {
		t.Error("the warn does not say why the skew is unknown")
	}
}

func TestAFailingClockSaysWhyItMatters(t *testing.T) {
	t.Parallel()
	server := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	got := clockCheck(server.UnixNano(), server.Add(-time.Hour))
	// The user's visible symptom is an auth failure, not a wrong clock. Naming
	// the connection is what stops them re-running `claude` forever.
	if !strings.Contains(got.Detail, "expiry") {
		t.Errorf("detail does not connect skew to the failure it causes: %q", got.Detail)
	}
}

// ---------------------------------------------------------------------------
// roundDur
// ---------------------------------------------------------------------------

// "expires in 45s" and "expired 3s ago" are the two most decision-relevant
// readings doctor produces. Rounding those to the minute renders them "0s",
// which reads as a bug rather than as an expiry about to happen.
func TestRoundDurKeepsSecondsVisibleBelowAMinute(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   time.Duration
		want time.Duration
	}{
		{45*time.Second + 400*time.Millisecond, 45 * time.Second},
		{59 * time.Second, 59 * time.Second},
		{3 * time.Second, 3 * time.Second},
		{90*time.Minute + 29*time.Second, 90 * time.Minute},
		{2*time.Hour + 31*time.Second, 2*time.Hour + time.Minute},
	}
	for _, tc := range cases {
		if got := roundDur(tc.in); got != tc.want {
			t.Errorf("roundDur(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// credentialsModeCheck
// ---------------------------------------------------------------------------

func TestCredentialsFileModeIsJudgedByWhoElseCanRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows does not carry unix permission bits")
	}
	t.Parallel()
	cases := []struct {
		mode os.FileMode
		want checkStatus
	}{
		{0o600, checkOK},
		// Stricter than Claude Code writes is not a problem: the check is about
		// who else can read a bearer token, not about matching a mode exactly.
		{0o400, checkOK},
		{0o644, checkWarn},
		{0o640, checkWarn},
		{0o604, checkWarn},
		{0o666, checkWarn},
	}
	for _, tc := range cases {
		t.Run(tc.mode.String(), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			p := writeCreds(t, dir, auth.Creds{AccessToken: "x"})
			if err := os.Chmod(p, tc.mode); err != nil {
				t.Fatal(err)
			}
			got := credentialsModeCheck(p)
			if got.Status != tc.want {
				t.Fatalf("mode %04o reported %q (%s), want %q", tc.mode.Perm(), got.Status, got.Detail, tc.want)
			}
			if tc.want == checkWarn {
				// The user has to be able to act on it: which file, which mode.
				if !strings.Contains(got.Detail, "0644") && tc.mode == 0o644 {
					t.Errorf("detail does not name the offending mode: %q", got.Detail)
				}
				if !strings.Contains(got.Detail, p) {
					t.Errorf("detail does not name the file: %q", got.Detail)
				}
			}
		})
	}
}

// A keychain-backed install has no plaintext file at all. That is the healthy
// shape, not something to warn about, or every macOS user would see a warning
// for doing the right thing.
func TestAnAbsentCredentialsFileIsNotAComplaint(t *testing.T) {
	t.Parallel()
	got := credentialsModeCheck(filepath.Join(t.TempDir(), "nothing-here.json"))
	if got.Status != checkOK {
		t.Fatalf("an absent file reported %q (%s), want %q", got.Status, got.Detail, checkOK)
	}
}

// A stat that fails for any other reason must not collapse into the "no file
// here" answer above: an unreadable credentials file is a real problem, and
// reporting it as ok hides it behind a reassuring line.
func TestAnUnstatableCredentialsPathWarnsRatherThanReadingAsAbsent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows does not enforce directory search bits this way")
	}
	if os.Getuid() == 0 {
		t.Skip("root ignores the permission bits this test relies on")
	}
	t.Parallel()

	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	p := writeCreds(t, locked, auth.Creds{AccessToken: "x"})
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	// Restore before TempDir's cleanup tries to walk it.
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	got := credentialsModeCheck(p)
	if got.Status != checkWarn {
		t.Fatalf("an unreadable credentials path reported %q (%s), want %q",
			got.Status, got.Detail, checkWarn)
	}
}

// ---------------------------------------------------------------------------
// describeSource
// ---------------------------------------------------------------------------

// Naming the store is the single most useful thing doctor can say to someone
// whose login it cannot see -- and for the keychain, "keychain" alone is not
// naming it. A machine with CLAUDE_CONFIG_DIR set holds several logins under
// several service names, and which item dsx read is the whole question.
func TestDescribeSourceNamesTheKeychainItemNotJustTheKeychain(t *testing.T) {
	dir := diagPinCredentialStore(t)
	want := auth.KeychainServiceName(os.LookupEnv, auth.HomeDir())

	got := describeSource(auth.SrcKeychain)
	if !strings.Contains(got, want) {
		t.Fatalf("describeSource(keychain) = %q, want it to name service %q", got, want)
	}
	if got == string(auth.SrcKeychain) {
		t.Fatal("the keychain source names no item at all")
	}

	if fileDesc := describeSource(auth.SrcFile); !strings.Contains(fileDesc, dir) {
		t.Errorf("describeSource(file) = %q, want it to name the path under %q", fileDesc, dir)
	}
	if none := describeSource(auth.SrcNone); none == "" {
		t.Error("describeSource(none) says nothing at all")
	}
}

// ---------------------------------------------------------------------------
// runDoctor
// ---------------------------------------------------------------------------

// A warn is not a failure. Exiting non-zero on a run whose worst finding is
// "your credentials file is 0644" trains people to ignore doctor's exit code,
// which is the only part of it a script reads.
func TestAWarnAloneKeepsTheReportOK(t *testing.T) {
	dir := diagPinCredentialStore(t)
	p := writeCreds(t, dir, diagHealthyCreds())
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}

	rep := runDoctor(context.Background(), diagHealthyServer(t).client())

	// Guard against a vacuous pass: if the mode check ever stops firing, this
	// test would still be green while asserting nothing.
	var warns int
	for _, c := range rep.Checks {
		if c.Status == checkWarn {
			warns++
		}
	}
	if warns == 0 {
		t.Fatalf("expected the 0644 file to warn; report was %+v", rep.Checks)
	}
	if !rep.OK {
		t.Errorf("a warn-only run reported not-OK: %+v", rep.Checks)
	}
}

func TestReportIsNotOKExactlyWhenSomeCheckFails(t *testing.T) {
	dir := diagPinCredentialStore(t)
	writeCreds(t, dir, auth.Creds{
		AccessToken: "sk-ant-oat01-diag",
		ExpiresAt:   time.Now().Add(-time.Hour).UnixMilli(), // the only fault
		Scopes:      []string{enforcedScope},
	})

	rep := runDoctor(context.Background(), diagHealthyServer(t).client())

	if got := diagCheckNamed(t, rep, "token").Status; got != checkFail {
		t.Fatalf("expired token check = %q, want %q", got, checkFail)
	}
	if rep.OK {
		t.Errorf("a report containing a failure reported OK: %+v", rep.Checks)
	}
}

// The endpoint check must report the reply it actually parsed. "ok" with no
// evidence behind it is the failure mode of every health check.
func TestAHealthyRunReportsTheEndpointItReachedAndWhatItSaw(t *testing.T) {
	dir := diagPinCredentialStore(t)
	writeCreds(t, dir, diagHealthyCreds())
	c := diagHealthyServer(t).client()

	rep := runDoctor(context.Background(), c)

	if !rep.OK {
		t.Fatalf("a healthy install reported not-OK: %+v", rep.Checks)
	}
	ep := diagCheckNamed(t, rep, "endpoint")
	if ep.Status != checkOK {
		t.Fatalf("endpoint check = %q (%s)", ep.Status, ep.Detail)
	}
	if !strings.Contains(ep.Detail, c.endpoint) {
		t.Errorf("endpoint detail does not name the URL it used: %q", ep.Detail)
	}
	// The fake's tools/list answers with exactly two tools.
	if !strings.Contains(ep.Detail, "2 tools") {
		t.Errorf("endpoint detail does not report what the reply held: %q", ep.Detail)
	}
	// A reply proves the clock too: httptest sends a Date header.
	if got := diagCheckNamed(t, rep, "clock").Status; got != checkOK {
		t.Errorf("clock check = %q, want %q against a server one second away", got, checkOK)
	}
	if got := diagCheckNamed(t, rep, "credentials").Status; got != checkOK {
		t.Errorf("credentials check = %q, want %q", got, checkOK)
	}
	if !strings.Contains(diagCheckNamed(t, rep, "credentials").Detail, dir) {
		t.Error("the credentials check does not name the store it read")
	}
}

func TestARejectedTokenFailsTheEndpointCheck(t *testing.T) {
	dir := diagPinCredentialStore(t)
	writeCreds(t, dir, diagHealthyCreds())

	// 401 is not retryable, so this returns without walking the backoff.
	c := mcpGenRawServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	rep := runDoctor(context.Background(), c)

	ep := diagCheckNamed(t, rep, "endpoint")
	if ep.Status != checkFail {
		t.Fatalf("a 401 reported %q (%s), want %q", ep.Status, ep.Detail, checkFail)
	}
	if rep.OK {
		t.Error("an unreachable endpoint reported OK")
	}
	// Without a reply there is no Date header, so claiming anything about the
	// clock would be inventing it. The check must be absent, not green.
	for _, c := range rep.Checks {
		if c.Name == "clock" {
			t.Errorf("a clock verdict was reported without a reply to judge it by: %+v", c)
		}
	}
}

// "not signed in" is the report a user acts on. It must survive a store that
// holds nothing, and it must not be reported as OK.
func TestNoLoginAnywhereFailsTheCredentialsCheck(t *testing.T) {
	diagPinCredentialStore(t) // pinned at an empty dir: no keychain item, no file

	rep := runDoctor(context.Background(), diagHealthyServer(t).client())

	if got := diagCheckNamed(t, rep, "credentials").Status; got != checkFail {
		t.Fatalf("credentials check = %q, want %q when no login exists", got, checkFail)
	}
	if rep.OK {
		t.Error("a run that found no login reported OK")
	}
	// Nothing was read, so there is nothing to say about expiry or scopes.
	// Reporting on a credential dsx never got would be reporting on the zero
	// value: "expired 56 years ago", from a struct nobody filled in.
	for _, c := range rep.Checks {
		if c.Name == "token" || c.Name == "scopes" {
			t.Errorf("%q was judged despite no credential being read: %+v", c.Name, c)
		}
	}
}

// ---------------------------------------------------------------------------
// The token must not appear in doctor's output
// ---------------------------------------------------------------------------

// dsx reads the credential and never prints it. doctor is the command that
// handles the most credential state, and the one most likely to be run with a
// terminal being recorded or pasted into an issue.
func TestDoctorNeverRendersTheToken(t *testing.T) {
	const secret = "sk-ant-oat01-DOCTOR-MUST-NEVER-PRINT-THIS"

	dir := diagPinCredentialStore(t)
	creds := diagHealthyCreds()
	creds.AccessToken = secret
	writeCreds(t, dir, creds)
	t.Setenv("DSX_TOKEN", secret+"-from-env")

	c := diagHealthyServer(t).client()
	c.token = secret // the bearer dsx would actually send

	for _, asJSON := range []bool{false, true} {
		args := []string{}
		if asJSON {
			args = append(args, "--json")
		}
		out, err := captureStdout(t, func() error { return cmdDoctor(context.Background(), c, args) })
		if err != nil {
			t.Fatalf("doctor(--json=%v) failed on a healthy install: %v", asJSON, err)
		}
		if out == "" {
			t.Fatalf("doctor(--json=%v) printed nothing; the assertion below would be vacuous", asJSON)
		}
		if strings.Contains(out, secret) {
			t.Fatalf("the token leaked into doctor's output (--json=%v):\n%s", asJSON, out)
		}
	}
}

// ---------------------------------------------------------------------------
// doctorReport.render
// ---------------------------------------------------------------------------

func diagSampleReport() doctorReport {
	return doctorReport{
		Checks: []check{
			{Name: "credentials", Status: checkOK, Detail: "file /tmp/x/.credentials.json"},
			{Name: "token", Status: checkWarn, Detail: "no expiry recorded"},
			{Name: "clock", Status: checkFail, Detail: "local clock is 1h0m0s from the server's"},
		},
		OK: false,
	}
}

func TestDoctorJSONIsOneLineAndRoundTrips(t *testing.T) {
	t.Parallel()
	rep := diagSampleReport()
	out := rep.render(true)

	if !json.Valid([]byte(out)) {
		t.Fatalf("--json output is not JSON:\n%s", out)
	}
	if strings.Contains(strings.TrimRight(out, "\n"), "\n") {
		t.Errorf("--json output must stay one line: %q", out)
	}
	var back doctorReport
	if err := json.Unmarshal([]byte(out), &back); err != nil {
		t.Fatalf("unmarshalling doctor's own JSON: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(back, rep) {
		t.Errorf("round trip lost data:\n got %+v\nwant %+v", back, rep)
	}
	// The verdict is what a script branches on; it must survive as a bool
	// rather than being inferred from the prose.
	if back.OK {
		t.Error("ok=false did not survive the round trip")
	}
}

func TestDoctorProseIsOneLinePerCheck(t *testing.T) {
	t.Parallel()
	rep := diagSampleReport()
	out := rep.render(false)

	lines := strings.Split(out, "\n")
	if len(lines) != len(rep.Checks) {
		t.Fatalf("prose has %d lines for %d checks:\n%s", len(lines), len(rep.Checks), out)
	}
	for i, line := range lines {
		c := rep.Checks[i]
		for _, want := range []string{string(c.Status), c.Name, c.Detail} {
			if !strings.Contains(line, want) {
				t.Errorf("line %d %q lacks %q", i, line, want)
			}
		}
	}
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("prose mode emitted JSON:\n%s", out)
	}
}

func TestAnEmptyReportRendersWithoutPanicking(t *testing.T) {
	t.Parallel()
	if got := (doctorReport{OK: true}).render(false); got != "" {
		t.Errorf("empty prose report = %q, want empty", got)
	}
	if got := (doctorReport{OK: true}).render(true); !json.Valid([]byte(got)) {
		t.Errorf("empty JSON report is not JSON: %q", got)
	}
}

// ---------------------------------------------------------------------------
// cmdDoctor
// ---------------------------------------------------------------------------

func TestCmdDoctorFailsExactlyWhenTheReportIsNotOK(t *testing.T) {
	dir := diagPinCredentialStore(t)
	writeCreds(t, dir, diagHealthyCreds())
	healthy := diagHealthyServer(t).client()

	out, err := captureStdout(t, func() error { return cmdDoctor(context.Background(), healthy, nil) })
	if err != nil {
		t.Fatalf("a healthy install exited non-zero: %v\n%s", err, out)
	}
	if out == "" {
		t.Error("doctor printed nothing on a healthy install")
	}

	broken := mcpGenRawServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	out, err = captureStdout(t, func() error { return cmdDoctor(context.Background(), broken, nil) })
	if err == nil {
		t.Fatalf("doctor found a problem and still exited 0:\n%s", out)
	}
	if got := dsxerr.ExitCodeFor(err); got != dsxerr.ExitFailure {
		t.Errorf("doctor exit = %d, want %d", got, dsxerr.ExitFailure)
	}
	// The report is the point of the command; it must be printed even when the
	// command then reports failure, or the user learns only that "something" is
	// wrong.
	if !strings.Contains(out, "endpoint") {
		t.Errorf("the failing report was not printed:\n%s", out)
	}
}

func TestCmdDoctorRejectsAnUnknownFlagAsUsage(t *testing.T) {
	// A bad invocation must not reach the network, and must not be reported as
	// a diagnosis.
	err := cmdDoctor(context.Background(), diagHealthyServer(t).client(), []string{"--nope"})
	if err == nil {
		t.Fatal("an unknown flag was accepted")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("unknown flag classified %q, want %q", got, dsxerr.KindUsage)
	}
}

// ---------------------------------------------------------------------------
// completion.go
// ---------------------------------------------------------------------------

var (
	diagBashList = regexp.MustCompile(`compgen -W "([^"]*)"`)
	diagZshList  = regexp.MustCompile(`cmds=\(([^)]*)\)`)
	diagFishItem = regexp.MustCompile(`(?m)^complete -c dsx -n __fish_use_subcommand -a (\S+)$`)
)

// diagCompletedCommands extracts the command words a shell would actually offer.
// A plain substring search would not do: "conv" is a substring of "conv-put",
// so a script that had lost "conv" entirely would still look present.
func diagCompletedCommands(t *testing.T, shell, script string) []string {
	t.Helper()
	switch shell {
	case "bash":
		m := diagBashList.FindStringSubmatch(script)
		if m == nil {
			t.Fatalf("bash script offers no command list:\n%s", script)
		}
		return strings.Fields(m[1])
	case "zsh":
		m := diagZshList.FindStringSubmatch(script)
		if m == nil {
			t.Fatalf("zsh script offers no command list:\n%s", script)
		}
		return strings.Fields(m[1])
	case "fish":
		var out []string
		for _, m := range diagFishItem.FindAllStringSubmatch(script, -1) {
			out = append(out, m[1])
		}
		if len(out) == 0 {
			t.Fatalf("fish script offers no subcommands:\n%s", script)
		}
		return out
	}
	t.Fatalf("unhandled shell %q", shell)
	return nil
}

var diagShells = []string{"bash", "zsh", "fish"}

// A command that exists but is not in the completion is invisible to a user
// pressing Tab, and there is no error to lead them anywhere: the command simply
// looks like it does not exist.
func TestEveryCommandNameIsOfferedByEveryShell(t *testing.T) {
	t.Parallel()
	want := append([]string(nil), commandNames...)
	sort.Strings(want)

	for _, shell := range diagShells {
		t.Run(shell, func(t *testing.T) {
			t.Parallel()
			script, err := completionScript(shell)
			if err != nil {
				t.Fatalf("completionScript(%q): %v", shell, err)
			}
			if script == "" {
				t.Fatalf("%s script is empty", shell)
			}
			if !strings.Contains(script, "dsx") {
				t.Errorf("%s script never names dsx:\n%s", shell, script)
			}

			got := diagCompletedCommands(t, shell, script)
			sort.Strings(got)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s completes a different set than commandNames\n got %v\nwant %v", shell, got, want)
			}
		})
	}
}

// Every shell needs its registration line, or the script sources cleanly and
// completes nothing -- a silent no-op is the worst outcome for this command.
func TestEachScriptRegistersItselfWithItsShell(t *testing.T) {
	t.Parallel()
	cases := map[string][]string{
		"bash": {"complete -F _dsx dsx", "compgen -W"},
		"zsh":  {"#compdef dsx", "compdef _dsx dsx"},
		"fish": {"complete -c dsx -f"},
	}
	for shell, wants := range cases {
		script, err := completionScript(shell)
		if err != nil {
			t.Fatalf("completionScript(%q): %v", shell, err)
		}
		for _, want := range wants {
			if !strings.Contains(script, want) {
				t.Errorf("%s script lacks %q:\n%s", shell, want, script)
			}
		}
	}
}

// The list is sorted by completionScript rather than assumed sorted at its
// source, so Tab output stays alphabetical however commandNames is maintained.
func TestTheOfferedListIsSortedEvenWhenTheSourceListIsNot(t *testing.T) {
	saved := append([]string(nil), commandNames...)
	t.Cleanup(func() { commandNames = saved })
	// Nothing else in the package reads commandNames, and this test is
	// sequential, so no reader can observe the reversal.
	shuffled := append([]string(nil), commandNames...)
	for i, j := 0, len(shuffled)-1; i < j; i, j = i+1, j-1 {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	}
	commandNames = shuffled

	for _, shell := range diagShells {
		script, err := completionScript(shell)
		if err != nil {
			t.Fatalf("completionScript(%q): %v", shell, err)
		}
		got := diagCompletedCommands(t, shell, script)
		if !sort.StringsAreSorted(got) {
			t.Errorf("%s offers an unsorted list: %v", shell, got)
		}
		// And the same input must produce the same script every time.
		again, err := completionScript(shell)
		if err != nil {
			t.Fatal(err)
		}
		if again != script {
			t.Errorf("%s script is not stable across calls", shell)
		}
	}
}

// The three names in the usage string are the contract; a fourth shell must not
// silently produce a bash script, and must not produce an empty one either --
// `eval "$(dsx completion tcsh)"` evaluating nothing at all is how a user ends
// up debugging their shell instead of their typo.
func TestAnUnknownShellIsAUsageErrorAndNoScript(t *testing.T) {
	t.Parallel()
	script, err := completionScript("powershell")
	if err == nil {
		t.Fatalf("an unknown shell produced a script:\n%s", script)
	}
	if script != "" {
		t.Errorf("a rejected shell still returned %d bytes of script", len(script))
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("unknown shell classified %q, want %q", got, dsxerr.KindUsage)
	}
	if got := dsxerr.ExitCodeFor(err); got != dsxerr.ExitUsage {
		t.Errorf("unknown shell exit = %d, want %d — retrying it cannot help", got, dsxerr.ExitUsage)
	}
}

func TestCompletionWithNoShellNamesTheOnesItHas(t *testing.T) {
	t.Parallel()
	err := cmdCompletion(nil)
	if err == nil {
		t.Fatal("completion with no argument succeeded")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("missing argument classified %q, want %q", got, dsxerr.KindUsage)
	}
	for _, shell := range diagShells {
		if !strings.Contains(err.Error(), shell) {
			t.Errorf("the usage message does not offer %q: %q", shell, err.Error())
		}
	}
}

func TestCmdCompletionPrintsTheScriptItselfSoItCanBeEvalled(t *testing.T) {
	want, err := completionScript("zsh")
	if err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return cmdCompletion([]string{"zsh"}) })
	if err != nil {
		t.Fatalf("cmdCompletion(zsh): %v", err)
	}
	// Byte-for-byte: the output is eval'd, so anything added around it is
	// executed by the user's shell.
	if out != want {
		t.Errorf("printed script differs from completionScript(zsh)\n got %q\nwant %q", out, want)
	}
}

func TestCmdCompletionRejectsAnUnknownShellWithoutPrinting(t *testing.T) {
	// Printing a partial or bash-shaped script before returning the error would
	// leave the shell evaluating it anyway.
	out, err := captureStdout(t, func() error { return cmdCompletion([]string{"tcsh"}) })
	if err == nil {
		t.Fatal("an unknown shell was accepted")
	}
	if out != "" {
		t.Errorf("a rejected shell still printed:\n%s", out)
	}
}

// The completion list and `usage` must stay in step: a command a user can Tab
// to but cannot read about is a command with no documentation.
//
// This asserts one direction only -- commandNames ⊆ usage. The reverse does not
// hold today and is reported separately rather than papered over here.
func TestUsageDocumentsEveryCompletableCommand(t *testing.T) {
	t.Parallel()
	for _, n := range commandNames {
		if !strings.Contains(usage, "dsx "+n) {
			t.Errorf("command %q is completable but undocumented in `dsx help`", n)
		}
	}
}
