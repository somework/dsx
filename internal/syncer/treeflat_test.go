package syncer

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// list_files grew a `depth` parameter that dsx did not use: `depth: -1` returns
// the whole tree, files only, no directory stubs, in ONE call. Measured against
// a real project: 109 files in one request where the recursive walk spends
// eleven. That is squarely what dsx exists to do.
//
// It is also the most dangerous function in the package to touch. Every sync
// verb reads this map, and `--prune` deletes whatever is missing from it, so a
// flat listing that silently came back short would read as "the user deleted
// those files". The whole design below is about never accepting such a listing:
// anything the flat call answers that does not look exactly like the measured
// shape is discarded and the proven recursive walk runs instead.

func TestWalkTreeTakesTheWholeTreeInOneCallWhenItCan(t *testing.T) {
	var calls atomic.Int32
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		if name != "list_files" {
			return fakeReply{Text: "unexpected " + name, IsError: true}
		}
		calls.Add(1)
		if args["depth"] == nil {
			return fakeReply{Text: "the flat call must be tried first", IsError: true}
		}
		return fakeReply{Text: listingFor(
			fileEntry("index.css", "e1", 3),
			fileEntry("tokens/colors.css", "e2", 5),
			fileEntry("tokens/deep/x.css", "e3", 7),
		)}
	})

	got, err := WalkTree(context.Background(), fakeClient(f), "p1", 4)
	if err != nil {
		t.Fatalf("WalkTree: %v", err)
	}
	want := []string{"index.css", "tokens/colors.css", "tokens/deep/x.css"}
	if !slices.Equal(SortedPaths(got), want) {
		t.Errorf("paths = %v, want %v", SortedPaths(got), want)
	}
	if got["tokens/colors.css"].Etag != "e2" {
		t.Errorf("etag = %q, want e2", got["tokens/colors.css"].Etag)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("%d list_files calls, want 1 — the flat listing is the whole point", n)
	}
}

// Every way the flat listing can fail to be the measured shape must end in the
// recursive walk, whose answer is authoritative. Falling back costs one wasted
// call; trusting a short listing costs the user's files.
func TestAFlatListingThatIsNotTheMeasuredShapeIsDiscarded(t *testing.T) {
	deep := "a/" + strings.Repeat("b/", maxTreeDepth) + "c.css"
	for _, tc := range []struct {
		name string
		flat fakeReply
	}{
		{
			// depth:-1 is documented and measured to return files only. A stub
			// means the server answered a different question than the one dsx
			// asked, and a tree missing everything under that stub is exactly
			// the shape --prune reads as deletions.
			name: "a directory stub came back",
			flat: fakeReply{Text: listingFor(fileEntry("index.css", "e1", 3), dirEntry("tokens"))},
		},
		{
			// The server errors past 20,000 rather than truncating — so it
			// says. A listing AT the cap is the one place that promise could
			// fail silently, so dsx declines to be the first to find out.
			name: "the listing reaches the cap",
			flat: fakeReply{Text: listingFor(flatCapEntries(flatListCap)...)},
		},
		{
			name: "the call errored",
			flat: fakeReply{Text: "list this a subdirectory at a time", IsError: true},
		},
		{
			name: "malformed",
			flat: fakeReply{Text: "{not a listing}"},
		},
		{
			// `null` unmarshals into a nil slice WITHOUT an error, so without
			// its own guard it reads as "this project has no files" — and the
			// very next `push --prune` deletes every file on the server, or
			// `pull --prune` every file on disk. The server was measured
			// answering `[]` even for a path that does not exist, so `null` is
			// a shape it never sends and must never be believed.
			name: "a null listing",
			flat: fakeReply{Text: "null"},
		},
		{
			// A path deeper than the recursive walk would ever reach must not
			// become reachable just because a different call fetched it: one
			// place decides what dsx will write to disk.
			name: "a path past the depth cap",
			flat: fakeReply{Text: listingFor(fileEntry(deep, "e9", 1))},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var flat, recursive atomic.Int32
			f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
				if name != "list_files" {
					return fakeReply{Text: "unexpected " + name, IsError: true}
				}
				if args["depth"] != nil {
					flat.Add(1)
					return tc.flat
				}
				recursive.Add(1)
				switch args["path"] {
				case nil:
					return fakeReply{Text: listingFor(fileEntry("index.css", "e1", 3), dirEntry("tokens"))}
				case "tokens":
					return fakeReply{Text: listingFor(fileEntry("tokens/colors.css", "e2", 5))}
				}
				return fakeReply{Text: "no such dir", IsError: true}
			})

			got, err := WalkTree(context.Background(), fakeClient(f), "p1", 4)
			if err != nil {
				t.Fatalf("WalkTree: %v", err)
			}
			want := []string{"index.css", "tokens/colors.css"}
			if !slices.Equal(SortedPaths(got), want) {
				t.Errorf("paths = %v, want the recursive walk's %v", SortedPaths(got), want)
			}
			if flat.Load() != 1 || recursive.Load() == 0 {
				t.Errorf("flat=%d recursive=%d — the flat call must be tried once and then abandoned",
					flat.Load(), recursive.Load())
			}
		})
	}
}

// Invariant 3 must survive the new path: a walk that could not complete returns
// no listing at all, never a short one.
func TestAFailedFlatListingStillFailsWhenTheWalkAlsoFails(t *testing.T) {
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: "down", IsError: true}
	})
	got, err := WalkTree(context.Background(), fakeClient(f), "p1", 4)
	if err == nil {
		t.Fatal("both listings failed and WalkTree reported success")
	}
	if got != nil {
		t.Errorf("a failed walk returned %d entries; --prune would read them as the whole tree", len(got))
	}
}

func TestAnInterruptedFlatListingIsAFailureNotAShortListing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		cancel()
		return fakeReply{Text: listingFor(fileEntry("index.css", "e1", 3))}
	})
	got, err := WalkTree(ctx, fakeClient(f), "p1", 4)
	if err == nil {
		t.Fatal("an interrupted listing was reported as a complete tree")
	}
	if got != nil {
		t.Errorf("returned %d entries after interruption", len(got))
	}
}

func flatCapEntries(n int) []RemoteEntry {
	out := make([]RemoteEntry, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, fileEntry(fmt.Sprintf("f%06d.css", i), "e", 1))
	}
	return out
}
