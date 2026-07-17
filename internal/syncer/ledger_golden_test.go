package syncer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// goldenLedger is hand-written on purpose: the ledger's on-disk shape is a
// compatibility contract, and a golden regenerated from the structs would only
// prove the code equals itself — a renamed json tag would pass both sides.
const goldenLedger = `{
  "project_id": "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
  "endpoint": "https://claude.ai/api/organizations/o/mcp",
  "files": {
    "README.md": {
      "etag": "W/\"v1-abc\"",
      "size": 42,
      "sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
    },
    "assets/logo.png": {
      "etag": "W/\"v2-def\"",
      "size": 0,
      "sha256": "",
      "binary": true
    }
  }
}
`

func goldenState() State {
	return State{
		ProjectID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		Endpoint:  "https://claude.ai/api/organizations/o/mcp",
		Files: map[string]FileState{
			"README.md": {
				Etag: `W/"v1-abc"`,
				Size: 42,
				SHA:  "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			},

			"assets/logo.png": {
				Etag:   `W/"v2-def"`,
				Binary: true,
			},
		},
	}
}

func TestLedgerGoldenDecodes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, StateFileName), []byte(goldenLedger), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	want := goldenState()
	if got.ProjectID != want.ProjectID {
		t.Errorf("ProjectID = %q, want %q", got.ProjectID, want.ProjectID)
	}
	if got.Endpoint != want.Endpoint {
		t.Errorf("Endpoint = %q, want %q", got.Endpoint, want.Endpoint)
	}
	if len(got.Files) != len(want.Files) {
		t.Fatalf("Files has %d entries, want %d: %#v", len(got.Files), len(want.Files), got.Files)
	}
	for path, wf := range want.Files {
		gf, ok := got.Files[path]
		if !ok {
			t.Errorf("Files[%q] missing", path)
			continue
		}
		if gf != wf {
			t.Errorf("Files[%q] = %#v, want %#v", path, gf, wf)
		}
	}
}

func TestLedgerGoldenRoundTripsByteExact(t *testing.T) {
	dir := t.TempDir()
	if err := goldenState().save(dir); err != nil {
		t.Fatalf("save: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, StateFileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != goldenLedger {
		t.Errorf("ledger bytes drifted.\n--- got ---\n%s\n--- want ---\n%s", b, goldenLedger)
	}
}

func TestLedgerOmitemptyContract(t *testing.T) {
	b, err := json.MarshalIndent(State{
		ProjectID: "p",
		Files:     map[string]FileState{"a.txt": {Etag: "e"}},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	const want = `{
  "project_id": "p",
  "files": {
    "a.txt": {
      "etag": "e",
      "size": 0,
      "sha256": ""
    }
  }
}`
	if string(b) != want {
		t.Errorf("omitempty contract drifted.\n--- got ---\n%s\n--- want ---\n%s", b, want)
	}
}

func TestLedgerUnknownFieldsSurvive(t *testing.T) {
	dir := t.TempDir()
	const future = `{
  "project_id": "p",
  "files": {"a.txt": {"etag": "e", "size": 1, "sha256": "s", "mtime_ns": 123}},
  "schema_version": 9
}
`
	if err := os.WriteFile(filepath.Join(dir, StateFileName), []byte(future), 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState rejected a forward-compatible ledger: %v", err)
	}
	if got := st.Files["a.txt"].Etag; got != "e" {
		t.Errorf("Etag = %q, want %q", got, "e")
	}
}
