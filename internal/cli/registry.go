package cli

import (
	"sort"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/cmd/conv"
	"github.com/somework/dsx/internal/cmd/escape"
	"github.com/somework/dsx/internal/cmd/files"
	"github.com/somework/dsx/internal/cmd/members"
	"github.com/somework/dsx/internal/cmd/plans"
	"github.com/somework/dsx/internal/cmd/projects"
	// The package under internal/cmd/sync is named synccmd, not sync, so a file
	// there can `import "sync"` for a Mutex without shadowing it — the collision
	// that forced internal/syncer's name. The alias reads it back as `sync` here
	// so the slice below still names the section after its directory, which is
	// what TestEveryDeclaredGroupIsRegistered checks. The alias only binds this
	// file's scope; registry.go needs no stdlib sync.
	sync "github.com/somework/dsx/internal/cmd/sync"
)

// groups is the one list dsx dispatches, documents and completes from.
//
// It used to be three lists — a switch in run(), commandNames, and the usage
// const — that nothing but discipline held together. Two tests parsed run()'s
// AST to check them against each other, because no compiler could, and `put`
// still fell out of commandNames unnoticed: dispatched, documented, invisible
// to every shell. All three are now derived from this one, so the next omission
// is not a test failure but an absence of code.
//
// The order is the order of sections in `dsx help`, which is why this is an
// explicit slice rather than init() appends: init() order across files is
// invisible, and a human reads this output.
var groups = []cmd.Group{
	sync.Group, projects.Group, files.Group, plans.Group,
	conv.Group, members.Group, escape.Group, diagGroup,
}

// commandIndex is every dispatchable spelling, aliases included. commandNames
// is every name a shell offers and `usage` documents; aliases are left out,
// because `dsx --version` is a spelling, not a subcommand.
//
// Both are derived in init() rather than by var initialiser, which Go refuses:
// `completion` is itself a command in `groups`, and completionScript reads
// commandNames, so the initialiser graph closes a loop — groups → diagGroup →
// cmdCompletion → completionScript → commandNames → groups. The loop never runs,
// but Go's check is on the graph, not the execution, and it is right to be.
//
// This is derivation, not registration: `groups` above stays the one explicit,
// ordered source, and nothing adds itself to it from another file's init().
var (
	commandIndex map[string]cmd.Command
	commandNames []string
)

func init() {
	commandIndex = indexCommands(groups)
	commandNames = commandNamesOf(groups)
}

func indexCommands(gs []cmd.Group) map[string]cmd.Command {
	out := make(map[string]cmd.Command)
	for _, g := range gs {
		for _, c := range g.Cmds {
			out[c.Name] = c
			for _, a := range c.Aliases {
				out[a] = c
			}
		}
	}
	return out
}

func commandNamesOf(gs []cmd.Group) []string {
	var out []string
	for _, g := range gs {
		for _, c := range g.Cmds {
			out = append(out, c.Name)
		}
	}
	sort.Strings(out)
	return out
}
