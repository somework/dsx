package synccmd

import (
	"os"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

const sampleProjectID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

func noLedger(string) (string, error) { return "", nil }

// uuidNamedDir makes a real directory called sampleProjectID inside a fresh
// cwd and returns the bare name. The name has to reach resolveSyncTarget
// unqualified: looksLikeProjectID measures the positional itself, so a
// t.TempDir()-prefixed path is 36 characters longer than the shape it tests.
// It must also exist, because the directory check now runs above the ledger
// read — an absent path is refused as a typo before the id-shaped refusal
// these tests are about is ever reached.
func uuidNamedDir(t *testing.T) string {
	t.Helper()
	t.Chdir(t.TempDir())
	if err := os.Mkdir(sampleProjectID, 0o755); err != nil {
		t.Fatal(err)
	}
	return sampleProjectID
}

// A single positional is <dir>, so `dsx pull <uuid>` looks for a ledger in a
// directory named after the project. The refusal then told the reader to run
// `dsx pull <project> <uuid>` — i.e. to create a directory named after the
// project id. That advice built the population of UUID-named directories.
func TestASoleProjectIDPositionalIsNotAdvisedAsADirectory(t *testing.T) {
	_, _, err := resolveSyncTarget("pull", []string{uuidNamedDir(t)}, noLedger)
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
	_, _, err := resolveSyncTarget("pull", []string{t.TempDir()}, noLedger)
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
	// Both directories are real: without that, each case would be refused for
	// not existing and this test would pass without reaching either branch it
	// names.
	for _, pos := range []string{uuidNamedDir(t), t.TempDir()} {
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
	project, dir, err := resolveSyncTarget("pull", []string{uuidNamedDir(t)}, bound)
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

// TestAMissingProjectIDPositionalIsNotAdvisedAsADirectoryEither is the same
// defect one layer up, found by running the binary rather than by a test.
// Hoisting the directory check above the ledger read made the missing-path
// refusal the one a lone project id reaches — and it advised
// `dsx clone <project> <uuid>`, i.e. creating a directory named after the
// project id, which is precisely the advice this file exists to prevent.
// The id-shaped positional almost never names a directory that exists, so
// this, not the sibling above, is the case a caller actually hits.
func TestAMissingProjectIDPositionalIsNotAdvisedAsADirectoryEither(t *testing.T) {
	t.Chdir(t.TempDir())

	_, _, err := resolveSyncTarget("status", []string{sampleProjectID}, noLedger)
	if err == nil {
		t.Fatal("a lone project id was accepted")
	}
	msg := err.Error()
	if strings.Contains(msg, sampleProjectID+"`") && strings.Contains(msg, "to create it") {
		t.Errorf("refusal advises creating a directory named after the project:\n%s", msg)
	}
	if !strings.Contains(msg, "looks like a project id") {
		t.Errorf("an id-shaped positional was reported as a plain missing path:\n%s", msg)
	}
	if !strings.Contains(msg, "<dir>") {
		t.Errorf("refusal does not show where the directory goes:\n%s", msg)
	}
}

// The positive control for the test above: an ordinary missing path must keep
// the plain refusal, or the id-shaped branch could swallow every typo.
func TestAnOrdinaryMissingPathKeepsThePlainRefusal(t *testing.T) {
	t.Chdir(t.TempDir())

	_, _, err := resolveSyncTarget("status", []string{"typo"}, noLedger)
	if err == nil {
		t.Fatal("a missing directory was accepted")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("the plain missing-directory refusal changed:\n%s", err)
	}
}
