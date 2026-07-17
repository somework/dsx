package syncer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The ledger's on-disk shape is a compatibility contract with every .dsx-state.json
// already sitting in a user's directory, and nothing else in the suite pins it.
// LoadState and save agree with each other by construction, so a renamed json tag
// stays green through both -- while in the field it decodes to a zero SHA, which
// plan.go reads as localDirty for every tracked file: every path a conflict, exit 3,
// and the user pushed toward --force. That is invariant 5's named failure mode
// arriving as a silent format change.
//
// These bytes are the contract. They are written by hand, not generated from the
// structs, because a fixture regenerated from the code under test only proves the
// code equals itself.
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
			// A binary entry is tracked but was never on disk: no bytes, so Size and
			// SHA stay zero and must still serialise, or invariant 4 loses the marker
			// that tells prune this absence is not a deletion.
			"assets/logo.png": {
				Etag:   `W/"v2-def"`,
				Binary: true,
			},
		},
	}
}

// TestLedgerGoldenDecodes pins the field names a real .dsx-state.json carries.
// Rename any json tag on FileState or State and this goes red.
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

// TestLedgerGoldenRoundTripsByteExact is the half that catches a tag rename even
// when both sides of the rename agree: the bytes we write must equal the bytes a
// previous version wrote.
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

// TestLedgerOmitemptyContract pins which fields vanish when zero. endpoint and
// binary are omitempty; size and sha256 are not, and must not become so: a binary
// entry's zero Size is meaningful, not absent.
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

// TestLedgerUnknownFieldsSurvive: a ledger written by a newer dsx must not be
// rejected by an older one. encoding/json ignores unknown fields by default; this
// pins that we never turn that off (e.g. via DisallowUnknownFields), which would
// make a downgrade corrupt-looking rather than merely lossy.
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
