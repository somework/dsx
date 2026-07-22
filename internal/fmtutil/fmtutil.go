package fmtutil

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

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

func Bytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	const units = "KMGT"
	div, exp := int64(unit), 0

	for v := n / unit; v >= unit && exp < len(units)-1; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), units[exp])
}

// Printable replaces every non-graphic rune with '?'. Text that arrives from
// the server is untrusted input (invariant 7): a carriage return or an escape
// in a name rewrites what the terminal has already shown, and dsx prints
// warnings there.
func Printable(s string) string {
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		if r == unicode.ReplacementChar || !unicode.IsGraphic(r) {
			sb.WriteRune('?')
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// PrintableDoc is Printable for a whole reply rather than one field: it keeps
// the line breaks and tabs that are the document's own structure, and disarms
// everything else Printable disarms. Printable alone answers false to
// unicode.IsGraphic for '\n' as readily as for '\r', so running it over a
// multi-line reply collapses the reply into a single '?'-riddled line — which
// is exactly what an unrecognised reply used to become.
//
// A line break dsx is printing is not the threat invariant 7 names. A break
// inside a field still is, so Printable keeps its old shape and the two are
// separate functions rather than one with a flag.
func PrintableDoc(s string) string {
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		if r == '\n' || r == '\t' {
			sb.WriteRune(r)
			continue
		}
		if r == unicode.ReplacementChar || !unicode.IsGraphic(r) {
			sb.WriteRune('?')
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}
