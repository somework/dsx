package conv

import (
	"context"
	"encoding/json"
	"os"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/mcp"
	"github.com/somework/dsx/internal/reply"
)

var Group = cmd.Group{
	Title: "CONVERSATION",
	Noun:  "conv",
	Desc:  "the project's Claude Design chat",
	Cmds: []cmd.Command{
		{Name: "conv get", Form: "conv get <project> [--chat id]", Desc: "read a conversation", Run: cmdConv},
		{Name: "conv put", Form: "conv put <project> --messages <file.json> [--chat id] [--title t] [--append] [--synced-through-idx N]", Desc: "write one", Run: cmdConvPut},
	},
}

func cmdConv(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("conv get")
	chat := flags.String("chat", "", "chat id")
	asJSON := cmd.JSONFlag(flags)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	project, rest, err := cmd.Need1(pos, "conv get <project> [--chat id]")
	if err != nil {
		return err
	}
	if err := cmd.NoExtra(rest, "conv get <project> [--chat id]"); err != nil {
		return err
	}
	a := map[string]any{"project_id": project}
	if *chat != "" {
		a["chat_id"] = *chat
	}
	return cmd.Emit(ctx, c, "get_conversation", a, *asJSON, reply.Conversation)
}

func cmdConvPut(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("conv put")
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
	project, rest, err := cmd.Need1(pos, "conv put <project> --messages <file.json>")
	if err != nil {
		return err
	}
	if err := cmd.NoExtra(rest, "conv put <project> --messages <file.json>"); err != nil {
		return err
	}
	if *msgFile == "" {
		return dsxerr.Usage("conv put <project> --messages <file.json>")
	}
	b, err := os.ReadFile(*msgFile)
	if err != nil {
		return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: "cannot read " + *msgFile, Err: err}
	}
	// Structural JSON type only. Which keys a message carries is the server's
	// arg schema; an enum hardcoded here would refuse work a later server accepts.
	var messages []map[string]any
	if err := json.Unmarshal(b, &messages); err != nil {
		return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: *msgFile + " must hold a JSON array of message objects", Err: err}
	}
	// A JSON null decodes into a nil map without an error, so the type check
	// above cannot see it; unchecked it is forwarded to the server as null.
	for _, m := range messages {
		if m == nil {
			return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: *msgFile + " must hold a JSON array of message objects"}
		}
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
	return cmd.Emit(ctx, c, "put_conversation", a, *asJSON, nil)
}
