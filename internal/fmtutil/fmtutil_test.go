// Tests for the two helpers that outgrew their old homes.
//
// They arrive with their comments verbatim: each records a defect that shipped,
// and that record is the load-bearing part -- the assertion only re-proves what
// the comment explains. Truncate's tests came from the transport's suite and
// humanBytes' from the CLI's, which is exactly why both functions are here now:
// each was reached from more than one layer.

package fmtutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestHumanBytesSurvivesASizeItCannotName(t *testing.T) {
	// humanBytes indexed "KMGT" with an exponent it never clamped, so anything
	// at or past 1 PiB panicked. rep.Bytes flows through here on every summary
	// line: a sync that moved a petabyte would take the process down while
	// printing how well it had gone.
	for _, n := range []int64{1 << 50, 1 << 60, 1<<63 - 1} {
		got := Bytes(n) // must not panic
		if got == "" {
			t.Errorf("Bytes(%d) = %q", n, got)
		}
	}
	// The units it does have must keep their old spelling exactly.
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{0, "0 B"}, {512, "512 B"}, {1024, "1.0 KB"},
		{1 << 20, "1.0 MB"}, {1 << 30, "1.0 GB"}, {1 << 40, "1.0 TB"},
	} {
		if got := Bytes(tc.in); got != tc.want {
			t.Errorf("Bytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTruncateNeverEmitsAHalfRune(t *testing.T) {
	// truncate cuts server text for display. Slicing by byte can land inside a
	// multi-byte rune, and the endpoint's own error prose is full of them
	// (it uses — and …). The cut must stay on a rune boundary.
	s := strings.Repeat("é", 50) // 100 bytes, 50 runes
	for n := 1; n < 100; n++ {
		got := Truncate(s, n)
		if !utf8.ValidString(got) {
			t.Fatalf("Truncate(%d) produced invalid UTF-8: %q", n, got)
		}
	}
	if got := Truncate("abc", 10); got != "abc" {
		t.Errorf("truncate must leave a short string alone, got %q", got)
	}
	if got := Truncate("abc", 3); got != "abc" {
		t.Errorf("truncate at exactly n must not cut, got %q", got)
	}
}

func TestHumanBytesAcrossEveryUnitBoundary(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{512, "512 B"},
		{1023, "1023 B"}, // last value before the unit switches
		{1024, "1.0 KB"}, // first value after it
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{1024 * 1024 * 3 / 2, "1.5 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{1024 * 1024 * 1024 * 1024, "1.0 TB"},
	}
	for _, tc := range cases {
		if got := Bytes(tc.n); got != tc.want {
			t.Errorf("Bytes(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// humanBytes saturates at TB rather than indexing past its unit table. The
// exponent is clamped at util.go's loop bound, so every int64 names something.
//
// This comment used to say the clamp was missing and that 1<<50 panicked. It
// was added in the meantime and the note was left behind, which is worse than
// no note: it tells the next reader to fix what is already fixed. The range
// below now runs past the old ceiling, exactly as the stale note instructed.
// TestHumanBytesSurvivesASizeItCannotName covers the extremes.
func TestHumanBytesStaysWithinItsUnitTableForEveryReachableTotal(t *testing.T) {
	for _, n := range []int64{1 << 40, 1 << 45, 1<<50 - 1, 1 << 50, 1 << 55} {
		got := Bytes(n)
		if got == "" {
			t.Errorf("Bytes(%d) returned nothing", n)
		}
		if !strings.HasSuffix(got, "B") {
			t.Errorf("Bytes(%d) = %q, want a unit suffix", n, got)
		}
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"shorter than n is untouched", "abc", 5, "abc"},
		{"exactly n is untouched", "abcde", 5, "abcde"},
		{"longer than n keeps n and marks the cut", "abcdef", 5, "abcde…"},
		{"empty", "", 5, ""},
		{"n of zero", "abc", 0, "…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Truncate(tc.in, tc.n); got != tc.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

// The boundary is the one that matters: at exactly n nothing is appended, so a
// caller cannot mistake a whole message for a cut one.
func TestTruncateDoesNotMarkAMessageItDidNotCut(t *testing.T) {
	t.Parallel()
	s := strings.Repeat("x", 200)
	if got := Truncate(s, 200); strings.Contains(got, "…") {
		t.Errorf("truncate marked a message it did not cut: %q", got)
	}
	if got := Truncate(s+"x", 200); !strings.HasSuffix(got, "…") {
		t.Errorf("truncate cut a message without marking it: %q", got)
	}
}
