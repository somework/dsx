package members

import (
	"context"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/mcp"
)

var Group = cmd.Group{
	Title: "MEMBERS / SHARING",
	Cmds: []cmd.Command{
		{Name: "members", Form: "members <project>",
			Tool: func(pos []string) (string, map[string]any, error) {
				id, _, err := cmd.Need1(pos, "members <project>")
				if err != nil {
					return "", nil, err
				}
				return "list_members", map[string]any{"project_id": id}, nil
			}},
		{Name: "member-add", Form: "member-add <project> --role <r> [--email e] [--uuid u]", Run: cmdMemberAdd},
		{Name: "member-rm", Form: "member-rm <project> <uuid>", Run: cmdMemberRm},
		{Name: "member-role", Form: "member-role <project> <uuid> <role>", Run: cmdMemberRole},
		{Name: "sharing", Form: "sharing <project> [--scope s] [--link-permission p]", Run: cmdSharing},
	},
}

func cmdMemberAdd(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("member-add")
	var (
		role   = flags.String("role", "", "role (required)")
		email  = flags.String("email", "", "invitee email")
		uuid   = flags.String("uuid", "", "invitee account uuid")
		asJSON = cmd.JSONFlag(flags)
	)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	project, _, err := cmd.Need1(pos, "member-add <project> --role <r> [--email e] [--uuid u]")
	if err != nil {
		return err
	}
	if *role == "" {
		return dsxerr.Usage("member-add <project> --role <r> [--email e] [--uuid u]")
	}
	if *email == "" && *uuid == "" {
		return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: "give --email or --uuid"}
	}
	a := map[string]any{"project_id": project, "role": *role}
	if *email != "" {
		a["email"] = *email
	}
	if *uuid != "" {
		a["account_uuid"] = *uuid
	}
	return cmd.Emit(ctx, c, "add_member", a, *asJSON)
}

func cmdMemberRm(ctx context.Context, c *mcp.Client, args []string) error {
	return cmd.EmitFlagged(ctx, c, "member-rm", args, func(pos []string) (string, map[string]any, error) {
		project, uuid, _, err := cmd.Need2(pos, "member-rm <project> <uuid>")
		if err != nil {
			return "", nil, err
		}
		return "remove_member", map[string]any{"project_id": project, "account_uuid": uuid}, nil
	})
}

func cmdMemberRole(ctx context.Context, c *mcp.Client, args []string) error {
	return cmd.EmitFlagged(ctx, c, "member-role", args, func(pos []string) (string, map[string]any, error) {
		project, uuid, rest, err := cmd.Need2(pos, "member-role <project> <uuid> <role>")
		if err != nil {
			return "", nil, err
		}
		if len(rest) == 0 {
			return "", nil, dsxerr.Usage("member-role <project> <uuid> <role>")
		}
		return "update_member_role", map[string]any{
			"project_id": project, "account_uuid": uuid, "role": rest[0],
		}, nil
	})
}

func cmdSharing(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("sharing")
	var (
		scope  = flags.String("scope", "", "sharing scope")
		link   = flags.String("link-permission", "", "link permission")
		asJSON = cmd.JSONFlag(flags)
	)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	project, _, err := cmd.Need1(pos, "sharing <project> [--scope s] [--link-permission p]")
	if err != nil {
		return err
	}
	a := map[string]any{"project_id": project}
	if *scope != "" {
		a["scope"] = *scope
	}
	if *link != "" {
		a["link_permission"] = *link
	}
	return cmd.Emit(ctx, c, "update_sharing", a, *asJSON)
}
