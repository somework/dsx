package cmd

import (
	"context"

	"github.com/somework/dsx/internal/mcp"
)

type Needs int

const (
	NeedClient Needs = iota
	NeedNothing
	NeedAuth
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
