// Package cmd is the shape of a dsx command: the types a group declares, and
// the helpers every command needs to parse its arguments and print its reply.
//
// It knows nothing about which commands exist. That list is `groups` in
// internal/cli, and it has to live above this package -- a kernel that knew its
// own groups would import them while they import it. The same forcing puts
// `usage`, `help` and `completion` in cli too: they are the commands that need
// the program as a whole, and nothing below the assembly point can have it.
package cmd

import (
	"context"

	"github.com/somework/dsx/internal/mcp"
)

// Needs says how much of dsx must exist before a command can run.
//
// It is a field rather than a position in run()'s statement order, which is
// what it used to be: three ifs stacked above the switch, each load-bearing and
// none of them saying so. NeedClient is the zero value because it is both the
// common case and the safe one — a command that declares nothing gets a
// credential and a client, not neither.
type Needs int

const (
	NeedClient  Needs = iota // the default: credential loaded, client built
	NeedNothing              // help, version, completion: no signal handler, no token
	NeedAuth                 // auth: reads the credential itself, builds no client
)

// Command is one dsx subcommand: one thin wrapper over one MCP tool. They exist
// to spell the arguments out, not to add behaviour — `dsx raw` is the escape
// hatch for anything not wrapped here, and a wrapper that started interpreting
// replies would make the two disagree.
//
// Every one takes --json. Under it, stdout is one JSON document — see Emit.
//
// Exactly one of Tool and Run is set — TestEveryCommandHasExactlyOneShape says
// so, because a command with neither dispatches to a nil call.
//
// Tool is the passthrough shape: parse --json, build the call, print the reply.
// It fits every command whose only flag is --json, and it is deliberately
// unable to express anything else, so a wrapper cannot quietly grow behaviour.
// Run is for the rest — anything that reads a file, walks the tree, or turns a
// reply into an exit code.
type Command struct {
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
	Needs Needs
	Tool  func(pos []string) (string, map[string]any, error)
	Run   func(ctx context.Context, c *mcp.Client, args []string) error
}

// Group is one section of `dsx help`. Each Group value lives in its own package
// under internal/cmd/<group> (or is diagGroup in cli); package cmd holds only
// the type, because a kernel that named its own groups would import them while
// they import it.
type Group struct {
	Title string // the whole heading line, parenthetical included
	Note  string // prose printed under the commands, already indented
	Cmds  []Command
}

// Dispatch runs the command, whichever of its two shapes it has.
func (c Command) Dispatch(ctx context.Context, client *mcp.Client, args []string) error {
	if c.Tool != nil {
		return EmitFlagged(ctx, client, c.Name, args, c.Tool)
	}
	return c.Run(ctx, client, args)
}

// NoClient adapts a command that needs neither a context nor a client. The two
// parameters stay in the signature so that every command has one shape; the
// alternative is a second Run field nobody would keep straight.
func NoClient(f func(args []string) error) func(context.Context, *mcp.Client, []string) error {
	return func(_ context.Context, _ *mcp.Client, args []string) error { return f(args) }
}
