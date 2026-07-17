// Package fmtutil holds the two formatting helpers that more than one layer
// needs, and exists only because of that.
//
// Truncate is called from the transport, from push, and from the CLI; Bytes from
// pull, push, and the CLI. Left where they were, each would have pulled an
// import edge upward -- sync importing cli for a byte counter, or the CLI
// importing the transport for a string clip. Neither pays for itself, and both
// would be cycles once the CLI imports sync.
//
// Nothing else belongs here. A package named for what it is rather than what it
// does earns its keep exactly as long as it stays this small.
package fmtutil

import (
	"fmt"
	"unicode/utf8"
)

// Truncate clips s to at most n bytes, marking the cut with an ellipsis.
//
// The bound is in bytes because the callers are bounding a payload, but the cut
// cannot land inside a rune: the endpoint's own prose is full of multi-byte
// characters (it writes — and …), and half of one is invalid UTF-8 in an error
// message that is about to be marshalled into --json.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// Bytes renders a byte count for a human.
func Bytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	const units = "KMGT"
	div, exp := int64(unit), 0
	// exp is clamped, not merely expected to stay small: it indexes `units`,
	// and an unclamped one panicked at 1 PiB. Nothing in a Design project comes
	// close today, but a summary line is a strange place to take the process
	// down, and the bound costs a comparison.
	for v := n / unit; v >= unit && exp < len(units)-1; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), units[exp])
}
