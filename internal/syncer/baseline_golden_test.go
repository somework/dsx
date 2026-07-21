package syncer

import (
	"encoding/json"
	"os"
	"testing"
)

// goldenBaseline is hand-written on purpose, for the reason goldenLedger is:
// baseline.json is a compatibility contract too, and a golden regenerated from
// the structs would only prove the code equals itself.
const goldenBaseline = `{
  "project_id": "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
  "endpoint": "https://claude.ai/api/organizations/o/mcp",
  "verified": {
    "README.md": {
      "etag": "W/\"v1-abc\"",
      "size": 42,
      "sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
    }
  }
}
`

func goldenBaselineValue() Baseline {
	return Baseline{
		ProjectID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		Endpoint:  "https://claude.ai/api/organizations/o/mcp",
		Verified: map[string]BaselineEntry{
			"README.md": {
				Etag: `W/"v1-abc"`,
				Size: 42,
				SHA:  "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			},
		},
	}
}

// loadBaseline's nil-map fixup turns a decode miss into an empty map rather
// than an error, so the entry count below is what a renamed key trips on.
func TestBaselineGoldenDecodes(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(StateDir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(BaselinePath(dir), []byte(goldenBaseline), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := loadBaseline(dir)
	if err != nil {
		t.Fatalf("loadBaseline: %v", err)
	}

	want := goldenBaselineValue()
	if got.ProjectID != want.ProjectID {
		t.Errorf("ProjectID = %q, want %q", got.ProjectID, want.ProjectID)
	}
	if got.Endpoint != want.Endpoint {
		t.Errorf("Endpoint = %q, want %q", got.Endpoint, want.Endpoint)
	}
	if len(got.Verified) != len(want.Verified) {
		t.Fatalf("Verified has %d entries, want %d: %#v", len(got.Verified), len(want.Verified), got.Verified)
	}
	for path, we := range want.Verified {
		ge, ok := got.Verified[path]
		if !ok {
			t.Errorf("Verified[%q] missing", path)
			continue
		}
		if ge != we {
			t.Errorf("Verified[%q] = %#v, want %#v", path, ge, we)
		}
	}
}

func TestBaselineGoldenRoundTripsByteExact(t *testing.T) {
	dir := t.TempDir()
	if err := goldenBaselineValue().save(dir); err != nil {
		t.Fatalf("save: %v", err)
	}

	b, err := os.ReadFile(BaselinePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != goldenBaseline {
		t.Errorf("baseline bytes drifted.\n--- got ---\n%s\n--- want ---\n%s", b, goldenBaseline)
	}
}

func TestBaselineVerifiedKeyIsNeverOmittedWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	b := Baseline{ProjectID: "p", Verified: map[string]BaselineEntry{}}
	if err := b.save(dir); err != nil {
		t.Fatalf("save: %v", err)
	}

	raw, err := os.ReadFile(BaselinePath(dir))
	if err != nil {
		t.Fatal(err)
	}

	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	v, ok := generic["verified"]
	if !ok {
		t.Fatal(`"verified" is missing from an empty baseline; the tag must not carry omitempty`)
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf(`"verified" = %#v (%T), want an empty object, not null`, v, v)
	}
	if len(m) != 0 {
		t.Errorf(`"verified" = %#v, want empty`, m)
	}
}
