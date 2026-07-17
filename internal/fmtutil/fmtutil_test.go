package fmtutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestHumanBytesSurvivesASizeItCannotName(t *testing.T) {
	for _, n := range []int64{1 << 50, 1 << 60, 1<<63 - 1} {
		got := Bytes(n)
		if got == "" {
			t.Errorf("Bytes(%d) = %q", n, got)
		}
	}

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
	s := strings.Repeat("é", 50)
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
		{1023, "1023 B"},
		{1024, "1.0 KB"},
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
