package syncer

import (
	"encoding/json"
	"testing"
)

// A zero-value report's JSON is the omitempty contract in both directions at
// once: a conflict-class slice that loses omitempty appears here, and an
// always-present field that gains one disappears. Hand-written, not marshalled
// from the same structs — a golden regenerated from them would only prove the
// code equals itself. Note the opposite contract on Baseline.Verified, which
// must stay present when empty (baseline_golden_test.go).
func TestReportOmitemptyContract(t *testing.T) {
	t.Run("pull", func(t *testing.T) {
		b, err := json.Marshal(PullReport{})
		if err != nil {
			t.Fatal(err)
		}
		const want = `{"fetched":null,"unchanged":0,"deleted":null,"verified":0,"conflicts":null,"binary":null,"bytes":0}`
		if string(b) != want {
			t.Errorf("omitempty contract drifted.\n--- got ---\n%s\n--- want ---\n%s", b, want)
		}
	})

	t.Run("push", func(t *testing.T) {
		b, err := json.Marshal(PushReport{})
		if err != nil {
			t.Fatal(err)
		}
		const want = `{"written":null,"unchanged":0,"deleted":null,"verified":0,"conflicts":null,"bytes":0}`
		if string(b) != want {
			t.Errorf("omitempty contract drifted.\n--- got ---\n%s\n--- want ---\n%s", b, want)
		}
	})

	// The paired positive control: every key omitted above must still appear
	// once its field is non-empty, or the assertions above would also pass
	// against a report that had dropped the fields entirely.
	t.Run("pull: the omitted keys appear when filled", func(t *testing.T) {
		b, err := json.Marshal(PullReport{
			Unverified:     []string{"u"},
			Diverged:       []string{"d"},
			StaleProof:     []string{"s"},
			PruneConflicts: []string{"pc"},
			PruneBinary:    []string{"pb"},
			Irregular:      []string{"i"},
			Incomplete:     true,
		})
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"unverified", "diverged", "stale_proof", "prune_conflicts", "prune_binary", "irregular", "incomplete"} {
			if _, ok := doc[k]; !ok {
				t.Errorf("key %q missing when its field is non-empty: %s", k, b)
			}
		}
	})

	t.Run("push: the omitted keys appear when filled", func(t *testing.T) {
		b, err := json.Marshal(PushReport{
			Unverified:       []string{"u"},
			Diverged:         []string{"d"},
			StaleProof:       []string{"s"},
			BinaryConflicts:  []string{"bc"},
			BinaryGone:       []string{"bg"},
			PruneConflicts:   []string{"pc"},
			LeaseBroken:      []string{"lb"},
			PruneLeaseBroken: []string{"plb"},
			Irregular:        []string{"i"},
			Incomplete:       true,
		})
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"unverified", "diverged", "stale_proof", "binary_conflicts", "binary_gone", "prune_conflicts", "lease_broken", "prune_lease_broken", "irregular", "incomplete"} {
			if _, ok := doc[k]; !ok {
				t.Errorf("key %q missing when its field is non-empty: %s", k, b)
			}
		}
	})
}
