package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/somework/dsx/internal/auth"
	"github.com/somework/dsx/internal/dsxerr"
)

// TestRunDispatchesAuthWithoutLoadingTheStoredToken drives `dsx auth` through
// the real entrypoint (run, not cmdAuth directly) with an EXPIRED token
// sitting in the credential store and no DSX_TOKEN override.
//
// auth.LoadToken — the path every NeedClient command takes — refuses an
// expired token outright (auth.go's tokenFrom checks ExpiresAt). But
// auth.TokenInfo, which cmdAuth calls internally, does not check expiry: a
// stale token's expiry is exactly the fact `dsx auth` exists to surface, not
// a reason to fail. So this only succeeds if run() took the NeedAuth branch
// (dispatch with a nil client, cli.go ~49-51) and never called LoadToken; if
// it fell through to the generic NeedClient path, LoadToken's expiry check
// would abort before cmdAuth ever ran.
func TestRunDispatchesAuthWithoutLoadingTheStoredToken(t *testing.T) {
	dir := diagPinCredentialStore(t)
	t.Setenv("DSX_TOKEN", "")
	writeCreds(t, dir, auth.Creds{
		AccessToken: "sk-ant-oat01-expired",
		Scopes:      []string{"user:inference"},
		ExpiresAt:   time.Now().Add(-time.Hour).UnixMilli(),
	})

	out, err := maincliRun(t, "auth")
	if err != nil {
		t.Fatalf("dsx auth with an expired stored token failed via run(): %v — LoadToken's expiry check must not run for NeedAuth commands", err)
	}
	if !strings.Contains(out, "expires") || !strings.Contains(out, "user:inference") {
		t.Errorf("stdout = %q, want the scopes and expiry auth exists to report", out)
	}
}

// TestRunLoadsTheTokenAndDispatchesForANeedClientCommand drives a plain
// NeedClient command ("projects") through run() with a valid credential and
// DSX_ENDPOINT pointed at a fake MCP server. It proves run()'s default branch
// (cli.go ~64) actually calls auth.LoadToken and builds a real client from
// it — dispatch must reach the fake endpoint.
//
// It also proves the LOADED token value itself threads onto the wire: the
// fake records the incoming Authorization header per request, and this test
// asserts it is exactly "Bearer test-token" — the DSX_TOKEN value run()
// loaded — not just that dispatch reached the fake with any client. A
// regression like mcp.New("") in place of mcp.New(token) would still reach
// the fake (same canned reply) but would fail this header check.
func TestRunLoadsTheTokenAndDispatchesForANeedClientCommand(t *testing.T) {
	diagPinCredentialStore(t)
	t.Setenv("DSX_TOKEN", "test-token")
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: `{"projects":[]}`}
	})
	t.Setenv("DSX_ENDPOINT", f.URL())

	out, err := maincliRun(t, "projects")
	if err != nil {
		t.Fatalf("dsx projects via run(): %v", err)
	}
	if n := f.CountTool("list_projects"); n != 1 {
		t.Fatalf("run() never reached the fake endpoint: calls=%v", f.Recorded())
	}
	call := syncFirstCall(t, f, "list_projects")
	if want := "Bearer test-token"; call.Authorization != want {
		t.Errorf("Authorization header = %q, want %q — the token run() loaded via auth.LoadToken must reach mcp.New and flow onto the wire", call.Authorization, want)
	}
	if !strings.Contains(out, "projects") {
		t.Errorf("stdout = %q, want the tool's reply", out)
	}
}

// TestRunAbortsANeedClientCommandWhenAuthFails is the mirror of
// TestDoctorRunsThroughTheEntrypointWhenAuthIsBroken (doctorrun_test.go)
// under identical no-credential conditions, but for a plain NeedClient
// command instead of doctor's NeedOptionalClient. Where doctor still emits
// its report, a NeedClient command must abort with the generic auth
// envelope — cli.go's `token, err := auth.LoadToken(); if err != nil {
// return err }` (~53-63) — before dispatch ever runs, so the fake endpoint
// must see no calls at all and stdout must stay empty.
func TestRunAbortsANeedClientCommandWhenAuthFails(t *testing.T) {
	diagPinCredentialStore(t) // empty store: no login anywhere
	t.Setenv("DSX_TOKEN", "") // and no env token
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		t.Errorf("run() dispatched %q despite no credentials; a NeedClient command must abort before contacting the endpoint", name)
		return fakeReply{}
	})
	t.Setenv("DSX_ENDPOINT", f.URL())

	out, err := maincliRun(t, "projects")
	if got := maincliKind(t, err); got != dsxerr.KindAuth {
		t.Fatalf("kind = %q, want %q", got, dsxerr.KindAuth)
	}
	if out != "" {
		t.Errorf("stdout = %q, want nothing: run() must abort before dispatch", out)
	}
}
