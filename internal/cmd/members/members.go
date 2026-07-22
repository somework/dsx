package members

import (
	"context"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/mcp"
)

var Group = cmd.Group{
	Title: "MEMBERS",
	Noun:  "member",
	Desc:  "who may open the project, and with what role",
	Cmds: []cmd.Command{
		{Name: "member ls", Form: "member ls <project>", Desc: "who is on the project",
			Tool: func(pos []string) (string, map[string]any, error) {
				id, rest, err := cmd.Need1(pos, "member ls <project>")
				if err != nil {
					return "", nil, err
				}
				if err := cmd.NoExtra(rest, "member ls <project>"); err != nil {
					return "", nil, err
				}
				return "list_members", map[string]any{"project_id": id}, nil
			}},
		{Name: "member add", Form: "member add <project> --role <r> (--email e | --uuid u)", Desc: "invite someone", Run: cmdMemberAdd},
		{Name: "member rm", Form: "member rm <project> <uuid>", Desc: "remove someone", Run: cmdMemberRm},
		{Name: "member role", Form: "member role <project> <uuid> <role>", Desc: "change a role", Run: cmdMemberRole},
	},
}

func cmdMemberAdd(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("member add")
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
	project, rest, err := cmd.Need1(pos, "member add <project> --role <r> (--email e | --uuid u)")
	if err != nil {
		return err
	}
	if err := cmd.NoExtra(rest, "member add <project> --role <r> (--email e | --uuid u)"); err != nil {
		return err
	}
	if *role == "" {
		return dsxerr.Usage("member add <project> --role <r> (--email e | --uuid u)")
	}
	if (*email == "") == (*uuid == "") {
		return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: "give --email or --uuid, not both"}
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
	return cmd.EmitFlagged(ctx, c, "member rm", args, func(pos []string) (string, map[string]any, error) {
		project, uuid, rest, err := cmd.Need2(pos, "member rm <project> <uuid>")
		if err != nil {
			return "", nil, err
		}
		if err := cmd.NoExtra(rest, "member rm <project> <uuid>"); err != nil {
			return "", nil, err
		}
		return "remove_member", map[string]any{"project_id": project, "account_uuid": uuid}, nil
	})
}

func cmdMemberRole(ctx context.Context, c *mcp.Client, args []string) error {
	return cmd.EmitFlagged(ctx, c, "member role", args, func(pos []string) (string, map[string]any, error) {
		project, uuid, rest, err := cmd.Need2(pos, "member role <project> <uuid> <role>")
		if err != nil {
			return "", nil, err
		}
		if len(rest) == 0 {
			return "", nil, dsxerr.Usage("member role <project> <uuid> <role>")
		}
		if err := cmd.NoExtra(rest[1:], "member role <project> <uuid> <role>"); err != nil {
			return "", nil, err
		}
		return "update_member_role", map[string]any{
			"project_id": project, "account_uuid": uuid, "role": rest[0],
		}, nil
	})
}
