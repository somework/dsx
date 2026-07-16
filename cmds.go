package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
)

func cmdNew(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	ds := fs.String("ds", "", "design system id to attach")
	pos, err := parseArgs(fs, args)
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
	return emit(ctx, c, "create_project", a)
}

func cmdLs(ctx context.Context, c *client, args []string) error {
	project, rest, err := need1(args, "ls <project> [path]")
	if err != nil {
		return err
	}
	a := map[string]any{"project_id": project}
	if len(rest) > 0 {
		a["path"] = rest[0]
	}
	return emit(ctx, c, "list_files", a)
}

func cmdTree(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("tree", flag.ContinueOnError)
	var (
		jobs   = fs.Int("j", 8, "concurrency")
		asJSON = fs.Bool("json", false, "JSON output")
	)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	project, _, err := need1(pos, "tree <project>")
	if err != nil {
		return err
	}
	files, err := c.walkTree(ctx, project, *jobs)
	if err != nil {
		return err
	}
	if *asJSON {
		out := make([]remoteEntry, 0, len(files))
		for _, p := range sortedPaths(files) {
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
	for _, p := range sortedPaths(files) {
		e := files[p]
		total += e.Size
		fmt.Printf("%-10s %16s  %s\n", humanBytes(e.Size), e.Etag, p)
	}
	fmt.Printf("%d files, %s\n", len(files), humanBytes(total))
	return nil
}

func cmdCat(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("cat", flag.ContinueOnError)
	out := fs.String("out", "", "write to this file instead of stdout")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	project, path, _, err := need2(pos, "cat <project> <path> [--out f]")
	if err != nil {
		return err
	}
	body, _, err := c.readFull(ctx, project, path)
	if err != nil {
		return err
	}
	if *out != "" {
		return os.WriteFile(*out, []byte(body), 0o644)
	}
	_, err = io.WriteString(os.Stdout, body)
	return err
}

func cmdPut(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("put", flag.ContinueOnError)
	var (
		ifMatch = fs.String("if-match", "", `etag guard; "0" asserts the path is new`)
		plan    = fs.String("plan", "", "plan_token from `dsx plan`")
	)
	pos, err := parseArgs(fs, args)
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
	return emit(ctx, c, "write_files", a)
}

func cmdRm(ctx context.Context, c *client, args []string) error {
	project, rest, err := need1(args, "rm <project> <path...>")
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fmt.Errorf("usage: dsx rm <project> <path...>")
	}

	// Deletes always need a path-scoped plan_token naming every path.
	text, err := c.callTool(ctx, "finalize_plan", map[string]any{
		"project_id": project,
		"deletes":    rest,
	})
	if err != nil {
		return fmt.Errorf("finalize_plan: %w", err)
	}
	var plan struct {
		PlanToken string `json:"plan_token"`
	}
	if err := json.Unmarshal([]byte(text), &plan); err != nil || plan.PlanToken == "" {
		return fmt.Errorf("finalize_plan returned no plan_token: %s", truncate(text, 200))
	}
	return emit(ctx, c, "delete_files", map[string]any{
		"project_id": project,
		"plan_token": plan.PlanToken,
		"paths":      rest,
	})
}

func cmdCp(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("cp", flag.ContinueOnError)
	var (
		from    = fs.String("from", "", "source project (omit for same-project copy)")
		ifMatch = fs.String("if-match", "", "etag guard on a single-file dest")
		plan    = fs.String("plan", "", "plan_token from `dsx plan`")
	)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	project, src, rest, err := need2(pos, "cp <project> <src> <dst> [--from <project>]")
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fmt.Errorf("usage: dsx cp <project> <src> <dst> [--from <project>]")
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
	return emit(ctx, c, "copy_files", a)
}

func cmdPlan(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	var (
		writes  = fs.String("writes", "", "comma-separated paths to authorise for writing")
		deletes = fs.String("deletes", "", "comma-separated paths to authorise for deletion")
		scope   = fs.String("scope", "", `"paths" (default) or "project"`)
	)
	pos, err := parseArgs(fs, args)
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
	return emit(ctx, c, "finalize_plan", a)
}

func cmdPreview(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("preview", flag.ContinueOnError)
	var (
		render     = fs.Bool("render", false, "render the preview")
		validators = fs.String("validators", "", "comma-separated validators")
	)
	pos, err := parseArgs(fs, args)
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
	return emit(ctx, c, "render_preview", a)
}

func cmdSupportJS(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("support-js", flag.ContinueOnError)
	var (
		path    = fs.String("path", "", "destination path")
		ifMatch = fs.String("if-match", "", "etag guard")
		plan    = fs.String("plan", "", "plan_token")
	)
	pos, err := parseArgs(fs, args)
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
	return emit(ctx, c, "create_support_js", a)
}

func cmdConv(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("conv", flag.ContinueOnError)
	chat := fs.String("chat", "", "chat id")
	pos, err := parseArgs(fs, args)
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
	return emit(ctx, c, "get_conversation", a)
}

func cmdConvPut(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("conv-put", flag.ContinueOnError)
	var (
		msgFile = fs.String("messages", "", "JSON file holding the messages array (required)")
		chat    = fs.String("chat", "", "chat id")
		title   = fs.String("title", "", "conversation title")
		appnd   = fs.Bool("append", false, "append instead of replacing")
		through = fs.Int("synced-through-idx", -1, "synced_through_idx")
	)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	project, _, err := need1(pos, "conv-put <project> --messages <file.json>")
	if err != nil {
		return err
	}
	if *msgFile == "" {
		return fmt.Errorf("--messages <file.json> is required")
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
	return emit(ctx, c, "put_conversation", a)
}

func cmdMemberAdd(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("member-add", flag.ContinueOnError)
	var (
		role  = fs.String("role", "", "role (required)")
		email = fs.String("email", "", "invitee email")
		uuid  = fs.String("uuid", "", "invitee account uuid")
	)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	project, _, err := need1(pos, "member-add <project> --role <r> [--email e] [--uuid u]")
	if err != nil {
		return err
	}
	if *role == "" {
		return fmt.Errorf("--role is required")
	}
	if *email == "" && *uuid == "" {
		return fmt.Errorf("give --email or --uuid")
	}
	a := map[string]any{"project_id": project, "role": *role}
	if *email != "" {
		a["email"] = *email
	}
	if *uuid != "" {
		a["account_uuid"] = *uuid
	}
	return emit(ctx, c, "add_member", a)
}

func cmdMemberRm(ctx context.Context, c *client, args []string) error {
	project, uuid, _, err := need2(args, "member-rm <project> <uuid>")
	if err != nil {
		return err
	}
	return emit(ctx, c, "remove_member", map[string]any{"project_id": project, "account_uuid": uuid})
}

func cmdMemberRole(ctx context.Context, c *client, args []string) error {
	project, uuid, rest, err := need2(args, "member-role <project> <uuid> <role>")
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fmt.Errorf("usage: dsx member-role <project> <uuid> <role>")
	}
	return emit(ctx, c, "update_member_role", map[string]any{
		"project_id": project, "account_uuid": uuid, "role": rest[0],
	})
}

func cmdSharing(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("sharing", flag.ContinueOnError)
	var (
		scope = fs.String("scope", "", "sharing scope")
		link  = fs.String("link-permission", "", "link permission")
	)
	pos, err := parseArgs(fs, args)
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
	return emit(ctx, c, "update_sharing", a)
}

func cmdPrompt(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("prompt", flag.ContinueOnError)
	var (
		project = fs.String("project", "", "project id")
		ds      = fs.String("ds", "", "design system id")
	)
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	a := map[string]any{}
	if *project != "" {
		a["project_id"] = *project
	}
	if *ds != "" {
		a["design_system_id"] = *ds
	}
	return emit(ctx, c, "get_claude_design_prompt", a)
}

func cmdTools(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("tools", flag.ContinueOnError)
	full := fs.Bool("schema", false, "print full JSON schemas")
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	raw, err := c.rpc(ctx, "tools/list", map[string]any{})
	if err != nil {
		return err
	}
	if *full {
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
		return err
	}
	for _, t := range list.Tools {
		fmt.Printf("%-26s %s\n", t.Name, truncate(firstLine(t.Description), 90))
	}
	return nil
}

func cmdRaw(ctx context.Context, c *client, args []string) error {
	tool, rest, err := need1(args, `raw <tool> '<json-args>'`)
	if err != nil {
		return err
	}
	a := map[string]any{}
	if len(rest) > 0 && rest[0] != "" {
		if err := json.Unmarshal([]byte(rest[0]), &a); err != nil {
			return fmt.Errorf("arguments must be a JSON object: %w", err)
		}
	}
	return emit(ctx, c, tool, a)
}

func firstLine(s string) string {
	for i := range len(s) {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}
