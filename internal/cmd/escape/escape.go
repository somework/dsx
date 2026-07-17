package escape

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/fmtutil"
	"github.com/somework/dsx/internal/mcp"
)

var Group = cmd.Group{
	Title: "ESCAPE HATCH",
	Cmds: []cmd.Command{
		{Name: "prompt", Form: "prompt [--project id] [--ds id]", Desc: "the server's own Claude Design prompt", Run: cmdPrompt},
		{Name: "tools", Form: "tools", Desc: "tool names and schemas from the server", Run: cmdTools},
		{Name: "raw", Form: "raw <tool> '<json-args>'", Desc: "call any tool verbatim", Run: cmdRaw},
	},
}

func cmdPrompt(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("prompt")
	var (
		project = flags.String("project", "", "project id")
		ds      = flags.String("ds", "", "design system id")
		asJSON  = cmd.JSONFlag(flags)
	)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}

	if err := cmd.NoPositionals(pos, "prompt [--project id] [--ds id]"); err != nil {
		return err
	}
	a := map[string]any{}
	if *project != "" {
		a["project_id"] = *project
	}
	if *ds != "" {
		a["design_system_id"] = *ds
	}
	return cmd.Emit(ctx, c, "get_claude_design_prompt", a, *asJSON)
}

func cmdTools(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("tools")
	var (
		full   = flags.Bool("schema", false, "print full JSON schemas")
		asJSON = cmd.JSONFlag(flags)
	)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}

	if err := cmd.NoPositionals(pos, "tools [--schema]"); err != nil {
		return err
	}
	raw, err := c.ToolsList(ctx)
	if err != nil {
		return err
	}
	if *full || *asJSON {
		fmt.Println(string(raw))
		return nil
	}
	var list struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return &dsxerr.Error{Kind: dsxerr.KindProtocol, Msg: "tools/list was not the shape dsx expects", Err: err}
	}
	for _, t := range list.Tools {
		fmt.Printf("%-26s %s\n", t.Name, fmtutil.Truncate(cmd.FirstLine(t.Description), 90))
	}
	return nil
}

func cmdRaw(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("raw")
	asJSON := cmd.JSONFlag(flags)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	tool, rest, err := cmd.Need1(pos, `raw <tool> '<json-args>'`)
	if err != nil {
		return err
	}
	a := map[string]any{}
	if len(rest) > 0 && rest[0] != "" {
		if err := json.Unmarshal([]byte(rest[0]), &a); err != nil {
			return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: "arguments must be a JSON object", Err: err}
		}

		if a == nil {
			return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: "arguments must be a JSON object, not null"}
		}
	}
	return cmd.Emit(ctx, c, tool, a, *asJSON)
}
