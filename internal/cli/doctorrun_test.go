package cli

import (
	"encoding/json"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// TestDoctorRunsThroughTheEntrypointWhenAuthIsBroken drives the real dispatch
// path (run, not runDoctor) with no credentials anywhere. doctor exists to
// diagnose exactly this state, so it must emit its own doctorReport — with the
// credentials check marked failed — instead of aborting with the generic auth
// envelope before it ever runs.
func TestDoctorRunsThroughTheEntrypointWhenAuthIsBroken(t *testing.T) {
	diagPinCredentialStore(t)                            // empty store: no login anywhere
	t.Setenv("DSX_TOKEN", "")                            // and no env token
	t.Setenv("DSX_ENDPOINT", diagHealthyServer(t).URL()) // keep the endpoint check off the real network

	out, err := maincliRun(t, "doctor", "--json")

	// A failed check makes doctor exit nonzero — expected and fine.
	if err == nil {
		t.Fatalf("doctor with no login reported success: %q", out)
	}
	// The DEFECT: run() aborted with the generic auth envelope before doctor
	// ran, so nothing reached stdout.
	if got := maincliKind(t, err); got == dsxerr.KindAuth && out == "" {
		t.Fatalf("run aborted with the generic auth envelope before doctor ran (kind=%q, out=%q)", got, out)
	}

	var rep doctorReport
	if e := json.Unmarshal([]byte(out), &rep); e != nil {
		t.Fatalf("doctor --json did not emit a parseable doctorReport: %v\n%s", e, out)
	}
	if len(rep.Checks) == 0 {
		t.Fatalf("doctorReport carried no checks: %q", out)
	}
	cc := diagCheckNamed(t, rep, "credentials")
	if cc.Status != checkFail {
		t.Fatalf("credentials check = %q, want %q when no login exists", cc.Status, checkFail)
	}
	if rep.OK {
		t.Errorf("report OK=true despite a failed credentials check: %q", out)
	}
}
