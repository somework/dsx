package syncer

import (
	"strings"
	"testing"
)

// TestOutcomeUnverifiedOnlyDoesNotClaimDivergence is the machine-facing twin
// of TestAStaleBaselineConflictDoesNotClaimDivergence: every existing
// .Outcome() test builds a report from tracked, binary or prune conflicts,
// never from Unverified alone, so nothing proved the except() call that
// keeps Unverified out of the generic "local differs"/"server moved ahead"
// clause is load-bearing rather than decorative.
func TestOutcomeUnverifiedOnlyDoesNotClaimDivergence(t *testing.T) {
	t.Run("pull", func(t *testing.T) {
		r := PullReport{Conflicts: []string{"a.css"}, Unverified: []string{"a.css"}}
		err := r.Outcome(false)
		if err == nil {
			t.Fatal("an unverified collision reported success")
		}
		msg := err.Error()
		if strings.Contains(msg, "local differs") {
			t.Errorf("claims a divergence nothing proved: %q", msg)
		}
		if !strings.Contains(msg, "dsx fetch") {
			t.Errorf("does not name the way to check without writing: %q", msg)
		}
	})

	t.Run("push", func(t *testing.T) {
		r := PushReport{Conflicts: []string{"a.css"}, Unverified: []string{"a.css"}}
		err := r.Outcome(false)
		if err == nil {
			t.Fatal("an unverified collision reported success")
		}
		msg := err.Error()
		if strings.Contains(msg, "server moved ahead") {
			t.Errorf("claims a divergence nothing proved: %q", msg)
		}
		if !strings.Contains(msg, "dsx fetch") {
			t.Errorf("does not name the way to check without writing: %q", msg)
		}
	})
}

// TestOutcomeDivergedOnlyStatesTheDivergence: the Diverged sibling of the
// above — a proven divergence, so the machine hint must say so and must not
// fall back to "dsx fetch" (already run) or the false "never verified".
func TestOutcomeDivergedOnlyStatesTheDivergence(t *testing.T) {
	t.Run("pull", func(t *testing.T) {
		r := PullReport{Conflicts: []string{"a.css"}, Diverged: []string{"a.css"}}
		err := r.Outcome(false)
		if err == nil {
			t.Fatal("a proven divergence reported success")
		}
		msg := err.Error()
		if strings.Contains(msg, "never verified") {
			t.Errorf("claims dsx never checked a proven divergence: %q", msg)
		}
		if !strings.Contains(msg, "dsx fetch") || !strings.Contains(msg, "--force") {
			t.Errorf("does not state the divergence was confirmed by fetch and name --force: %q", msg)
		}
	})

	t.Run("push", func(t *testing.T) {
		r := PushReport{Conflicts: []string{"a.css"}, Diverged: []string{"a.css"}}
		err := r.Outcome(false)
		if err == nil {
			t.Fatal("a proven divergence reported success")
		}
		msg := err.Error()
		if strings.Contains(msg, "never verified") {
			t.Errorf("claims dsx never checked a proven divergence: %q", msg)
		}
		if !strings.Contains(msg, "dsx fetch") || !strings.Contains(msg, "--force") {
			t.Errorf("does not state the divergence was confirmed by fetch and name --force: %q", msg)
		}
	})
}
