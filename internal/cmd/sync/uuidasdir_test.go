package synccmd

import (
	"strings"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

const sampleProjectID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

func noLedger(string) (string, error) { return "", nil }

// A single positional is <dir>, so `dsx pull <uuid>` looks for a ledger in a
// directory named after the project. The refusal then told the reader to run
// `dsx pull <project> <uuid>` — i.e. to create a directory named after the
// project id. That advice built the population of UUID-named directories.
func TestASoleProjectIDPositionalIsNotAdvisedAsADirectory(t *testing.T) {
	_, _, err := resolveSyncTarget("pull", []string{sampleProjectID}, noLedger)
	if err == nil {
		t.Fatal("a lone project id was accepted as a directory")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
	msg := err.Error()
	if strings.Contains(msg, sampleProjectID+" "+sampleProjectID) {
		t.Errorf("refusal advises creating a directory named after the project:\n%s", msg)
	}
	if !strings.Contains(msg, "<dir>") {
		t.Errorf("refusal does not show where the directory goes:\n%s", msg)
	}
}

// The ordinary case keeps its wording: a real directory that carries no ledger.
func TestAnUnboundDirectoryKeepsItsOwnRefusal(t *testing.T) {
	_, _, err := resolveSyncTarget("pull", []string{"design"}, noLedger)
	if err == nil {
		t.Fatal("an unbound directory was accepted")
	}
	if !strings.Contains(err.Error(), "carries no dsx ledger") {
		t.Errorf("the plain unbound-directory refusal changed:\n%s", err)
	}
}

// Both branches refuse; neither rebinds a positional. A wrong guess about the
// id's shape costs the reader the old message, never a different action.
func TestBothRefusalsAreRefusals(t *testing.T) {
	for _, pos := range []string{sampleProjectID, "design"} {
		project, dir, err := resolveSyncTarget("push", []string{pos}, noLedger)
		if err == nil {
			t.Fatalf("%q was accepted", pos)
		}
		if project != "" || dir != "" {
			t.Errorf("%q resolved to project=%q dir=%q; a refusal must resolve nothing", pos, project, dir)
		}
	}
}

// A directory bound to a project still works, whatever it is named — including
// the UUID-named directories the old advice produced.
func TestAUUIDNamedDirectoryThatIsBoundStillResolves(t *testing.T) {
	bound := func(string) (string, error) { return "proj-A", nil }
	project, dir, err := resolveSyncTarget("pull", []string{sampleProjectID}, bound)
	if err != nil {
		t.Fatalf("a bound UUID-named directory was refused: %v", err)
	}
	if project != "proj-A" || dir != sampleProjectID {
		t.Errorf("project=%q dir=%q, want proj-A and the directory", project, dir)
	}
}

func TestLooksLikeProjectID(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{sampleProjectID, true},
		{strings.ToUpper(sampleProjectID), true},
		{"design", false},
		{"design-system", false},
		{"aaaaaaaa-d4dc-4bf2-b06b-ba9358a234b", false},
		{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaax", false},
		{"aaaaaaaaaaaa4aaa8aaaaaaaaaaaaaaa", false},
		{"zzzzzzzz-d4dc-4bf2-b06b-ba9358a234b0", false},
		{"", false},
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := looksLikeProjectID(tc.in); got != tc.want {
				t.Errorf("looksLikeProjectID(%q)=%v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
