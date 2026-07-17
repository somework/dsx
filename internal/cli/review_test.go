package cli

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

// The half of the adversarial review that is about the CLI surface rather
// than the sync decisions. Its siblings moved to internal/syncer with the
// code they guard; the preamble there explains where all of them came from.

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

// TestPutSelfAuthorisesLikePushDoes moved to internal/cmd/files with cmdPut,
// which it drives directly.

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

func jsonUnmarshalString(s string, v any) error { return json.Unmarshal([]byte(s), v) }
