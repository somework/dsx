package cmd

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/somework/dsx/internal/dsxerr"
)

func Need1(args []string, form string) (string, []string, error) {
	if len(args) < 1 {
		return "", nil, dsxerr.Usage(form)
	}
	return args[0], args[1:], nil
}

func Need2(args []string, form string) (string, string, []string, error) {
	if len(args) < 2 {
		return "", "", nil, dsxerr.Usage(form)
	}
	return args[0], args[1], args[2:], nil
}

// NoExtra rejects positionals a command did not consume. Callers pass the tail
// left over after Need1/Need2 (or the whole slice for arg-free commands); a
// stray trailing arg is a mistyped invocation and must fail fast, mirroring how
// resolveSyncTarget rejects `len(pos) > expected` rather than ignoring it.
func NoExtra(rest []string, form string) error {
	if len(rest) == 0 {
		return nil
	}
	return &dsxerr.Error{Kind: dsxerr.KindUsage,
		Msg: fmt.Sprintf("unexpected argument %q — usage: dsx %s", rest[0], form)}
}

// NoPositionals asserts a command takes no positionals at all. It is NoExtra
// read from the other end: same rejection, named for the arg-free case.
func NoPositionals(pos []string, form string) error {
	return NoExtra(pos, form)
}

func JSONFlag(fs *flag.FlagSet) *bool { return fs.Bool("json", false, "machine-readable output") }

func NewFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// helpError carries the flag documentation the FlagSet holds at the moment
// help is asked for. It unwraps to flag.ErrHelp so the caller can recognise a
// question rather than a malformed invocation without matching on text, and it
// renders that documentation as its own message so flag's internal
// "flag: help requested" never reaches a user.
type helpError struct{ flags string }

func (e *helpError) Error() string { return e.flags }
func (e *helpError) Unwrap() error { return flag.ErrHelp }

// flagDefaults renders fs's own per-flag list. NewFlagSet points fs at
// io.Discard so flag never chatters to stderr on a parse error; the redirect
// here is scoped to one call and restores the discard before returning.
func flagDefaults(fs *flag.FlagSet) string {
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	fs.PrintDefaults()
	fs.SetOutput(io.Discard)
	return buf.String()
}

func ParseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, &helpError{flags: flagDefaults(fs)}
			}
			return nil, &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: "dsx " + fs.Name(), Err: err}
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// SplitList splits a comma-separated flag value, trimming each entry and
// dropping empties. The comma is the only separator by design and nothing
// escapes it, so a value that itself contains a comma is unrepresentable;
// inner spaces, by contrast, are deliberately preserved. A backslash escape or
// a repeatable accumulating flag were both weighed and rejected: they would
// break the tests that pin this shape to serve an input we cannot confirm the
// server accepts. TestSplitListCannotExpressACommaInAPath holds the limit.
func SplitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func FirstLine(s string) string {
	for i := range len(s) {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}
