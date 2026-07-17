package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/mcp"
	"github.com/somework/dsx/internal/syncer"
)

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

func EmitWrite(ctx context.Context, c *mcp.Client, tool string, args map[string]any, projectID string, paths []string, asJSON bool) error {
	var (
		text string
		err  error
	)
	if _, given := args["plan_token"]; given {
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
