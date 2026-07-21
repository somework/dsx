package synccmd

import (
	"context"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/syncer"
)

// bindingModes are the verbs whose normal run writes State.ProjectID, so the
// no-ledger refusal's "once and it is remembered" is true of them.
var bindingModes = map[string]bool{"pull": true, "push": true}

// TestFetchDoesNotRememberTheProjectItWasGiven pins the mechanism the message
// test below depends on: fetch is the most tempting of the three to believe,
// because it does write to .dsx/ — but it writes baseline.json, and
// boundProject reads state.json.
func TestFetchDoesNotRememberTheProjectItWasGiven(t *testing.T) {
	dir := t.TempDir()
	body := []byte("hello\n")
	maincliWriteFile(t, dir, "a.css", string(body))

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(fileEntry("a.css", "e1", int64(len(body))))}
		}
		p, _ := args["path"].(string)
		return fakeReply{Text: envelopeFor(p, "e1", string(body))}
	})

	if _, err := captureStdout(t, func() error {
		return cmdFetch(context.Background(), fakeClient(f), []string{"proj-A", dir})
	}); err != nil {
		t.Fatalf("the naming run failed, so the remembering half proves nothing: %v", err)
	}

	_, err := captureStdout(t, func() error {
		return cmdFetch(context.Background(), fakeClient(f), []string{dir})
	})
	if err == nil {
		t.Fatal("fetch remembered the project — if this ever passes, the refusal's " +
			"promise came true and the message test below is the one to delete")
	}
	if !strings.Contains(err.Error(), "carries no dsx ledger") {
		t.Errorf("second run failed for another reason: %v", err)
	}
}

// TestPullDoesRememberTheProjectItWasGiven is the positive control for the
// test above: without it, a refusal that never resolved anything for any verb
// would pass it just as well.
func TestPullDoesRememberTheProjectItWasGiven(t *testing.T) {
	dir := t.TempDir()
	body := []byte("hello\n")

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name == "list_files" {
			return fakeReply{Text: listingFor(fileEntry("a.css", "e1", int64(len(body))))}
		}
		p, _ := args["path"].(string)
		return fakeReply{Text: envelopeFor(p, "e1", string(body))}
	})

	if _, err := captureStdout(t, func() error {
		return cmdSync(context.Background(), fakeClient(f), "pull", []string{"proj-A", dir})
	}); err != nil {
		t.Fatalf("the naming run failed: %v", err)
	}
	if _, err := captureStdout(t, func() error {
		return cmdSync(context.Background(), fakeClient(f), "pull", []string{dir})
	}); err != nil {
		t.Fatalf("pull did not remember the project it was given: %v", err)
	}
}

// TestTheNoLedgerRefusalPromisesMemoryOnlyWhereItIsKept: the refusal tells the
// caller to re-run the same verb with a project and promises "it is
// remembered". That was true when the resolver served pull and push alone.
// status forces DryRun and returns before the ledger is written; fetch writes
// .dsx/baseline.json, which boundProject never reads; diff writes nothing at
// all — all three were wired to this resolver later and inherited a promise
// they do not keep. A verb that does not remember must not claim to, and must
// name the one that does.
func TestTheNoLedgerRefusalPromisesMemoryOnlyWhereItIsKept(t *testing.T) {
	const promise = "it is remembered"

	for _, mode := range []string{"pull", "push", "status", "fetch", "diff"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			_, _, err := resolveSyncTarget(mode, []string{dir}, boundProject)
			if err == nil {
				t.Fatal("an unbound directory resolved without a project")
			}
			msg := err.Error()

			if bindingModes[mode] {
				if !strings.Contains(msg, promise) {
					t.Errorf("%s does remember, but its refusal no longer says so — "+
						"the one-command path is now undiscoverable: %q", mode, msg)
				}
				return
			}
			if strings.Contains(msg, promise) {
				t.Errorf("%s promises memory it does not keep; run it and the next run "+
					"repeats this refusal verbatim: %q", mode, msg)
			}
			if !strings.Contains(msg, "pin") {
				t.Errorf("%s does not remember and does not name the verb that does, "+
					"so the refusal leaves no way forward: %q", mode, msg)
			}
		})
	}
}

// TestEveryNonBindingModeIsReallyNonBinding keeps bindingModes honest: it is a
// hand-written claim about syncer, and a verb that started writing the ledger
// would leave the table above asserting the opposite of the truth.
func TestEveryNonBindingModeIsReallyNonBinding(t *testing.T) {
	dir := t.TempDir()
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: listingFor()}
	})

	for _, mode := range []string{"status", "diff"} {
		t.Run(mode, func(t *testing.T) {
			sub := t.TempDir()
			var err error
			if mode == "status" {
				_, err = captureStdout(t, func() error {
					return cmdSync(context.Background(), fakeClient(f), "status", []string{"proj-A", sub})
				})
			} else {
				_, err = captureStdout(t, func() error {
					return cmdDiff(context.Background(), fakeClient(f), []string{"proj-A", sub})
				})
			}
			if err != nil {
				t.Fatalf("%s failed, so its non-binding claim is untested: %v", mode, err)
			}
			if st, lErr := syncer.LoadState(sub); lErr != nil {
				t.Fatalf("LoadState: %v", lErr)
			} else if st.ProjectID != "" {
				t.Errorf("%s bound the directory to %q — it is a binding mode now, "+
					"so bindingModes and the refusal must both learn it", mode, st.ProjectID)
			}
		})
	}
	_ = dir
}
