package reply

import (
	"strings"
	"testing"
)

// Measured against the real endpoint on 2026-07-23. All four reachable projects
// answered with zero comments, and there is no tool that creates one — they are
// left by people clicking pins in Claude Design — so the non-empty element is
// unmeasured in the same way list_members' is, and is rendered by nothing.
//
// The ack reply was measured with a well-formed but nonexistent UUID, which the
// server returns under not_queued rather than as an error. A real queued id was
// deliberately not used: acking one clears a flag a person is waiting on.
const (
	realComments = `{"comments":[],"server_time":"2026-07-23T06:49:31.190296Z"}`
	realAcked    = `{"acked":[],"not_queued":["00000000-0000-4000-8000-000000000000"]}`
)

func TestCommentsRendersTheOneShapeItHasSeen(t *testing.T) {
	t.Parallel()
	out, ok := Comments(realComments)
	if !ok {
		t.Fatal("refused its own measured reply")
	}
	for _, w := range []string{"no comments", "2026-07-23T06:49:31.190296Z"} {
		if !strings.Contains(out, w) {
			t.Errorf("output missing %q:\n%s", w, out)
		}
	}
}

func TestAckedNamesWhatMovedAndWhatWasAlreadyClear(t *testing.T) {
	t.Parallel()
	out, ok := Acked(realAcked)
	if !ok {
		t.Fatal("refused its own measured reply")
	}
	for _, w := range []string{"0 comments handled", "1 already clear", "00000000-0000-4000-8000-000000000000"} {
		if !strings.Contains(out, w) {
			t.Errorf("output missing %q:\n%s", w, out)
		}
	}
	// An ack that moved something must say so rather than only listing ids.
	out, ok = Acked(`{"acked":["a1b2c3d4-0000-4000-8000-000000000001"],"not_queued":[]}`)
	if !ok {
		t.Fatal("refused a reply that acked one comment")
	}
	if !strings.Contains(out, "1 comment handled") || strings.Contains(out, "already clear") {
		t.Errorf("a clean ack reads wrong:\n%s", out)
	}
}

func TestTheCommentDecodersRefuseWhatIsNotTheirShape(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		render func(string) (string, bool)
		in     string
	}{
		// A non-empty listing falls through whole: the element was never
		// measured and a table built from the tool's prose would be a guess.
		{"a non-empty listing", Comments, `{"comments":[{"comment_id":"x"}],"server_time":"t"}`},
		{"a listing with no watermark", Comments, `{"comments":[]}`},
		{"a bare array", Comments, `[]`},
		{"members", Comments, realMembers},
		{"an ack with no acked key", Acked, `{"not_queued":[]}`},
		{"an ack with no not_queued key", Acked, `{"acked":[]}`},
		{"a write ack", Acked, realWrite},
		{"a delete ack", Acked, realDelete},
		{"prose", Acked, "done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if out, ok := tc.render(tc.in); ok {
				t.Errorf("rendered a shape it has not measured:\n%s", out)
			}
		})
	}
}

// Invariant 7: server_time and every id are server text on lines a person
// reads, and the watermark is one they are told to pass back.
func TestCommentServerTextIsSanitised(t *testing.T) {
	t.Parallel()
	out, ok := Comments(`{"comments":[],"server_time":"2026-07-23T06:49:31Z\rEVIL"}`)
	if !ok {
		t.Fatal("refused")
	}
	if strings.ContainsRune(out, '\r') {
		t.Errorf("a carriage return reached the terminal:\n%q", out)
	}
	out, ok = Acked(`{"acked":["a\rEVIL"],"not_queued":[]}`)
	if !ok {
		t.Fatal("refused")
	}
	if strings.ContainsRune(out, '\r') {
		t.Errorf("a carriage return reached the terminal through an id:\n%q", out)
	}
}
