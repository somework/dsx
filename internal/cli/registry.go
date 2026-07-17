package cli

import (
	"context"
	"sort"

	"github.com/somework/dsx/internal/mcp"
)

// needs says how much of dsx must exist before a command can run.
//
// It is a field rather than a position in run()'s statement order, which is
// what it used to be: three ifs stacked above the switch, each load-bearing and
// none of them saying so. needClient is the zero value because it is both the
// common case and the safe one — a command that declares nothing gets a
// credential and a client, not neither.
type needs int

const (
	needClient  needs = iota // the default: credential loaded, client built
	needNothing              // help, version, completion: no signal handler, no token
	needAuth                 // auth: reads the credential itself, builds no client
)

// command is one dsx subcommand: one thin wrapper over one MCP tool. They exist
// to spell the arguments out, not to add behaviour — `dsx raw` is the escape
// hatch for anything not wrapped here, and a wrapper that started interpreting
// replies would make the two disagree.
//
// Every one takes --json. Under it, stdout is one JSON document — see emit.
//
// Exactly one of Tool and Run is set — TestEveryCommandHasExactlyOneShape says
// so, because a command with neither dispatches to a nil call.
//
// Tool is the passthrough shape: parse --json, build the call, print the reply.
// It fits every command whose only flag is --json, and it is deliberately
// unable to express anything else, so a wrapper cannot quietly grow behaviour.
// Run is for the rest — anything that reads a file, walks the tree, or turns a
// reply into an exit code.
type command struct {
	Name string
	// Aliases are flag spellings of the same command (-h, --version). They
	// dispatch, but are neither documented nor completed: nobody Tabs for them,
	// and listing them would put "--json"-shaped noise in the command list.
	Aliases []string
	// Form is the usage line after "dsx ", and starts with Name —
	// TestEveryCommandFormStartsWithItsName. A Form naming a different command
	// than Name dispatches one thing and documents another.
	Form string
	// Desc is the right-hand column of that line, or empty when Form is already
	// too long to leave room. Output width is a token budget.
	Desc  string
	Needs needs
	Tool  func(pos []string) (string, map[string]any, error)
	Run   func(ctx context.Context, c *mcp.Client, args []string) error
}

// group is one section of `dsx help`, and one file in this package.
type group struct {
	Title string // the whole heading line, parenthetical included
	Note  string // prose printed under the commands, already indented
	Cmds  []command
}

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
var groups = []group{
	syncGroup, projectsGroup, filesGroup, plansGroup,
	convGroup, membersGroup, escapeGroup, diagGroup,
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
	commandIndex map[string]command
	commandNames []string
)

func init() {
	commandIndex = indexCommands(groups)
	commandNames = commandNamesOf(groups)
}

func indexCommands(gs []group) map[string]command {
	out := make(map[string]command)
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

func commandNamesOf(gs []group) []string {
	var out []string
	for _, g := range gs {
		for _, c := range g.Cmds {
			out = append(out, c.Name)
		}
	}
	sort.Strings(out)
	return out
}

func isKnownCommand(name string) bool {
	_, ok := commandIndex[name]
	return ok
}

// dispatch runs the command, whichever of its two shapes it has.
func (c command) dispatch(ctx context.Context, client *mcp.Client, args []string) error {
	if c.Tool != nil {
		return emitFlagged(ctx, client, c.Name, args, c.Tool)
	}
	return c.Run(ctx, client, args)
}

// noClient adapts a command that needs neither a context nor a client. The two
// parameters stay in the signature so that every command has one shape; the
// alternative is a second Run field nobody would keep straight.
func noClient(f func(args []string) error) func(context.Context, *mcp.Client, []string) error {
	return func(_ context.Context, _ *mcp.Client, args []string) error { return f(args) }
}
