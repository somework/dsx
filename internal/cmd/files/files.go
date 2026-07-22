package files

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/fmtutil"
	"github.com/somework/dsx/internal/mcp"
	"github.com/somework/dsx/internal/syncer"
)

var Group = cmd.Group{
	Title: "FILES",
	Noun:  "files",
	Desc:  "one project's files, read and written",
	Note: "  tree and cat fall back to the directory's project when none is named; a named\n" +
		"  one still wins. ls and every write always name theirs: a lone positional would\n" +
		"  mean the project or the path, and the working directory must not choose the\n" +
		"  target of a destructive act.",
	Cmds: []cmd.Command{
		{Name: "files tree", Section: "READ", Form: "files tree [<project>]", Desc: "every file, recursive, with etags", Run: cmdTree},
		{Name: "files cat", Section: "READ", Form: "files cat [<project>] <path> [--out f]", Desc: "read a file (stdout by default)", Run: cmdCat},
		{Name: "files preview", Section: "READ", Form: "files preview <project> <path> [--render] [--validators a,b]", Desc: "preview links for one file", Run: cmdPreview},
		{Name: "files ls", Section: "READ", Form: "files ls <project> [path]", Desc: "list one directory", Run: cmdLs},
		{Name: "files put", Section: "WRITE", Form: "files put <project> <path> [file]", Desc: "write a file (stdin when file is omitted)", Run: cmdPut},
		{Name: "files rm", Section: "WRITE", Form: "files rm <project> <path...>", Desc: "delete files", Run: cmdRm},
		{Name: "files cp", Section: "WRITE", Form: "files cp <project> <src> <dst> [--from <project>]", Run: cmdCp},
	},
}

// boundProject reads the project the working directory is already synced to.
// tree and cat are read-only against the server and take no <dir>, so this adds
// no way to name a project — it stops hiding the one pull/push/status already
// obey. The mutating commands keep naming theirs: cwd must not choose the
// target of a destructive act.
func boundProject(form string) (string, error) {
	st, err := syncer.LoadState(".")
	if err != nil {
		return "", err
	}
	if st.ProjectID == "" {
		return "", dsxerr.Usage(form)
	}
	return st.ProjectID, nil
}

func cmdLs(ctx context.Context, c *mcp.Client, args []string) error {
	return cmd.EmitFlagged(ctx, c, "files ls", args, func(pos []string) (string, map[string]any, error) {
		project, rest, err := cmd.Need1(pos, "files ls <project> [path]")
		if err != nil {
			return "", nil, err
		}
		a := map[string]any{"project_id": project}
		if len(rest) > 0 {
			a["path"] = rest[0]
			if err := cmd.NoExtra(rest[1:], "files ls <project> [path]"); err != nil {
				return "", nil, err
			}
		}
		return "list_files", a, nil
	})
}

func cmdTree(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("files tree")
	var (
		jobs   = flags.Int("j", cmd.DefaultConcurrency, "concurrency")
		asJSON = cmd.JSONFlag(flags)
	)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	var project string
	if len(pos) == 0 {
		if project, err = boundProject("files tree <project>"); err != nil {
			return err
		}
	} else {
		var rest []string
		if project, rest, err = cmd.Need1(pos, "files tree <project>"); err != nil {
			return err
		}
		if err := cmd.NoExtra(rest, "files tree <project>"); err != nil {
			return err
		}
	}
	files, err := syncer.WalkTree(ctx, c, project, *jobs)
	if err != nil {
		return err
	}
	if *asJSON {
		out := make([]syncer.RemoteEntry, 0, len(files))
		for _, p := range syncer.SortedPaths(files) {
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
	for _, p := range syncer.SortedPaths(files) {
		e := files[p]
		total += e.Size
		fmt.Printf("%-10s %16s  %s\n", fmtutil.Bytes(e.Size), e.Etag, p)
	}
	fmt.Printf("%d files, %s\n", len(files), fmtutil.Bytes(total))
	return nil
}

func cmdCat(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("files cat")
	var (
		out    = flags.String("out", "", "write to this file instead of stdout")
		asJSON = cmd.JSONFlag(flags)
	)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	var project, path string
	if len(pos) == 1 {
		if project, err = boundProject("files cat <project> <path> [--out f]"); err != nil {
			return err
		}
		path = pos[0]
	} else {
		var rest []string
		if project, path, rest, err = cmd.Need2(pos, "files cat <project> <path> [--out f]"); err != nil {
			return err
		}
		if err := cmd.NoExtra(rest, "files cat <project> <path> [--out f]"); err != nil {
			return err
		}
	}
	body, etag, err := c.ReadFull(ctx, project, path)
	if err != nil {
		return err
	}
	if *out != "" {
		if err := syncer.WriteAtomic(*out, []byte(body)); err != nil {
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

// putWarn is where put's ledger note goes; nil means os.Stderr. Tests swap it
// rather than the process's stderr.
var putWarn io.Writer

// warnIfLedgerNearby says what put's write means for the directory it was run
// from. It writes nothing to the ledger: put has no <dir>, so the shell's
// directory is not known to be the sync root, and writing there on a guess
// would be inventing a binding.
func warnIfLedgerNearby(project string) {
	st, err := syncer.LoadState(".")
	if err != nil || st.ProjectID != project {
		return
	}
	w := putWarn
	if w == nil {
		w = os.Stderr
	}
	fmt.Fprintf(w, "dsx: note: ./%s/ is bound to this project — put writes the server "+
		"without updating it, so `dsx status` here will report a conflict; "+
		"use `dsx push` to stay in step\n", syncer.DirName)
}

func cmdPut(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("files put")
	var (
		ifMatch = flags.String("if-match", "", `etag guard; "0" asserts the path is new`)
		plan    = flags.String("plan", "", "plan_token from `dsx plan`")
		asJSON  = cmd.JSONFlag(flags)
	)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	project, path, rest, err := cmd.Need2(pos, "files put <project> <path> [file]")
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		if err := cmd.NoExtra(rest[1:], "files put <project> <path> [file]"); err != nil {
			return err
		}
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

	if err := cmd.EmitWrite(ctx, c, "write_files", a, project, []string{path}, *asJSON); err != nil {
		return err
	}
	// After the write: a note about a ledger is only worth printing once the
	// bytes it describes have actually landed.
	warnIfLedgerNearby(project)
	return nil
}

func cmdRm(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("files rm")
	asJSON := cmd.JSONFlag(flags)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	project, rest, err := cmd.Need1(pos, "files rm <project> <path...>")
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return dsxerr.Usage("files rm <project> <path...>")
	}

	token, err := syncer.PlanToken(ctx, c, map[string]any{"project_id": project, "deletes": rest})
	if err != nil {
		return err
	}
	return cmd.Emit(ctx, c, "delete_files", map[string]any{
		"project_id": project,
		"plan_token": token,
		"paths":      rest,
	}, *asJSON)
}

func cmdCp(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("files cp")
	var (
		from    = flags.String("from", "", "source project (omit for same-project copy)")
		ifMatch = flags.String("if-match", "", "etag guard on a single-file dest")
		plan    = flags.String("plan", "", "plan_token from `dsx plan`")
		asJSON  = cmd.JSONFlag(flags)
	)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	project, src, rest, err := cmd.Need2(pos, "files cp <project> <src> <dst> [--from <project>]")
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return dsxerr.Usage("files cp <project> <src> <dst> [--from <project>]")
	}
	if err := cmd.NoExtra(rest[1:], "files cp <project> <src> <dst> [--from <project>]"); err != nil {
		return err
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
	return cmd.EmitWrite(ctx, c, "copy_files", a, project, []string{rest[0]}, *asJSON)
}
