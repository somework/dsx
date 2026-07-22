package syncer

import (
	"encoding/json"
	"strings"
	"testing"
)

const hintNeedle = "dsx files cat"

// The only route out of a conflict was `dsx files cat <project> <path>`, and the id
// had long left the screen. Commit 6 dropped the id; this names the route.
func TestAPlainPullConflictNamesTheWayToLook(t *testing.T) {
	out := PullReport{Conflicts: []string{"tokens.css"}}.Render(false)
	if !strings.Contains(out, hintNeedle) {
		t.Errorf("a plain conflict does not say how to see the server's copy:\n%s", out)
	}
	if !strings.Contains(out, "local differs; --force to overwrite") {
		t.Errorf("the per-line verdict changed:\n%s", out)
	}
}

// The hint must make no claim about --force. rep.Conflicts is a merged list,
// and for the destructive rungs --force DELETES rather than overwrites.
func TestTheHintMakesNoClaimAboutForce(t *testing.T) {
	out := PullReport{Conflicts: []string{"tokens.css"}}.Render(false)
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, hintNeedle) {
			continue
		}
		if strings.Contains(line, "--force") {
			t.Errorf("the hint line talks about --force:\n%s", line)
		}
	}
}

// cat cannot fetch a binary, and a path gone from the server has no copy to
// fetch. Offering the route on those rungs would be a lie.
func TestNoHintWhenEveryConflictIsDestructive(t *testing.T) {
	for _, r := range []PullReport{
		{Conflicts: []string{"a.png"}, PruneBinary: []string{"a.png"}},
		{Conflicts: []string{"gone.css"}, PruneConflicts: []string{"gone.css"}},
		{Conflicts: []string{"a.png", "gone.css"},
			PruneBinary: []string{"a.png"}, PruneConflicts: []string{"gone.css"}},
	} {
		out := r.Render(false)
		if strings.Contains(out, hintNeedle) {
			t.Errorf("hint offered where cat cannot help:\n%s", out)
		}
	}
}

// A mixed report still earns the hint, for the plain rung it does contain.
func TestAMixedReportStillEarnsTheHint(t *testing.T) {
	out := PullReport{
		Conflicts:      []string{"gone.css", "tokens.css"},
		PruneConflicts: []string{"gone.css"},
	}.Render(false)
	if !strings.Contains(out, hintNeedle) {
		t.Errorf("a report holding one plain conflict lost the hint:\n%s", out)
	}
	if !strings.Contains(out, "--force would DELETE your only copy") {
		t.Errorf("the destructive verdict changed:\n%s", out)
	}
}

func TestNoHintWhenThereAreNoConflicts(t *testing.T) {
	if out := (PullReport{}).Render(false); strings.Contains(out, hintNeedle) {
		t.Errorf("hint printed with no conflicts:\n%s", out)
	}
}

// The machine surface must not move: the hint is prose only.
func TestTheHintIsAbsentFromJSON(t *testing.T) {
	out := PullReport{Conflicts: []string{"tokens.css"}}.Render(true)
	if strings.Contains(out, hintNeedle) {
		t.Errorf("the hint leaked into --json:\n%s", out)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("--json is not one document: %v", err)
	}
	if _, ok := m["hint"]; ok {
		t.Errorf("--json grew a hint key: %s", out)
	}
}

// push carries rungs cat cannot serve either.
func TestAPlainPushConflictNamesTheWayToLook(t *testing.T) {
	out := PushReport{Conflicts: []string{"tokens.css"}}.Render(false)
	if !strings.Contains(out, hintNeedle) {
		t.Errorf("a plain push conflict does not say how to look:\n%s", out)
	}
}

func TestNoPushHintWhenEveryConflictIsBinaryOrGone(t *testing.T) {
	for _, r := range []PushReport{
		{Conflicts: []string{"a.png"}, BinaryConflicts: []string{"a.png"}},
		{Conflicts: []string{"a.png"}, BinaryGone: []string{"a.png"}},
		{Conflicts: []string{"gone.css"}, PruneConflicts: []string{"gone.css"}},
	} {
		if out := r.Render(false); strings.Contains(out, hintNeedle) {
			t.Errorf("push hint offered where cat cannot help:\n%s", out)
		}
	}
}

// The hint has to be true wherever it is read: conflict paths are relative to
// the synced directory, which is not necessarily the shell's.
func TestTheHintSaysWhereToRunIt(t *testing.T) {
	out := PullReport{Conflicts: []string{"tokens.css"}}.Render(false)
	if !strings.Contains(out, "synced dir") {
		t.Errorf("the hint does not say where `dsx cat` resolves its project:\n%s", out)
	}
}

// One line however many conflicts there are.
func TestTheHintIsPrintedOnce(t *testing.T) {
	out := PullReport{Conflicts: []string{"a.css", "b.css", "c.css"}}.Render(false)
	if n := strings.Count(out, hintNeedle); n != 1 {
		t.Errorf("hint printed %d times, want 1:\n%s", n, out)
	}
}
