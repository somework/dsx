package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

const usage = `dsx — Claude Design sync. Reads Claude Code's own OAuth token from the macOS Keychain.

SYNC (etag-aware; unchanged files cost no request at all)
  dsx pull  <project> <dir> [--prune] [--force] [-n] [-j N]
  dsx push  <project> <dir> [--prune] [--force] [-n] [-j N]
  dsx status <project> <dir>            what a sync would do; transfers nothing

PROJECTS
  dsx projects                          list projects
  dsx project <id>                      project detail
  dsx new <name> [--ds <id>]            create project
  dsx systems                           list design systems

FILES
  dsx ls <project> [path]               list one directory
  dsx tree <project>                    every file, recursive, with etags
  dsx cat <project> <path> [--out f]    read a file (stdout by default)
  dsx put <project> <path> [file]       write a file (stdin when file is omitted)
  dsx rm <project> <path...>            delete files
  dsx cp <project> <src> <dst> [--from <project>]

PLANS / PREVIEW
  dsx plan <project> [--writes a,b] [--deletes c,d] [--scope project]
  dsx preview <project> <path> [--render] [--validators a,b]
  dsx support-js <project> [--path p]

CONVERSATION
  dsx conv <project> [--chat id]
  dsx conv-put <project> --messages <file.json> [--chat id] [--title t] [--append]

MEMBERS / SHARING
  dsx members <project>
  dsx member-add <project> --role <r> [--email e] [--uuid u]
  dsx member-rm <project> <uuid>
  dsx member-role <project> <uuid> <role>
  dsx sharing <project> [--scope s] [--link-permission p]

ESCAPE HATCH
  dsx tools                             tool names and schemas from the server
  dsx raw <tool> '<json-args>'          call any tool verbatim
  dsx auth                              token scopes and expiry (never the token)

GLOBAL
  --json      machine-readable output      -q  suppress the summary line
  -j N        concurrency (default 8)      -n  dry run

Env: DSX_TOKEN overrides the Keychain. DSX_ENDPOINT overrides the MCP URL.`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dsx: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		fmt.Println(usage)
		return nil
	}

	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "-h", "--help", "help":
		fmt.Println(usage)
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cmd == "auth" {
		return cmdAuth()
	}

	token, err := loadToken()
	if err != nil {
		return err
	}
	c := newClient(token)

	switch cmd {
	case "pull", "push", "status":
		return cmdSync(ctx, c, cmd, args)
	case "projects":
		return emit(ctx, c, "list_projects", map[string]any{})
	case "project":
		id, rest, err := need1(args, "project <id>")
		if err != nil {
			return err
		}
		_ = rest
		return emit(ctx, c, "get_project", map[string]any{"project_id": id})
	case "systems":
		return emit(ctx, c, "list_design_systems", map[string]any{})
	case "new":
		return cmdNew(ctx, c, args)
	case "ls":
		return cmdLs(ctx, c, args)
	case "tree":
		return cmdTree(ctx, c, args)
	case "cat":
		return cmdCat(ctx, c, args)
	case "put":
		return cmdPut(ctx, c, args)
	case "rm":
		return cmdRm(ctx, c, args)
	case "cp":
		return cmdCp(ctx, c, args)
	case "plan":
		return cmdPlan(ctx, c, args)
	case "preview":
		return cmdPreview(ctx, c, args)
	case "support-js":
		return cmdSupportJS(ctx, c, args)
	case "conv":
		return cmdConv(ctx, c, args)
	case "conv-put":
		return cmdConvPut(ctx, c, args)
	case "members":
		id, _, err := need1(args, "members <project>")
		if err != nil {
			return err
		}
		return emit(ctx, c, "list_members", map[string]any{"project_id": id})
	case "member-add":
		return cmdMemberAdd(ctx, c, args)
	case "member-rm":
		return cmdMemberRm(ctx, c, args)
	case "member-role":
		return cmdMemberRole(ctx, c, args)
	case "sharing":
		return cmdSharing(ctx, c, args)
	case "prompt":
		return cmdPrompt(ctx, c, args)
	case "tools":
		return cmdTools(ctx, c, args)
	case "raw":
		return cmdRaw(ctx, c, args)
	default:
		return fmt.Errorf("unknown command %q — run `dsx help`", cmd)
	}
}

func need1(args []string, form string) (string, []string, error) {
	if len(args) < 1 {
		return "", nil, fmt.Errorf("usage: dsx %s", form)
	}
	return args[0], args[1:], nil
}

func need2(args []string, form string) (string, string, []string, error) {
	if len(args) < 2 {
		return "", "", nil, fmt.Errorf("usage: dsx %s", form)
	}
	return args[0], args[1], args[2:], nil
}

// emit calls a tool and prints its text result verbatim.
func emit(ctx context.Context, c *client, tool string, args map[string]any) error {
	text, err := c.callTool(ctx, tool, args)
	if err != nil {
		return err
	}
	fmt.Println(text)
	return nil
}

func cmdAuth() error {
	scopes, exp, err := tokenInfo()
	if err != nil {
		return err
	}
	fmt.Printf("scopes:  %v\nexpires: %s\n", scopes, exp.Format("2006-01-02T15:04:05Z07:00"))
	return nil
}

func cmdSync(ctx context.Context, c *client, mode string, args []string) error {
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	var (
		prune  = fs.Bool("prune", false, "remove files absent on the other side")
		force  = fs.Bool("force", false, "overwrite conflicts")
		dry    = fs.Bool("n", false, "dry run")
		jobs   = fs.Int("j", 8, "concurrency")
		asJSON = fs.Bool("json", false, "JSON output")
		quiet  = fs.Bool("q", false, "suppress summary")
	)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	project, dir, _, err := need2(pos, mode+" <project> <dir>")
	if err != nil {
		return err
	}
	if *jobs < 1 {
		*jobs = 1
	}

	dryRun := *dry || mode == "status"
	if mode != "push" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	if mode == "push" {
		rep, err := runPush(ctx, c, pushOpts{
			projectID: project, dir: dir, concurrency: *jobs,
			prune: *prune, force: *force, dryRun: dryRun,
		})
		if err != nil {
			return err
		}
		if !*quiet {
			fmt.Println(rep.render(*asJSON))
		}
		return nil
	}

	pullRep, err := runPull(ctx, c, pullOpts{
		projectID: project, dir: dir, concurrency: *jobs,
		prune: *prune, force: *force, dryRun: dryRun,
	})
	if err != nil {
		return err
	}

	if mode == "pull" {
		if !*quiet {
			fmt.Println(pullRep.render(*asJSON))
		}
		return nil
	}

	// `status` is the only mode that reports both directions. Neither side
	// moves bytes, so the two dry runs cannot interfere.
	pushRep, err := runPush(ctx, c, pushOpts{
		projectID: project, dir: dir, concurrency: *jobs,
		prune: *prune, force: *force, dryRun: true,
	})
	if err != nil {
		return err
	}
	if *quiet {
		return nil
	}
	if *asJSON {
		b, err := json.Marshal(map[string]any{"pull": pullRep, "push": pushRep})
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	fmt.Println("pull: " + pullRep.render(false))
	fmt.Println("push: " + pushRep.render(false))
	return nil
}
