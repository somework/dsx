package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// jsonOf renders a fake's reply body. It panics rather than returning an error
// because a fixture that cannot marshal is a broken test, not a failing one.
func jsonOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// Report slices come out sorted, from Pull and Push, in every mode.
//
// Output width is a token budget: dsx exists so an agent can diff two runs and
// see only what changed. Identical state rendered in two orders is noise that
// costs the caller real context.
//
// The test this replaces hand-built a PullReport, called slices.Sort on it
// inside the test body, then asserted it was sorted -- it tested the stdlib and
// never called Pull. Measured: reversing every sort in pull.go left all 604
// tests green, which also means the suite could not have caught a bad
// sortStrings -> slices.Sort replacement. That refactor was verified by
// call-site accounting instead, precisely because the tests were no help.
//
// Both modes are asserted because the original defect was a mode split: the tail
// sort sits after the dry-run early return, and `status` is always a dry run, so
// a real pull emitted sorted output while status emitted raw map order for the
// same state. A test that drove only one mode would have passed.

// assertSorted names the field, because "not sorted" without one sends the
// reader to the wrong line.
func assertSorted(t *testing.T, field string, got []string) {
	t.Helper()
	if !slices.IsSorted(got) {
		t.Errorf("%s is not sorted: %v", field, got)
	}
}

// orderFake serves a listing whose map order cannot accidentally be sorted: the
// paths are chosen so that any single unsorted rendering is visible.
func orderFake(t *testing.T) *fakeMCP {
	t.Helper()
	return newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(
				fileEntry("zulu.css", "e1", 1),
				fileEntry("alpha.css", "e2", 1),
				fileEntry("mike.css", "e3", 1),
				fileEntry("bravo.css", "e4", 1),
			)}
		case "read_file":
			p, _ := args["path"].(string)
			return fakeReply{Text: envelopeFor(p, "e-new", "x")}
		case "finalize_plan":
			return fakeReply{Text: `{"plan_token":"tok"}`}
		}
		return fakeReply{Text: "[]"}
	})
}

func TestPullReportFieldsAreSortedInEveryMode(t *testing.T) {
	for _, dry := range []bool{true, false} {
		name := "real"
		if dry {
			name = "dry-run (status)"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			rep, err := Pull(context.Background(), fakeClient(orderFake(t)), PullOpts{
				ProjectID:   "p1",
				Dir:         dir,
				Concurrency: 4,
				DryRun:      dry,
			})
			if err != nil {
				t.Fatalf("Pull: %v", err)
			}
			if len(rep.Fetched) != 4 {
				t.Fatalf("fetched %d, want 4 — the fixture stopped exercising the sort: %v",
					len(rep.Fetched), rep.Fetched)
			}
			assertSorted(t, "Fetched", rep.Fetched)
			assertSorted(t, "Deleted", rep.Deleted)
			assertSorted(t, "Conflicts", rep.Conflicts)
			assertSorted(t, "PruneConflicts", rep.PruneConflicts)
			assertSorted(t, "Irregular", rep.Irregular)
			assertSorted(t, "Binary", rep.Binary)
		})
	}
}

// The conflict list is the union of two already-sorted lists, which is the one
// case where a missing sort is easy to miss by eye: each half looks fine.
func TestPullConflictsAreSortedAcrossTheUnionInEveryMode(t *testing.T) {
	for _, dry := range []bool{true, false} {
		name := "real"
		if dry {
			name = "dry-run (status)"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()

			// Two ordinary conflicts and one prune-conflict, named so that the
			// union is unsorted unless it is sorted: prune-conflict "aaa" must
			// land before the ordinary "zzz".
			for _, p := range []string{"zzz.css", "mmm.css", "aaa.css"} {
				if err := os.WriteFile(filepath.Join(dir, p), []byte("LOCAL EDIT"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// Tracked with a stale sha: both sides changed -> conflict. aaa.css is
			// absent from the listing and locally edited -> prune-conflict.
			st := State{
				ProjectID: "p1",
				Files: map[string]FileState{
					"zzz.css": {Etag: "old", Size: 3, SHA: SHA256Hex([]byte("old"))},
					"mmm.css": {Etag: "old", Size: 3, SHA: SHA256Hex([]byte("old"))},
					"aaa.css": {Etag: "old", Size: 3, SHA: SHA256Hex([]byte("old"))},
				},
			}
			if err := st.save(dir); err != nil {
				t.Fatal(err)
			}

			f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
				if name == "list_files" {
					return fakeReply{Text: listingFor(
						fileEntry("zzz.css", "new", 1),
						fileEntry("mmm.css", "new", 1),
					)}
				}
				return fakeReply{Text: "[]"}
			})

			rep, err := Pull(context.Background(), fakeClient(f), PullOpts{
				ProjectID:   "p1",
				Dir:         dir,
				Concurrency: 4,
				Prune:       true,
				DryRun:      dry,
			})
			if err != nil {
				t.Fatalf("Pull: %v", err)
			}
			if len(rep.Conflicts) < 2 {
				t.Fatalf("want at least 2 conflicts to exercise the union, got %v", rep.Conflicts)
			}
			assertSorted(t, "Conflicts", rep.Conflicts)
		})
	}
}

func TestPushReportFieldsAreSorted(t *testing.T) {
	for _, dry := range []bool{true, false} {
		name := "real"
		if dry {
			name = "dry-run"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for _, p := range []string{"zulu.css", "alpha.css", "mike.css", "bravo.css"} {
				if err := os.WriteFile(filepath.Join(dir, p), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
				switch name {
				case "list_files":
					return fakeReply{Text: listingFor()}
				case "finalize_plan":
					return fakeReply{Text: `{"plan_token":"tok"}`}
				case "write_files":
					// Echo an etag for every path the batch carried, in whatever
					// order the map yields -- Push must sort regardless.
					etags := map[string]any{}
					if fs, ok := args["files"].([]any); ok {
						for _, e := range fs {
							if m, ok := e.(map[string]any); ok {
								if p, ok := m["path"].(string); ok {
									etags[p] = "e-new"
								}
							}
						}
					}
					return fakeReply{Text: jsonOf(map[string]any{"etags": etags, "written": len(etags)})}
				}
				return fakeReply{Text: "[]"}
			})

			rep, err := Push(context.Background(), fakeClient(f), PushOpts{
				ProjectID: "p1",
				Dir:       dir,
				DryRun:    dry,
			})
			if err != nil {
				t.Fatalf("Push: %v", err)
			}
			if len(rep.Written) != 4 {
				t.Fatalf("written %d, want 4 — the fixture stopped exercising the sort: %v",
					len(rep.Written), rep.Written)
			}
			assertSorted(t, "Written", rep.Written)
			assertSorted(t, "Deleted", rep.Deleted)
			assertSorted(t, "Conflicts", rep.Conflicts)
			assertSorted(t, "BinaryConflicts", rep.BinaryConflicts)
			assertSorted(t, "Irregular", rep.Irregular)
		})
	}
}
