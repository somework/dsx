package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/somework/dsx/internal/dsxerr"
)

// splitGlobalFlags peels dsx's own options off the front of argv, before the
// command name. `-C <dir>` is the only one, and it is git's: run as if dsx had
// been started in <dir>.
//
// It stops at the first token that is not a global, so a -C after the verb
// belongs to the verb. That matters more than it looks: every command parses
// its own FlagSet, and peeling a flag out from under one would silently change
// what it sees.
//
// Repeated -C accumulates rather than overwrites, because the caller applies
// them in order with successive chdirs — which is exactly git's "each
// subsequent -C is interpreted relative to the preceding one", obtained by
// doing nothing special.
//
// A bare -C is a usage error rather than a no-op: swallowing it would run the
// command in the directory the caller was standing in, and for `push --prune`
// that is a different tree.
func splitGlobalFlags(args []string) (chdirs []string, rest []string, err error) {
	const needDir = "-C needs a directory — `dsx -C <dir> <command>`"

	for len(args) > 0 {
		switch arg := args[0]; {
		case arg == "-C":
			if len(args) < 2 {
				return nil, nil, &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: needDir}
			}
			chdirs, args = append(chdirs, args[1]), args[2:]
		case strings.HasPrefix(arg, "-C="):
			dir := strings.TrimPrefix(arg, "-C=")
			if dir == "" {
				return nil, nil, &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: needDir}
			}
			chdirs, args = append(chdirs, dir), args[1:]
		default:
			return chdirs, args, nil
		}
	}
	return chdirs, nil, nil
}

// applyChdirs moves into each -C in turn. In order, not merged: git reads each
// subsequent -C relative to the one before it, and successive chdirs are that
// rule rather than an imitation of it.
//
// A refusal here is KindUsage, not a filesystem error: the caller named a
// directory that is not one, and no retry of the same command helps.
func applyChdirs(dirs []string) error {
	for _, dir := range dirs {
		if err := os.Chdir(dir); err != nil {
			return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
				"-C %s: cannot run there — %v", dir, err)}
		}
	}
	return nil
}
