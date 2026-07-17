package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/somework/dsx/internal/auth"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/mcp"
)

const usage = `dsx — Claude Design sync. Reads Claude Code's own OAuth token; never writes it.

SYNC (etag-aware; unchanged files cost no request at all)
  dsx pull  [<project>] [<dir>] [--prune] [--force] [-n] [-j N]
  dsx push  [<project>] [<dir>] [--prune] [--force] [-n] [-j N]
  dsx status [<project>] [<dir>]        what a sync would do; transfers nothing
  The project id is optional once <dir> holds a ledger; <dir> defaults to "."
  .dsxignore excludes paths from the sync, in both directions.

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
  dsx prompt [--project id] [--ds id]    the server's own Claude Design prompt
  dsx tools                             tool names and schemas from the server
  dsx raw <tool> '<json-args>'          call any tool verbatim

DIAGNOSTICS
  dsx help
  dsx auth                              token scopes and expiry (never the token)
  dsx doctor                            token, endpoint, clock skew
  dsx version                           version, revision, platform
  dsx completion <bash|zsh|fish>

GLOBAL
  --json      machine-readable output      -q  suppress the summary line
  -j N        concurrency (default 8)      -n  dry run

EXIT CODES
  0 ok   1 failed   2 usage   3 conflict (needs a human)
  4 transport (retry may help)   5 auth (run any ` + "`claude`" + ` command)

Env: DSX_TOKEN overrides the stored credential. DSX_ENDPOINT overrides the MCP URL.`

func main() {
	err := run()
	if err == nil {
		return
	}
	// The renderer runs here, outside every FlagSet, so that a failure raised
	// before flags were parsed still honours --json.
	fmt.Fprintln(os.Stderr, dsxerr.Render(err, dsxerr.JSONRequested(os.Args[1:])))
	os.Exit(dsxerr.ExitCodeFor(err))
}

func run() error {
	if len(os.Args) < 2 {
		fmt.Println(usage)
		return nil
	}

	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "-h", "--help", "help":
		return cmdHelp(args)
	case "-v", "--version", "version":
		return cmdVersion(args)
	case "completion":
		return cmdCompletion(args)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Reject an unknown command before reaching for the credential. Loading it
	// first meant a typo on a machine with no login was reported as an auth
	// failure -- dsx blaming the user's credentials for their spelling, and
	// exit 5 inviting a re-authentication that could not possibly help.
	if !isKnownCommand(cmd) {
		return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: "unknown command " + strconv.Quote(cmd) + " — run `dsx help`"}
	}

	if cmd == "auth" {
		return cmdAuth(args)
	}

	token, err := auth.LoadToken()
	if err != nil {
		return err
	}
	c := mcp.New(token)

	if cmd == "doctor" {
		return cmdDoctor(ctx, c, args)
	}

	switch cmd {
	case "pull", "push", "status":
		return cmdSync(ctx, c, cmd, args)
	case "projects":
		return emitFlagged(ctx, c, "projects", args, func([]string) (string, map[string]any, error) {
			return "list_projects", map[string]any{}, nil
		})
	case "project":
		return emitFlagged(ctx, c, "project", args, func(pos []string) (string, map[string]any, error) {
			id, _, err := need1(pos, "project <id>")
			if err != nil {
				return "", nil, err
			}
			return "get_project", map[string]any{"project_id": id}, nil
		})
	case "systems":
		return emitFlagged(ctx, c, "systems", args, func([]string) (string, map[string]any, error) {
			return "list_design_systems", map[string]any{}, nil
		})
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
		return emitFlagged(ctx, c, "members", args, func(pos []string) (string, map[string]any, error) {
			id, _, err := need1(pos, "members <project>")
			if err != nil {
				return "", nil, err
			}
			return "list_members", map[string]any{"project_id": id}, nil
		})
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
		// Unreachable while commandNames and this switch agree, which
		// TestEveryDispatchedCommandIsCompletable enforces. It stays as the
		// answer if they ever stop agreeing.
		return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: "command " + strconv.Quote(cmd) + " is listed but not wired — this is a dsx bug"}
	}
}

func need1(args []string, form string) (string, []string, error) {
	if len(args) < 1 {
		return "", nil, dsxerr.Usage(form)
	}
	return args[0], args[1:], nil
}

func need2(args []string, form string) (string, string, []string, error) {
	if len(args) < 2 {
		return "", "", nil, dsxerr.Usage(form)
	}
	return args[0], args[1], args[2:], nil
}

// emit calls a tool and prints its text result.
//
// Under --json, stdout is guaranteed to be one JSON document. Most tools
// already answer in JSON and are passed through untouched; the few that answer
// in prose are wrapped rather than handed to a caller that is about to run a
// parser over them. A guarantee with exceptions is not one an agent can use.
func emit(ctx context.Context, c *mcp.Client, tool string, args map[string]any, asJSON bool) error {
	text, err := c.CallTool(ctx, tool, args)
	if err != nil {
		return err
	}
	fmt.Println(jsonSafe(text, asJSON))
	return nil
}

func jsonSafe(text string, asJSON bool) string {
	if !asJSON {
		return text
	}
	if json.Valid([]byte(text)) {
		return text
	}
	b, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return text
	}
	return string(b)
}

// emitFlagged parses the standard --json flag, then calls the tool. It is the
// shape every passthrough command takes: an agent should not have to learn
// which subcommands happen to accept --json.
func emitFlagged(ctx context.Context, c *mcp.Client, name string, args []string, build func(pos []string) (string, map[string]any, error)) error {
	flags := newFlagSet(name)
	asJSON := jsonFlag(flags)
	pos, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	tool, toolArgs, err := build(pos)
	if err != nil {
		return err
	}
	return emit(ctx, c, tool, toolArgs, *asJSON)
}

// cmdAuth reports the credential's metadata. It must never render the token:
// this is the command most likely to be run with a terminal being recorded.
func cmdAuth(args []string) error {
	flags := newFlagSet("auth")
	asJSON := flags.Bool("json", false, "JSON output")
	if _, err := parseArgs(flags, args); err != nil {
		return err
	}
	// DSX_TOKEN overrides the stored credential for every other command, so it
	// has to override it here too. Reporting the stored credential's metadata
	// while the next request uses a different token is worse than reporting
	// nothing: this is the command someone runs to explain a 401.
	if t, _ := os.LookupEnv("DSX_TOKEN"); t != "" {
		if *asJSON {
			b, err := json.Marshal(map[string]any{"source": "DSX_TOKEN"})
			if err != nil {
				return err
			}
			fmt.Println(string(b))
			return nil
		}
		fmt.Println("source:  DSX_TOKEN (scopes and expiry are not knowable from a bare token)")
		return nil
	}

	scopes, exp, err := auth.TokenInfo()
	if err != nil {
		return err
	}
	if *asJSON {
		b, err := json.Marshal(map[string]any{
			"source":  "store",
			"scopes":  scopes,
			"expires": exp.Format(time.RFC3339),
		})
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}
	fmt.Printf("scopes:  %v\nexpires: %s\n", scopes, exp.Format(time.RFC3339))
	return nil
}

// cmdHelp prints the usage text.
//
// It takes --json for the same reason everything else does: the guarantee is
// that under --json stdout is one JSON document, and a guarantee with
// exceptions is not one an agent can use. It was dispatched before any FlagSet
// and printed prose regardless.
func cmdHelp(args []string) error {
	flags := newFlagSet("help")
	asJSON := jsonFlag(flags)
	if _, err := parseArgs(flags, args); err != nil {
		return err
	}
	if !*asJSON {
		fmt.Println(usage)
		return nil
	}
	b, err := json.Marshal(map[string]any{"usage": usage, "commands": commandNames})
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

// boundProject reports the project a directory is already pinned to, or "" if
// the directory carries no ledger yet.
func boundProject(dir string) (string, error) {
	st, err := LoadState(dir)
	if err != nil {
		return "", err
	}
	return st.ProjectID, nil
}

// resolveSyncTarget works out which project and directory a sync command means.
//
// The ledger already records the project id, so retyping a UUID on every sync
// is pure friction. Two positional arguments keep their old meaning exactly;
// fewer fall back to the ledger, which is the only place the binding is known.
func resolveSyncTarget(mode string, pos []string, bound func(string) (string, error)) (project, dir string, err error) {
	switch len(pos) {
	case 0:
		dir = "."
	case 1:
		dir = pos[0]
	case 2:
		return pos[0], pos[1], nil
	default:
		return "", "", dsxerr.Usage(mode + " [<project>] [<dir>]")
	}

	p, err := bound(dir)
	if err != nil {
		return "", "", err
	}
	if p == "" {
		return "", "", &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: fmt.Sprintf(
			"%s carries no dsx ledger, so its project is unknown — run `dsx %s <project> %s` once and it is remembered",
			dir, mode, dir)}
	}
	return p, dir, nil
}

// conflictOutcome turns reported conflicts into the exit status.
//
// A dry run was asked to move nothing, so refusing to move something is the
// answer it wanted, not a failure. A real run that refused did not do what it
// was told, and a caller that reads exit 0 there would carry on over the top of
// work that exists nowhere else.
func conflictOutcome(conflicts []string, dryRun bool, hint string) error {
	if dryRun || len(conflicts) == 0 {
		return nil
	}
	return dsxerr.Conflict(conflicts, hint)
}

func cmdSync(ctx context.Context, c *mcp.Client, mode string, args []string) error {
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
	project, dir, err := resolveSyncTarget(mode, pos, boundProject)
	if err != nil {
		return err
	}

	dryRun := *dry || mode == "status"
	if mode != "push" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	if mode == "push" {
		rep, err := Push(ctx, c, PushOpts{
			ProjectID: project, Dir: dir, Concurrency: *jobs,
			Prune: *prune, Force: *force, DryRun: dryRun,
		})
		if err != nil {
			return err
		}
		if !*quiet {
			fmt.Println(rep.Render(*asJSON))
		}
		return conflictOutcome(rep.Conflicts, dryRun,
			"server moved ahead; `dsx pull` first, or --force")
	}

	pullRep, err := Pull(ctx, c, PullOpts{
		ProjectID: project, Dir: dir, Concurrency: *jobs,
		Prune: *prune, Force: *force, DryRun: dryRun,
	})
	if err != nil {
		return err
	}

	if mode == "pull" {
		if !*quiet {
			fmt.Println(pullRep.Render(*asJSON))
		}
		return conflictOutcome(pullRep.Conflicts, dryRun,
			"local differs from the server, or was deleted there and edited here")
	}

	// `status` is the only mode that reports both directions. Neither side
	// moves bytes, so the two dry runs cannot interfere.
	pushRep, err := Push(ctx, c, PushOpts{
		ProjectID: project, Dir: dir, Concurrency: *jobs,
		Prune: *prune, Force: *force, DryRun: true,
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
	fmt.Println("pull: " + pullRep.Render(false))
	fmt.Println("push: " + pushRep.Render(false))
	return nil
}
