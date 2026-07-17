package cmd

import (
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

// NoPositionals refuses arguments a command does not take.
//
// Silently discarding them lets `dsx prompt <project>` -- the spelling every
// other command uses -- return the generic prompt with exit 0. A plausible
// wrong answer is worse than an error, because the caller never learns it asked
// the wrong question.
func NoPositionals(pos []string, form string) error {
	if len(pos) == 0 {
		return nil
	}
	return &dsxerr.Error{Kind: dsxerr.KindUsage,
		Msg: fmt.Sprintf("unexpected argument %q — usage: dsx %s", pos[0], form)}
}

// JSONFlag adds the standard --json. Every command takes it, so that an agent
// never has to know which ones happen to.
func JSONFlag(fs *flag.FlagSet) *bool { return fs.Bool("json", false, "machine-readable output") }

// NewFlagSet builds a FlagSet that reports usage errors through dsx's own
// classification instead of printing to stderr and returning an unlabelled
// error. flag's default output would bypass --json entirely.
func NewFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// ParseArgs parses flags that may appear before, between, or after positional
// arguments. Go's flag package stops at the first non-flag token, which would
// silently ignore `dsx tree <project> --json`.
func ParseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			// A bad flag is a bad invocation: retrying it verbatim cannot help,
			// and the caller deserves that in the exit code.
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

// SplitList parses a comma-separated flag value, dropping empties.
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
