package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/fmtutil"
	"github.com/somework/dsx/internal/mcp"
)

// One thin function per MCP tool. They exist to spell the arguments out, not to
// add behaviour: `dsx raw` is the escape hatch for anything not wrapped here,
// and a wrapper that started interpreting replies would make the two disagree.
//
// Every one takes --json. Under it, stdout is one JSON document -- see emit.

func cmdNew(ctx context.Context, c *mcp.Client, args []string) error {
	flags := newFlagSet("new")
	ds := flags.String("ds", "", "design system id to attach")
	asJSON := jsonFlag(flags)
	pos, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	name, _, err := need1(pos, "new <name> [--ds <id>]")
	if err != nil {
		return err
	}
	a := map[string]any{"name": name}
	if *ds != "" {
		a["design_system_id"] = *ds
	}
	return emit(ctx, c, "create_project", a, *asJSON)
}

func cmdLs(ctx context.Context, c *mcp.Client, args []string) error {
	return emitFlagged(ctx, c, "ls", args, func(pos []string) (string, map[string]any, error) {
		project, rest, err := need1(pos, "ls <project> [path]")
		if err != nil {
			return "", nil, err
		}
		a := map[string]any{"project_id": project}
		if len(rest) > 0 {
			a["path"] = rest[0]
		}
		return "list_files", a, nil
	})
}

func cmdTree(ctx context.Context, c *mcp.Client, args []string) error {
	flags := newFlagSet("tree")
	var (
		jobs   = flags.Int("j", 8, "concurrency")
		asJSON = jsonFlag(flags)
	)
	pos, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	project, _, err := need1(pos, "tree <project>")
	if err != nil {
		return err
	}
	files, err := WalkTree(ctx, c, project, *jobs)
	if err != nil {
		return err
	}
	if *asJSON {
		out := make([]RemoteEntry, 0, len(files))
		for _, p := range SortedPaths(files) {
			out = append(out, files[p])
		}
		b, err := json.Marshal(out)
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	var total int64
	for _, p := range SortedPaths(files) {
		e := files[p]
		total += e.Size
		fmt.Printf("%-10s %16s  %s\n", fmtutil.Bytes(e.Size), e.Etag, p)
	}
	fmt.Printf("%d files, %s\n", len(files), fmtutil.Bytes(total))
	return nil
}

func cmdCat(ctx context.Context, c *mcp.Client, args []string) error {
	flags := newFlagSet("cat")
	var (
		out    = flags.String("out", "", "write to this file instead of stdout")
		asJSON = jsonFlag(flags)
	)
	pos, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	project, path, _, err := need2(pos, "cat <project> <path> [--out f]")
	if err != nil {
		return err
	}
	body, etag, err := c.ReadFull(ctx, project, path)
	if err != nil {
		return err
	}
	if *out != "" {
		if err := os.WriteFile(*out, []byte(body), 0o644); err != nil {
			return err
		}
		if *asJSON {
			b, err := json.Marshal(map[string]any{"path": path, "etag": etag, "bytes": len(body), "out": *out})
			if err != nil {
				return err
			}
			fmt.Println(string(b))
		}
		return nil
	}
	if *asJSON {
		// The body goes in a JSON string rather than raw on stdout: a caller
		// that asked for JSON is running a parser, and a CSS file is not one.
		b, err := json.Marshal(map[string]any{"path": path, "etag": etag, "content": body})
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	_, err = io.WriteString(os.Stdout, body)
	return err
}

func cmdPut(ctx context.Context, c *mcp.Client, args []string) error {
	flags := newFlagSet("put")
	var (
		ifMatch = flags.String("if-match", "", `etag guard; "0" asserts the path is new`)
		plan    = flags.String("plan", "", "plan_token from `dsx plan`")
		asJSON  = jsonFlag(flags)
	)
	pos, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	project, path, rest, err := need2(pos, "put <project> <path> [file]")
	if err != nil {
		return err
	}

	var body []byte
	if len(rest) > 0 {
		body, err = os.ReadFile(rest[0])
	} else {
		body, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		return err
	}

	file := map[string]any{
		"path":     path,
		"data":     base64.StdEncoding.EncodeToString(body),
		"encoding": "base64",
	}
	if *ifMatch != "" {
		file["if_match"] = *ifMatch
	}
	a := map[string]any{"project_id": project, "files": []any{file}}
	if *plan != "" {
		a["plan_token"] = *plan
	}
	// Self-authorise exactly the way push does. A project with no standing
	// grant is the default, and `dsx put` used to stop dead on the 403 that
	// `dsx push` recovers from silently.
	return emitWrite(ctx, c, "write_files", a, project, []string{path}, *asJSON)
}

// emitWrite is emit for a tool that writes: it recovers from the server's
// demand for a standing project grant before printing.
func emitWrite(ctx context.Context, c *mcp.Client, tool string, args map[string]any, projectID string, paths []string, asJSON bool) error {
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
		text, err = CallWithGrant(ctx, c, tool, args, projectID, paths)
	}
	if err != nil {
		if conflicts, ok := mcp.ConflictFromToolError(err); ok {
			return dsxerr.Conflict(conflicts, "the server changed since dsx read it; nothing was written")
		}
		return err
	}
	fmt.Println(jsonSafe(text, asJSON))
	return nil
}

func cmdRm(ctx context.Context, c *mcp.Client, args []string) error {
	flags := newFlagSet("rm")
	asJSON := jsonFlag(flags)
	pos, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	project, rest, err := need1(pos, "rm <project> <path...>")
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return dsxerr.Usage("rm <project> <path...>")
	}

	// Deletes always need a path-scoped plan_token naming every path; a
	// project-scoped one is refused.
	token, err := PlanToken(ctx, c, map[string]any{"project_id": project, "deletes": rest})
	if err != nil {
		return err
	}
	return emit(ctx, c, "delete_files", map[string]any{
		"project_id": project,
		"plan_token": token,
		"paths":      rest,
	}, *asJSON)
}

func cmdCp(ctx context.Context, c *mcp.Client, args []string) error {
	flags := newFlagSet("cp")
	var (
		from    = flags.String("from", "", "source project (omit for same-project copy)")
		ifMatch = flags.String("if-match", "", "etag guard on a single-file dest")
		plan    = flags.String("plan", "", "plan_token from `dsx plan`")
		asJSON  = jsonFlag(flags)
	)
	pos, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	project, src, rest, err := need2(pos, "cp <project> <src> <dst> [--from <project>]")
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return dsxerr.Usage("cp <project> <src> <dst> [--from <project>]")
	}

	file := map[string]any{"src": src, "dest": rest[0]}
	if *from != "" {
		file["src_project_id"] = *from
	}
	if *ifMatch != "" {
		file["if_match"] = *ifMatch
	}
	a := map[string]any{"project_id": project, "files": []any{file}}
	if *plan != "" {
		a["plan_token"] = *plan
	}
	return emitWrite(ctx, c, "copy_files", a, project, []string{rest[0]}, *asJSON)
}

func cmdPlan(ctx context.Context, c *mcp.Client, args []string) error {
	flags := newFlagSet("plan")
	var (
		writes  = flags.String("writes", "", "comma-separated paths to authorise for writing")
		deletes = flags.String("deletes", "", "comma-separated paths to authorise for deletion")
		scope   = flags.String("scope", "", `"paths" (default) or "project"`)
		asJSON  = jsonFlag(flags)
	)
	pos, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	project, _, err := need1(pos, "plan <project> [--writes a,b] [--deletes c,d] [--scope project]")
	if err != nil {
		return err
	}
	a := map[string]any{"project_id": project}
	if v := splitList(*writes); len(v) > 0 {
		a["writes"] = v
	}
	if v := splitList(*deletes); len(v) > 0 {
		a["deletes"] = v
	}
	if *scope != "" {
		a["scope"] = *scope
	}
	return emit(ctx, c, "finalize_plan", a, *asJSON)
}

func cmdPreview(ctx context.Context, c *mcp.Client, args []string) error {
	flags := newFlagSet("preview")
	var (
		render     = flags.Bool("render", false, "render the preview")
		validators = flags.String("validators", "", "comma-separated validators")
		asJSON     = jsonFlag(flags)
	)
	pos, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	project, path, _, err := need2(pos, "preview <project> <path>")
	if err != nil {
		return err
	}
	a := map[string]any{"project_id": project, "path": path}
	if *render {
		a["render"] = true
	}
	if v := splitList(*validators); len(v) > 0 {
		a["validators"] = v
	}
	return emit(ctx, c, "render_preview", a, *asJSON)
}

// defaultSupportJS is where create_support_js writes when `path` is omitted.
// reference/mcp-tools.json: "defaults to \"support.js\" at the project root".
const defaultSupportJS = "support.js"

func cmdSupportJS(ctx context.Context, c *mcp.Client, args []string) error {
	flags := newFlagSet("support-js")
	var (
		path    = flags.String("path", "", "destination path")
		ifMatch = flags.String("if-match", "", "etag guard")
		plan    = flags.String("plan", "", "plan_token")
		asJSON  = jsonFlag(flags)
	)
	pos, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	project, _, err := need1(pos, "support-js <project> [--path p]")
	if err != nil {
		return err
	}
	a := map[string]any{"project_id": project}
	for k, v := range map[string]string{"path": *path, "if_match": *ifMatch, "plan_token": *plan} {
		if v != "" {
			a[k] = v
		}
	}
	// The server's own schema says path "defaults to support.js at the project
	// root", so there is always something to name in a plan. Believing otherwise
	// left the documented form -- `dsx support-js <project>` -- unable to
	// self-authorise, and it exited 1 on a project with no standing grant, which
	// is the default.
	dest := *path
	if dest == "" {
		dest = defaultSupportJS
	}
	return emitWrite(ctx, c, "create_support_js", a, project, []string{dest}, *asJSON)
}

func cmdConv(ctx context.Context, c *mcp.Client, args []string) error {
	flags := newFlagSet("conv")
	chat := flags.String("chat", "", "chat id")
	asJSON := jsonFlag(flags)
	pos, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	project, _, err := need1(pos, "conv <project> [--chat id]")
	if err != nil {
		return err
	}
	a := map[string]any{"project_id": project}
	if *chat != "" {
		a["chat_id"] = *chat
	}
	return emit(ctx, c, "get_conversation", a, *asJSON)
}

func cmdConvPut(ctx context.Context, c *mcp.Client, args []string) error {
	flags := newFlagSet("conv-put")
	var (
		msgFile = flags.String("messages", "", "JSON file holding the messages array (required)")
		chat    = flags.String("chat", "", "chat id")
		title   = flags.String("title", "", "conversation title")
		appnd   = flags.Bool("append", false, "append instead of replacing")
		through = flags.Int("synced-through-idx", -1, "synced_through_idx")
		asJSON  = jsonFlag(flags)
	)
	pos, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	project, _, err := need1(pos, "conv-put <project> --messages <file.json>")
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
	return emit(ctx, c, "put_conversation", a, *asJSON)
}

func cmdMemberAdd(ctx context.Context, c *mcp.Client, args []string) error {
	flags := newFlagSet("member-add")
	var (
		role   = flags.String("role", "", "role (required)")
		email  = flags.String("email", "", "invitee email")
		uuid   = flags.String("uuid", "", "invitee account uuid")
		asJSON = jsonFlag(flags)
	)
	pos, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	project, _, err := need1(pos, "member-add <project> --role <r> [--email e] [--uuid u]")
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
	return emit(ctx, c, "add_member", a, *asJSON)
}

func cmdMemberRm(ctx context.Context, c *mcp.Client, args []string) error {
	return emitFlagged(ctx, c, "member-rm", args, func(pos []string) (string, map[string]any, error) {
		project, uuid, _, err := need2(pos, "member-rm <project> <uuid>")
		if err != nil {
			return "", nil, err
		}
		return "remove_member", map[string]any{"project_id": project, "account_uuid": uuid}, nil
	})
}

func cmdMemberRole(ctx context.Context, c *mcp.Client, args []string) error {
	return emitFlagged(ctx, c, "member-role", args, func(pos []string) (string, map[string]any, error) {
		project, uuid, rest, err := need2(pos, "member-role <project> <uuid> <role>")
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
	flags := newFlagSet("sharing")
	var (
		scope  = flags.String("scope", "", "sharing scope")
		link   = flags.String("link-permission", "", "link permission")
		asJSON = jsonFlag(flags)
	)
	pos, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	project, _, err := need1(pos, "sharing <project> [--scope s] [--link-permission p]")
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
	return emit(ctx, c, "update_sharing", a, *asJSON)
}

func cmdPrompt(ctx context.Context, c *mcp.Client, args []string) error {
	flags := newFlagSet("prompt")
	var (
		project = flags.String("project", "", "project id")
		ds      = flags.String("ds", "", "design system id")
		asJSON  = jsonFlag(flags)
	)
	pos, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	// The project goes in --project, not as a positional, so a bare id has to be
	// refused rather than quietly answered with the generic prompt.
	if err := noPositionals(pos, "prompt [--project id] [--ds id]"); err != nil {
		return err
	}
	a := map[string]any{}
	if *project != "" {
		a["project_id"] = *project
	}
	if *ds != "" {
		a["design_system_id"] = *ds
	}
	return emit(ctx, c, "get_claude_design_prompt", a, *asJSON)
}

func cmdTools(ctx context.Context, c *mcp.Client, args []string) error {
	flags := newFlagSet("tools")
	var (
		full   = flags.Bool("schema", false, "print full JSON schemas")
		asJSON = jsonFlag(flags)
	)
	pos, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	// `dsx tools <name>` looks like it would filter, and does not.
	if err := noPositionals(pos, "tools [--schema]"); err != nil {
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
		fmt.Printf("%-26s %s\n", t.Name, fmtutil.Truncate(firstLine(t.Description), 90))
	}
	return nil
}

func cmdRaw(ctx context.Context, c *mcp.Client, args []string) error {
	flags := newFlagSet("raw")
	asJSON := jsonFlag(flags)
	pos, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	tool, rest, err := need1(pos, `raw <tool> '<json-args>'`)
	if err != nil {
		return err
	}
	a := map[string]any{}
	if len(rest) > 0 && rest[0] != "" {
		if err := json.Unmarshal([]byte(rest[0]), &a); err != nil {
			return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: "arguments must be a JSON object", Err: err}
		}
		// The literal `null` unmarshals into a map without error and leaves it
		// nil, so the guard above waves it through and dsx sends
		// "arguments": null. Every other non-object is refused; this one was an
		// inconsistent boundary, not a decision.
		if a == nil {
			return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: "arguments must be a JSON object, not null"}
		}
	}
	return emit(ctx, c, tool, a, *asJSON)
}

func firstLine(s string) string {
	for i := range len(s) {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}
