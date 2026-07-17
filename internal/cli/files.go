package cli

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

var filesGroup = cmd.Group{
	Title: "FILES",
	Cmds: []cmd.Command{
		{Name: "ls", Form: "ls <project> [path]", Desc: "list one directory", Run: cmdLs},
		{Name: "tree", Form: "tree <project>", Desc: "every file, recursive, with etags", Run: cmdTree},
		{Name: "cat", Form: "cat <project> <path> [--out f]", Desc: "read a file (stdout by default)", Run: cmdCat},
		{Name: "put", Form: "put <project> <path> [file]", Desc: "write a file (stdin when file is omitted)", Run: cmdPut},
		{Name: "rm", Form: "rm <project> <path...>", Desc: "delete files", Run: cmdRm},
		{Name: "cp", Form: "cp <project> <src> <dst> [--from <project>]", Run: cmdCp},
	},
}

func cmdLs(ctx context.Context, c *mcp.Client, args []string) error {
	return cmd.EmitFlagged(ctx, c, "ls", args, func(pos []string) (string, map[string]any, error) {
		project, rest, err := cmd.Need1(pos, "ls <project> [path]")
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
	flags := cmd.NewFlagSet("tree")
	var (
		jobs   = flags.Int("j", 8, "concurrency")
		asJSON = cmd.JSONFlag(flags)
	)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	project, _, err := cmd.Need1(pos, "tree <project>")
	if err != nil {
		return err
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
	flags := cmd.NewFlagSet("cat")
	var (
		out    = flags.String("out", "", "write to this file instead of stdout")
		asJSON = cmd.JSONFlag(flags)
	)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	project, path, _, err := cmd.Need2(pos, "cat <project> <path> [--out f]")
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
	flags := cmd.NewFlagSet("put")
	var (
		ifMatch = flags.String("if-match", "", `etag guard; "0" asserts the path is new`)
		plan    = flags.String("plan", "", "plan_token from `dsx plan`")
		asJSON  = cmd.JSONFlag(flags)
	)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	project, path, rest, err := cmd.Need2(pos, "put <project> <path> [file]")
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
	return cmd.EmitWrite(ctx, c, "write_files", a, project, []string{path}, *asJSON)
}

func cmdRm(ctx context.Context, c *mcp.Client, args []string) error {
	flags := cmd.NewFlagSet("rm")
	asJSON := cmd.JSONFlag(flags)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	project, rest, err := cmd.Need1(pos, "rm <project> <path...>")
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return dsxerr.Usage("rm <project> <path...>")
	}

	// Deletes always need a path-scoped plan_token naming every path; a
	// project-scoped one is refused.
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
	flags := cmd.NewFlagSet("cp")
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
	project, src, rest, err := cmd.Need2(pos, "cp <project> <src> <dst> [--from <project>]")
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
	return cmd.EmitWrite(ctx, c, "copy_files", a, project, []string{rest[0]}, *asJSON)
}
