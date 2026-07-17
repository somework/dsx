package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	"github.com/somework/dsx/internal/mcp"
	"github.com/somework/dsx/internal/mcptest"
)

func diagPinCredentialStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", dir)
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	if svc := auth.KeychainServiceName(os.LookupEnv, auth.HomeDir()); svc == "Claude Code-credentials" {
		t.Fatalf("credential store is not pinned: service resolved to the real item %q", svc)
	}
	return dir
}

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

func diagHealthyServer(t *testing.T) *fakeMCP {
	t.Helper()
	return newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		t.Errorf("doctor called tool %q; it must diagnose with read-only calls only", name)
		return fakeReply{}
	})
}

func TestExpiredTokenFailsAndNamesTheFix(t *testing.T) {
	t.Parallel()
	got := tokenExpiryCheck(time.Now().Add(-90 * time.Minute).UnixMilli())

	if got.Status != checkFail {
		t.Errorf("expired token reported %q, want %q — this is the whole reason to run doctor", got.Status, checkFail)
	}

	if !strings.Contains(got.Detail, "claude") {
		t.Errorf("detail does not tell the user to run claude: %q", got.Detail)
	}

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

func TestScopeCheckPassesWhenTheEnforcedScopeIsPresent(t *testing.T) {
	t.Parallel()
	got := scopeCheck([]string{"user:profile", enforcedScope, "user:inference"})
	if got.Status != checkOK {
		t.Fatalf("scopes %v reported %q, want %q", []string{enforcedScope}, got.Status, checkOK)
	}
}

func TestAnAbsentScopeWarnsAndNeverBlocksTheRun(t *testing.T) {
	t.Parallel()
	got := scopeCheck([]string{"user:inference", "user:profile"})

	if got.Status == checkFail {
		t.Fatalf("an unrecognised scope list was treated as fatal: %+v", got)
	}
	if got.Status != checkWarn {
		t.Fatalf("scopes reported %q, want %q", got.Status, checkWarn)
	}

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

	if !strings.Contains(got.Detail, "expiry") {
		t.Errorf("detail does not connect skew to the failure it causes: %q", got.Detail)
	}
}

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

func TestAnAbsentCredentialsFileIsNotAComplaint(t *testing.T) {
	t.Parallel()
	got := credentialsModeCheck(filepath.Join(t.TempDir(), "nothing-here.json"))
	if got.Status != checkOK {
		t.Fatalf("an absent file reported %q (%s), want %q", got.Status, got.Detail, checkOK)
	}
}

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

	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	got := credentialsModeCheck(p)
	if got.Status != checkWarn {
		t.Fatalf("an unreadable credentials path reported %q (%s), want %q",
			got.Status, got.Detail, checkWarn)
	}
}

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

func TestAWarnAloneKeepsTheReportOK(t *testing.T) {
	dir := diagPinCredentialStore(t)
	p := writeCreds(t, dir, diagHealthyCreds())
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}

	rep := runDoctor(context.Background(), fakeClient(diagHealthyServer(t)))

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
		ExpiresAt:   time.Now().Add(-time.Hour).UnixMilli(),
		Scopes:      []string{enforcedScope},
	})

	rep := runDoctor(context.Background(), fakeClient(diagHealthyServer(t)))

	if got := diagCheckNamed(t, rep, "token").Status; got != checkFail {
		t.Fatalf("expired token check = %q, want %q", got, checkFail)
	}
	if rep.OK {
		t.Errorf("a report containing a failure reported OK: %+v", rep.Checks)
	}
}

func TestAHealthyRunReportsTheEndpointItReachedAndWhatItSaw(t *testing.T) {
	dir := diagPinCredentialStore(t)
	writeCreds(t, dir, diagHealthyCreds())
	c := fakeClient(diagHealthyServer(t))

	rep := runDoctor(context.Background(), c)

	if !rep.OK {
		t.Fatalf("a healthy install reported not-OK: %+v", rep.Checks)
	}
	ep := diagCheckNamed(t, rep, "endpoint")
	if ep.Status != checkOK {
		t.Fatalf("endpoint check = %q (%s)", ep.Status, ep.Detail)
	}
	if !strings.Contains(ep.Detail, c.Endpoint()) {
		t.Errorf("endpoint detail does not name the URL it used: %q", ep.Detail)
	}

	if !strings.Contains(ep.Detail, "2 tools") {
		t.Errorf("endpoint detail does not report what the reply held: %q", ep.Detail)
	}

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

	c := diagRawServer(t, func(w http.ResponseWriter, r *http.Request) {
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

	for _, c := range rep.Checks {
		if c.Name == "clock" {
			t.Errorf("a clock verdict was reported without a reply to judge it by: %+v", c)
		}
	}
}

func TestNoLoginAnywhereFailsTheCredentialsCheck(t *testing.T) {
	diagPinCredentialStore(t)

	rep := runDoctor(context.Background(), fakeClient(diagHealthyServer(t)))

	if got := diagCheckNamed(t, rep, "credentials").Status; got != checkFail {
		t.Fatalf("credentials check = %q, want %q when no login exists", got, checkFail)
	}
	if rep.OK {
		t.Error("a run that found no login reported OK")
	}

	for _, c := range rep.Checks {
		if c.Name == "token" || c.Name == "scopes" {
			t.Errorf("%q was judged despite no credential being read: %+v", c.Name, c)
		}
	}
}

func TestDoctorNeverRendersTheToken(t *testing.T) {
	const secret = "sk-ant-oat01-DOCTOR-MUST-NEVER-PRINT-THIS"

	dir := diagPinCredentialStore(t)
	creds := diagHealthyCreds()
	creds.AccessToken = secret
	writeCreds(t, dir, creds)
	t.Setenv("DSX_TOKEN", secret+"-from-env")

	c := mcp.New(secret, mcp.WithEndpoint(diagHealthyServer(t).URL()))

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

func TestCmdDoctorFailsExactlyWhenTheReportIsNotOK(t *testing.T) {
	dir := diagPinCredentialStore(t)
	writeCreds(t, dir, diagHealthyCreds())
	healthy := fakeClient(diagHealthyServer(t))

	out, err := captureStdout(t, func() error { return cmdDoctor(context.Background(), healthy, nil) })
	if err != nil {
		t.Fatalf("a healthy install exited non-zero: %v\n%s", err, out)
	}
	if out == "" {
		t.Error("doctor printed nothing on a healthy install")
	}

	broken := diagRawServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	out, err = captureStdout(t, func() error { return cmdDoctor(context.Background(), broken, nil) })
	if err == nil {
		t.Fatalf("doctor found a problem and still exited 0:\n%s", out)
	}
	if got := dsxerr.ExitCodeFor(err); got != dsxerr.ExitFailure {
		t.Errorf("doctor exit = %d, want %d", got, dsxerr.ExitFailure)
	}

	if !strings.Contains(out, "endpoint") {
		t.Errorf("the failing report was not printed:\n%s", out)
	}
}

func TestCmdDoctorRejectsAnUnknownFlagAsUsage(t *testing.T) {
	err := cmdDoctor(context.Background(), fakeClient(diagHealthyServer(t)), []string{"--nope"})
	if err == nil {
		t.Fatal("an unknown flag was accepted")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("unknown flag classified %q, want %q", got, dsxerr.KindUsage)
	}
}

var (
	diagBashList = regexp.MustCompile(`compgen -W "([^"]*)"`)
	diagZshList  = regexp.MustCompile(`cmds=\(([^)]*)\)`)
	diagFishItem = regexp.MustCompile(`(?m)^complete -c dsx -n __fish_use_subcommand -a (\S+)$`)
)

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

func TestTheOfferedListIsSortedEvenWhenTheSourceListIsNot(t *testing.T) {
	saved := append([]string(nil), commandNames...)
	t.Cleanup(func() { commandNames = saved })

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

		again, err := completionScript(shell)
		if err != nil {
			t.Fatal(err)
		}
		if again != script {
			t.Errorf("%s script is not stable across calls", shell)
		}
	}
}

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

	if out != want {
		t.Errorf("printed script differs from completionScript(zsh)\n got %q\nwant %q", out, want)
	}
}

func TestCmdCompletionRejectsAnUnknownShellWithoutPrinting(t *testing.T) {
	out, err := captureStdout(t, func() error { return cmdCompletion([]string{"tcsh"}) })
	if err == nil {
		t.Fatal("an unknown shell was accepted")
	}
	if out != "" {
		t.Errorf("a rejected shell still printed:\n%s", out)
	}
}

func TestReplyWithoutADateLeavesLastServerDateZero(t *testing.T) {
	t.Parallel()
	c := diagRawServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header()["Date"] = nil
		mcptest.WriteJSON(w, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tools": []any{}}})
	})

	if _, err := c.ToolsList(context.Background()); err != nil {
		t.Fatalf("rpc: %v", err)
	}
	if got := c.LastServerDate(); got != 0 {
		t.Fatalf("LastServerDate = %d, want 0 for a reply carrying no Date", got)
	}

	if ch := clockCheck(c.LastServerDate(), time.Now()); ch.Status != checkWarn {
		t.Errorf("clock check = %+v, want a warn rather than an invented verdict", ch)
	}
}

func diagRawServer(t *testing.T, h http.HandlerFunc) *mcp.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return mcp.New("test-token", mcp.WithEndpoint(srv.URL))
}
