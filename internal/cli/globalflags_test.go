package cli

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/dsxerr"
)

func TestSplitGlobalFlags(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		wantChdirs []string
		wantRest   []string
	}{
		{"no globals at all", []string{"pull", "--prune"}, nil, []string{"pull", "--prune"}},
		{"separated form", []string{"-C", "design", "pull"}, []string{"design"}, []string{"pull"}},
		{"joined form", []string{"-C=design", "pull"}, []string{"design"}, []string{"pull"}},
		{
			// git's own rule: each -C is interpreted relative to the preceding
			// one, which successive chdirs give for free.
			"repeated, applied in order",
			[]string{"-C", "a", "-C", "b", "status"},
			[]string{"a", "b"}, []string{"status"},
		},
		{"nothing after the globals", []string{"-C", "design"}, []string{"design"}, nil},
		{
			// The command's own flags are not dsx's: a -C after the verb belongs
			// to the verb, and peeling it here would silently change what the
			// FlagSet sees.
			"-C after the command name is left alone",
			[]string{"pull", "-C", "design"}, nil, []string{"pull", "-C", "design"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chdirs, rest, err := splitGlobalFlags(tc.args)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(chdirs, tc.wantChdirs) {
				t.Errorf("chdirs = %v, want %v", chdirs, tc.wantChdirs)
			}
			if !reflect.DeepEqual(rest, tc.wantRest) {
				t.Errorf("rest = %v, want %v", rest, tc.wantRest)
			}
		})
	}
}

// A bare -C with nothing after it must be a usage error, not a silent no-op:
// swallowing it would run the command in the wrong directory, which for
// `push --prune` is the whole tree.
func TestSplitGlobalFlagsRefusesABareC(t *testing.T) {
	_, _, err := splitGlobalFlags([]string{"-C"})
	if err == nil {
		t.Fatal("a -C with no directory was accepted")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
	if !strings.Contains(err.Error(), "-C") {
		t.Errorf("the refusal does not name the flag: %q", err)
	}
}

func TestSplitGlobalFlagsRefusesAnEmptyCValue(t *testing.T) {
	if _, _, err := splitGlobalFlags([]string{"-C=", "pull"}); err == nil {
		t.Fatal("-C= with an empty directory was accepted")
	}
}

// TestDashCActuallyMovesBeforeTheCommandRuns is the half splitGlobalFlags
// cannot prove: the peeling could be perfect and the process still run in the
// wrong tree. `version` is the cheapest command that needs no credential and
// no network, so what it does is irrelevant — where it does it is the point.
func TestDashCActuallyMovesBeforeTheCommandRuns(t *testing.T) {
	target := t.TempDir()
	t.Chdir(t.TempDir())

	saved := os.Args
	t.Cleanup(func() { os.Args = saved })
	os.Args = []string{"dsx", "-C", target, "version"}

	if err := run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	got, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if !sameDirCLI(t, got, target) {
		t.Errorf("ran in %q, want %q", got, target)
	}
}

func TestDashCRefusesADirectoryThatIsNotOne(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := applyChdirs([]string{"nope"}); err == nil {
		t.Fatal("-C accepted a directory that does not exist")
	} else if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
}

func sameDirCLI(t *testing.T, a, b string) bool {
	t.Helper()
	fa, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	fb, err := os.Stat(b)
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(fa, fb)
}

// TestTheGlobalBlockDocumentsExactlyTheRealGlobals keeps usage.go's GLOBAL
// block from becoming the one place a flag can be promised without anything
// checking it. footerFlagScopes skips that block precisely because a global has
// no per-command declaration to match, so this is the check that replaces the
// one it skips — and it is tied to the parser, not to the prose: every flag the
// block names must be one splitGlobalFlags actually consumes, and every flag it
// consumes must be named.
func TestTheGlobalBlockDocumentsExactlyTheRealGlobals(t *testing.T) {
	documented := map[string]bool{}
	inBlock := false
	for _, line := range strings.Split(usageFooter, "\n") {
		if strings.HasPrefix(line, "GLOBAL") {
			inBlock = true
			continue
		}
		if !inBlock {
			continue
		}
		if strings.TrimSpace(line) == "" {
			break
		}
		for _, f := range flagTokens(line) {
			documented["-"+f] = true
		}
	}
	if len(documented) == 0 {
		t.Fatal("the GLOBAL block names no flag at all, so the checks below prove nothing")
	}

	for f := range documented {
		if _, rest, err := splitGlobalFlags([]string{f, "x", "version"}); err != nil || len(rest) != 1 {
			t.Errorf("usage promises the global %s, but splitGlobalFlags does not consume it "+
				"(rest=%v, err=%v)", f, rest, err)
		}
	}
	if !documented["-C"] {
		t.Error("splitGlobalFlags consumes -C but the GLOBAL block does not name it")
	}
}
