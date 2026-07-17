package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/mcp"
	"github.com/somework/dsx/internal/syncer"
)

// Emit calls a tool and prints its text result.
//
// Under --json, stdout is guaranteed to be one JSON document. Most tools
// already answer in JSON and are passed through untouched; the few that answer
// in prose are wrapped rather than handed to a caller that is about to run a
// parser over them. A guarantee with exceptions is not one an agent can use.
func Emit(ctx context.Context, c *mcp.Client, tool string, args map[string]any, asJSON bool) error {
	text, err := c.CallTool(ctx, tool, args)
	if err != nil {
		return err
	}
	fmt.Println(JSONSafe(text, asJSON))
	return nil
}

func JSONSafe(text string, asJSON bool) string {
	if !asJSON {
		return text
	}
	if json.Valid([]byte(text)) {
		return text
	}
	b, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return text
	}
	return string(b)
}

// EmitFlagged parses the standard --json flag, then calls the tool. It is the
// shape every passthrough command takes: an agent should not have to learn
// which subcommands happen to accept --json.
func EmitFlagged(ctx context.Context, c *mcp.Client, name string, args []string, build func(pos []string) (string, map[string]any, error)) error {
	flags := NewFlagSet(name)
	asJSON := JSONFlag(flags)
	pos, err := ParseArgs(flags, args)
	if err != nil {
		return err
	}
	tool, toolArgs, err := build(pos)
	if err != nil {
		return err
	}
	return Emit(ctx, c, tool, toolArgs, *asJSON)
}

// EmitWrite is Emit for a tool that writes: it recovers from the server's
// demand for a standing project grant before printing.
func EmitWrite(ctx context.Context, c *mcp.Client, tool string, args map[string]any, projectID string, paths []string, asJSON bool) error {
	var (
		text string
		err  error
	)
	if _, given := args["plan_token"]; given {
		// The caller brought their own authority; do not mint another over it.
		// Short-circuiting to emit() here is what made `dsx put --plan` exit 1
		// on the very reply that exits 3 without --plan: emit does not classify.
		text, err = c.CallTool(ctx, tool, args)
	} else {
		text, err = syncer.CallWithGrant(ctx, c, tool, args, projectID, paths)
	}
	if err != nil {
		if conflicts, ok := mcp.ConflictFromToolError(err); ok {
			return dsxerr.Conflict(conflicts, "the server changed since dsx read it; nothing was written")
		}
		return err
	}
	fmt.Println(JSONSafe(text, asJSON))
	return nil
}
