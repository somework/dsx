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

func NoPositionals(pos []string, form string) error {
	if len(pos) == 0 {
		return nil
	}
	return &dsxerr.Error{Kind: dsxerr.KindUsage,
		Msg: fmt.Sprintf("unexpected argument %q — usage: dsx %s", pos[0], form)}
}

func JSONFlag(fs *flag.FlagSet) *bool { return fs.Bool("json", false, "machine-readable output") }

func NewFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func ParseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
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
