package synccmd

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/syncer"
)

// TestOnlyTheOfflineSyncVerbsNeedNoCredential is the structural half of
// unpin's and status's reason to exist: a caller wanting out of a binding, or
// wanting to know what is on their disk, may be in exactly the state where
// auth fails, and dispatch aborts a NeedClient command before Run is ever
// called. Declared as data in Group, so only reading Group proves it — the
// cmdX signature (no *mcp.Client) cannot, since cmd.NoClient wraps it either
// way.
//
// The list is the whole point and must stay short: a verb earns a place here
// by making no network call at all, and every other sync verb does make one.
// Adding a name is a claim about that verb, not a way to silence this.
func TestOnlyTheOfflineSyncVerbsNeedNoCredential(t *testing.T) {
	offline := map[string]bool{"unpin": true, "status": true}

	seen := map[string]bool{}
	for _, c := range Group.Cmds {
		if !offline[c.Name] {
			if c.Needs == cmd.NeedNothing {
				t.Errorf("%s declares NeedNothing but is not in the offline set; "+
					"a sync verb that talks to the server must not", c.Name)
			}
			continue
		}
		seen[c.Name] = true
		if c.Needs != cmd.NeedNothing {
			t.Errorf("%s declares Needs=%v; dispatch aborts on an expired token before "+
				"Run is reached, so it would be unavailable in the state it exists for", c.Name, c.Needs)
		}
	}
	for name := range offline {
		if !seen[name] {
			t.Fatalf("%s is not registered in Group", name)
		}
	}
}

// TestUnpinWithNoPositionalDefaultsToTheCurrentDirectory covers cmdUnpin's
// case 0 branch, which extraargs_test.go's baseInvocations never reaches.
func TestUnpinWithNoPositionalDefaultsToTheCurrentDirectory(t *testing.T) {
	dir := t.TempDir()
	syncSeedState(t, dir, syncer.State{ProjectID: "proj-A"})
	t.Chdir(dir)

	if _, err := captureStdout(t, func() error {
		return cmdUnpin(nil)
	}); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	st, err := syncer.LoadState(".")
	if err != nil {
		t.Fatal(err)
	}
	if st.ProjectID != "" {
		t.Errorf("still bound to %q — dir did not default to the current directory", st.ProjectID)
	}
}

// TestUnpinRefusesAMissingDirectory: invariant 16 again — unpin makes no round
// trip, so nothing else would ever catch a typo in the one argument it takes.
func TestUnpinRefusesAMissingDirectory(t *testing.T) {
	missing := t.TempDir() + "/nope"

	_, err := captureStdout(t, func() error {
		return cmdUnpin([]string{missing})
	})
	if err == nil {
		t.Fatal("unpin accepted a directory that does not exist")
	}
	if got := dsxerr.Classify(err).Kind; got != dsxerr.KindUsage {
		t.Errorf("kind=%v, want %v", got, dsxerr.KindUsage)
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("refusal does not name the problem: %q", err)
	}
	if _, sErr := os.Lstat(missing); sErr == nil {
		t.Error("unpin created the directory it refused")
	}
}

func TestUnpinSupportsJSONOutput(t *testing.T) {
	dir := t.TempDir()
	syncSeedState(t, dir, syncer.State{ProjectID: "proj-A"})

	out, err := captureStdout(t, func() error {
		return cmdUnpin([]string{dir, "--json"})
	})
	if err != nil {
		t.Fatalf("unpin --json: %v", err)
	}
	var got struct {
		Dir string `json:"dir"`
	}
	if uErr := json.Unmarshal([]byte(out), &got); uErr != nil {
		t.Fatalf("unpin --json did not print one JSON document: %v\n%s", uErr, out)
	}
	if got.Dir != dir {
		t.Errorf("dir = %q, want %q", got.Dir, dir)
	}
}

// TestUnpinRefusesTooManyPositionals keeps cmdUnpin's default branch reachable
// in a test: extraargs_test.go proves the guard exists for every command, but
// not that this one names its own form.
func TestUnpinRefusesTooManyPositionals(t *testing.T) {
	err := cmdUnpin([]string{"a", "b"})
	if err == nil {
		t.Fatal("unpin accepted two positionals")
	}
	if !strings.Contains(err.Error(), unpinForm) {
		t.Errorf("usage error does not name unpin's form: %q", err)
	}
}
