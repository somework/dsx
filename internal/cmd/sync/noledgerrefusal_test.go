package synccmd

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/syncer"
)

// syncVerbs are the five that read their project from the ledger. clone and
// pin are deliberately absent: they are the two that write it.
var syncVerbs = []string{"pull", "push", "status", "fetch", "diff"}

// TestTheNoLedgerRefusalNamesOnlyFormsThatParse. The refusal used to tell the
// caller to re-run the same verb with a project in front, which was true for
// pull and push and a lie for the other three (fetch writes only
// .dsx/baseline.json, diff writes nothing, status forces DryRun and returns
// above the ledger write). It is now not merely a lie but unparseable for all
// five, so the whole class is gone and the refusal must name the two verbs
// that can actually bind a directory.
func TestTheNoLedgerRefusalNamesOnlyFormsThatParse(t *testing.T) {
	for _, mode := range syncVerbs {
		t.Run(mode, func(t *testing.T) {
			t.Chdir(t.TempDir())
			_, _, err := resolveSyncTarget(mode, nil, boundProject)
			if err == nil {
				t.Fatal("an unbound directory resolved without a project")
			}
			msg := err.Error()

			for _, want := range []string{"dsx pin <project>", "dsx clone <project>"} {
				if !strings.Contains(msg, want) {
					t.Errorf("refusal does not name %q, so it leaves no way forward: %q", want, msg)
				}
			}
			if strings.Contains(msg, "dsx "+mode+" <project>") {
				t.Errorf("refusal advises `dsx %s <project> …`, which no longer parses: %q", mode, msg)
			}
			if strings.Contains(msg, "dsx "+mode+" <dir>") {
				t.Errorf("refusal advises `dsx %s <dir>`, which no longer parses either: %q", mode, msg)
			}
		})
	}
}

// TestNoSyncVerbWritesTheBindingItReads is the premise the design rests on: if
// any of the five recorded a project, "only clone and pin name a project"
// would stop being the whole story and the refusal above would owe the reader
// a third repair. status and diff are driven end to end here; pull and push
// are excluded because writing the ledger is exactly their job, and fetch has
// its own test below for the tempting case — it does write under .dsx/, just
// not the ledger.
func TestNoSyncVerbWritesTheBindingItReads(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})

	for _, mode := range []string{"status", "diff"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			syncSeedState(t, dir, syncer.State{ProjectID: "proj-A"})
			t.Chdir(dir)

			var err error
			if mode == "status" {
				_, err = captureStdout(t, func() error {
					return cmdSync(context.Background(), fakeClient(f), "status", nil)
				})
			} else {
				_, err = captureStdout(t, func() error {
					return cmdDiff(context.Background(), fakeClient(f), nil)
				})
			}
			if err != nil {
				t.Fatalf("%s failed, so its claim is untested: %v", mode, err)
			}

			st, lErr := syncer.LoadState(dir)
			if lErr != nil {
				t.Fatal(lErr)
			}
			if len(st.Files) != 0 {
				t.Errorf("%s recorded %d tracked file(s); it is a binding verb now, and "+
					"invariant 4's \"untracked → not ours\" no longer holds for what it touched",
					mode, len(st.Files))
			}
		})
	}
}

// TestFetchWritesUnderDsxButNotTheLedger: fetch is the one verb where the
// claim above is easy to disbelieve, because it does create .dsx/ and write
// into it. What it writes is baseline.json, which boundProject never reads.
func TestFetchWritesUnderDsxButNotTheLedger(t *testing.T) {
	dir := t.TempDir()
	body := []byte("hello\n")
	maincliWriteFile(t, dir, "a.css", string(body))
	syncSeedState(t, dir, syncer.State{ProjectID: "proj-A"})
	t.Chdir(dir)

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(fileEntry("a.css", "e1", int64(len(body))))}
		}
		p, _ := args["path"].(string)
		return fakeReply{Text: envelopeFor(p, "e1", string(body))}
	})
	if _, err := captureStdout(t, func() error {
		return cmdFetch(context.Background(), fakeClient(f), nil)
	}); err != nil {
		t.Fatalf("fetch: %v", err)
	}

	// The positive control: without it, a fetch that did nothing at all would
	// satisfy the assertion below just as well. LedgerExists is the wrong probe
	// here — it stats StatePath — so the baseline is stat'd directly.
	if _, err := os.Stat(syncer.BaselinePath(dir)); err != nil {
		t.Fatalf("fetch wrote no baseline, so the ledger assertion below proves nothing: %v", err)
	}
	st, err := syncer.LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Files) != 0 {
		t.Errorf("fetch promoted %d path(s) into the ledger — invariant 17 says a proven "+
			"path stays untracked and is re-consulted from baseline.json every run", len(st.Files))
	}
}
