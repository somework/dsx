package projects

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/somework/dsx/internal/fmtutil"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/mcp"
)

var Group = cmd.Group{
	Title: "PROJECTS",
	Noun:  "project",
	Desc:  "design projects on the server",
	Cmds: []cmd.Command{
		{Name: "project ls", Form: "project ls", Desc: "list projects", Run: cmdProjects},
		{Name: "project get", Form: "project get <id>", Desc: "project detail",
			Tool: func(pos []string) (string, map[string]any, error) {
				id, rest, err := cmd.Need1(pos, "project get <id>")
				if err != nil {
					return "", nil, err
				}
				if err := cmd.NoExtra(rest, "project get <id>"); err != nil {
					return "", nil, err
				}
				return "get_project", map[string]any{"project_id": id}, nil
			}},
		{Name: "project new", Form: "project new <name> [--ds <id>]", Desc: "create project",
			Run: cmdNew},
		{Name: "project support-js", Form: "project support-js <project> [--path p]", Desc: "write the Design Components runtime", Run: cmdSupportJS},
		{Name: "project sharing", Form: "project sharing <project> [--scope s] [--link-permission p]", Desc: "scope and link permission", Run: cmdSharing},
	},
}

// projectRow is list_projects' measured element (PROTOCOL.md, list_projects).
type projectRow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// decodeProjects returns the rows only when the reply matches the shape
// PROTOCOL.md measured. That claim is measured, not guaranteed, and three
// protocol details have already been guessed wrong — so an unrecognised reply
// is passed through verbatim rather than rendered from a guess.
func decodeProjects(text string) ([]projectRow, bool) {
	var rows []projectRow
	if err := json.Unmarshal([]byte(text), &rows); err != nil {
		return nil, false
	}
	for _, r := range rows {
		if r.ID == "" {
			return nil, false
		}
	}
	return rows, true
}

func cmdProjects(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("project ls")
	asJSON := cmd.JSONFlag(flags)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	if err := cmd.NoExtra(pos, "project ls"); err != nil {
		return err
	}

	text, err := c.CallTool(ctx, "list_projects", map[string]any{})
	if err != nil {
		return err
	}

	rows, ok := decodeProjects(text)
	if !ok {
		// Sanitised here too: an unrecognised reply is still server text, and
		// the fallback is exactly the path a hostile reply would take.
		fmt.Println(fmtutil.Printable(strings.TrimSpace(text)))
		return nil
	}

	if *asJSON {
		b, err := json.Marshal(rows)
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}

	// The id first: names hold spaces, so it is the only column order that
	// survives `awk '{print $1}'`. The name is server-supplied, hence untrusted
	// (invariant 7).
	for _, r := range rows {
		fmt.Printf("%-36s  %s\n", r.ID, fmtutil.Printable(r.Name))
	}
	fmt.Printf("%d projects\n", len(rows))
	return nil
}

func cmdNew(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("project new")
	ds := flags.String("ds", "", "design system id to attach")
	asJSON := cmd.JSONFlag(flags)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	name, rest, err := cmd.Need1(pos, "project new <name> [--ds <id>]")
	if err != nil {
		return err
	}
	if err := cmd.NoExtra(rest, "project new <name> [--ds <id>]"); err != nil {
		return err
	}
	a := map[string]any{"name": name}
	if *ds != "" {
		a["design_system_id"] = *ds
	}
	return cmd.Emit(ctx, c, "create_project", a, *asJSON)
}
