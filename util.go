package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
)

func sortStrings(s []string) { sort.Strings(s) }

// jsonFlag adds the standard --json. Every command takes it, so that an agent
// never has to know which ones happen to.
func jsonFlag(fs *flag.FlagSet) *bool { return fs.Bool("json", false, "machine-readable output") }

// newFlagSet builds a FlagSet that reports usage errors through dsx's own
// classification instead of printing to stderr and returning an unlabelled
// error. flag's default output would bypass --json entirely.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parseArgs parses flags that may appear before, between, or after positional
// arguments. Go's flag package stops at the first non-flag token, which would
// silently ignore `dsx tree <project> --json`.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			// A bad flag is a bad invocation: retrying it verbatim cannot help,
			// and the caller deserves that in the exit code.
			return nil, &dsxError{Kind: kindUsage, Msg: "dsx " + fs.Name(), Err: err}
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

func asToolError(err error, target **toolError) bool {
	var te *toolError
	if errors.As(err, &te) {
		*target = te
		return true
	}
	return false
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// splitList parses a comma-separated flag value, dropping empties.
func splitList(s string) []string {
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
