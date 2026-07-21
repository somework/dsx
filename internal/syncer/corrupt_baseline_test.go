package syncer

import (
	"os"
	"testing"
)

// TestCorruptBaselineIsDiscardedNotFatal is loadBaseline's own unit-level
// guard: baseline.json is dsx's cache of what fetch downloaded, never a claim
// of ownership, so an unreadable, undecodable, or wrong-shaped file must load
// as an empty Baseline rather than surfacing a raw encoding/json error that
// names no file, carries no dsxerr Kind, and states no remedy. See
// TestACorruptBaselineDoesNotBlockASync for the same property proven through
// Pull/Push.
func TestCorruptBaselineIsDiscardedNotFatal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(t *testing.T, dir string)
	}{
		{"missing entirely", func(t *testing.T, dir string) {}},
		{"empty file", func(t *testing.T, dir string) {
			if err := os.MkdirAll(StateDir(dir), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(BaselinePath(dir), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"not json at all", func(t *testing.T, dir string) {
			if err := os.MkdirAll(StateDir(dir), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(BaselinePath(dir), []byte("not json at all"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"type mismatch", func(t *testing.T, dir string) {
			if err := os.MkdirAll(StateDir(dir), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(BaselinePath(dir), []byte(`{"verified": "not-a-map"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"truncated", func(t *testing.T, dir string) {
			if err := os.MkdirAll(StateDir(dir), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(BaselinePath(dir), []byte(`{"project_id": "p", "verif`), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.write(t, dir)

			bl, err := loadBaseline(dir)
			if err != nil {
				t.Fatalf("loadBaseline returned an error instead of discarding a broken cache: %v", err)
			}
			if bl.Verified == nil {
				t.Error("Verified is nil, want a fixed-up empty map")
			}
			if len(bl.Verified) != 0 {
				t.Errorf("Verified = %+v, want empty", bl.Verified)
			}
		})
	}
}
