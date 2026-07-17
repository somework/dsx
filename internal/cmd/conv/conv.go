package conv

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/mcp"
)

// Group is the CONVERSATION section of `dsx help`.
var Group = cmd.Group{
	Title: "CONVERSATION",
	Cmds: []cmd.Command{
		{Name: "conv", Form: "conv <project> [--chat id]", Run: cmdConv},
		{Name: "conv-put", Form: "conv-put <project> --messages <file.json> [--chat id] [--title t] [--append]", Run: cmdConvPut},
	},
}

func cmdConv(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("conv")
	chat := flags.String("chat", "", "chat id")
	asJSON := cmd.JSONFlag(flags)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	project, _, err := cmd.Need1(pos, "conv <project> [--chat id]")
	if err != nil {
		return err
	}
	a := map[string]any{"project_id": project}
	if *chat != "" {
		a["chat_id"] = *chat
	}
	return cmd.Emit(ctx, c, "get_conversation", a, *asJSON)
}

func cmdConvPut(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("conv-put")
	var (
		msgFile = flags.String("messages", "", "JSON file holding the messages array (required)")
		chat    = flags.String("chat", "", "chat id")
		title   = flags.String("title", "", "conversation title")
		appnd   = flags.Bool("append", false, "append instead of replacing")
		through = flags.Int("synced-through-idx", -1, "synced_through_idx")
		asJSON  = cmd.JSONFlag(flags)
	)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	project, _, err := cmd.Need1(pos, "conv-put <project> --messages <file.json>")
	if err != nil {
		return err
	}
	if *msgFile == "" {
		return dsxerr.Usage("conv-put <project> --messages <file.json>")
	}
	b, err := os.ReadFile(*msgFile)
	if err != nil {
		return err
	}
	var messages []any
	if err := json.Unmarshal(b, &messages); err != nil {
		return fmt.Errorf("%s must hold a JSON array of messages: %w", *msgFile, err)
	}

	a := map[string]any{"project_id": project, "messages": messages}
	if *chat != "" {
		a["chat_id"] = *chat
	}
	if *title != "" {
		a["title"] = *title
	}
	if *appnd {
		a["append"] = true
	}
	if *through >= 0 {
		a["synced_through_idx"] = *through
	}
	return cmd.Emit(ctx, c, "put_conversation", a, *asJSON)
}
