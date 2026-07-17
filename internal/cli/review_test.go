package cli

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

func TestUnclassifiedErrorRendersItsMessageOnce(t *testing.T) {
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
	t.Setenv("DSX_TOKEN", "sk-ant-oat01-SENTINEL")
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

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

func TestVersionHonoursJSON(t *testing.T) {
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

	prose, err := captureStdout(t, func() error { return cmdVersion(nil) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(prose, "dsx ") || strings.Count(strings.TrimSpace(prose), "\n") != 0 {
		t.Errorf("prose version = %q", prose)
	}
}

func jsonUnmarshalString(s string, v any) error { return json.Unmarshal([]byte(s), v) }
