package cmd

import (
	"context"
	"io"

	"github.com/somework/dsx/internal/mcp"
)

type Needs int

const (
	// NeedClient is the zero value and thus the default for any command that
	// omits Needs.
	NeedClient Needs = iota
	NeedNothing
	NeedAuth
	// NeedOptionalClient builds a client from whatever token is available but
	// does not abort when auth fails: the command runs regardless, with a
	// tokenless client if need be. `doctor` uses this so it can diagnose the
	// very auth failures that would otherwise short-circuit dispatch.
	NeedOptionalClient
)

type Command struct {
	Name string

	Aliases []string

	Form string

	Desc  string
	Needs Needs
	Tool  func(pos []string) (string, map[string]any, error)
	Run   func(ctx context.Context, c *mcp.Client, args []string) error
}

type Group struct {
	Title string
	Note  string
	Cmds  []Command
}

func (c Command) Dispatch(ctx context.Context, client *mcp.Client, args []string) error {
	if c.Tool != nil {
		return EmitFlagged(ctx, client, c.Name, args, c.Tool)
	}
	return c.Run(ctx, client, args)
}

func NoClient(f func(args []string) error) func(context.Context, *mcp.Client, []string) error {
	return func(_ context.Context, _ *mcp.Client, args []string) error { return f(args) }
}

// Progress is where a transfer counter draws, nil for silence. cli decides
// once at startup and sets it; the leaf packages never ask the terminal
// themselves. Only presentation hangs off it — nothing semantic.
var Progress io.Writer
