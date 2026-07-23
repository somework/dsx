package comments

import (
	"context"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/mcp"
	"github.com/somework/dsx/internal/reply"
)

var Group = cmd.Group{
	Title: "COMMENTS",
	Noun:  "comment",
	Desc:  "pin-anchored feedback left on the project in Claude Design",
	Cmds: []cmd.Command{
		{Name: "comment ls", Form: "comment ls <project> [--queued] [--since <ts>]",
			Desc: "comment threads, newest state", Run: cmdCommentLs},
		{Name: "comment ack", Form: "comment ack <project> <id...>",
			Desc: "mark queued comments handled", Run: cmdCommentAck},
	},
}

func cmdCommentLs(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("comment ls")
	var (
		queued = flags.Bool("queued", false, "only comments queued for you via the app's \"Send to Claude\"")
		since  = flags.String("since", "", "RFC 3339 watermark; pass a previous reply's server_time back verbatim")
		asJSON = cmd.JSONFlag(flags)
	)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	project, rest, err := cmd.Need1(pos, "comment ls <project> [--queued] [--since <ts>]")
	if err != nil {
		return err
	}
	if err := cmd.NoExtra(rest, "comment ls <project> [--queued] [--since <ts>]"); err != nil {
		return err
	}
	a := map[string]any{"project_id": project}
	if *queued {
		a["queued_for_claude"] = true
	}
	if *since != "" {
		a["changed_since"] = *since
	}
	return cmd.Emit(ctx, c, "list_comments", a, *asJSON, reply.Comments)
}

func cmdCommentAck(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("comment ack")
	asJSON := cmd.JSONFlag(flags)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	project, ids, err := cmd.Need1(pos, "comment ack <project> <id...>")
	if err != nil {
		return err
	}
	// Need1 leaves the rest, but "the rest" being empty is the whole mistake
	// here: acking nothing is a call that reports success and moves no flag.
	if len(ids) == 0 {
		return dsxerr.Usage("comment ack <project> <id...>")
	}
	return cmd.Emit(ctx, c, "ack_comments", map[string]any{
		"project_id": project, "comment_ids": ids,
	}, *asJSON, reply.Acked)
}
