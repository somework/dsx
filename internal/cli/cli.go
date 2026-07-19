package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/somework/dsx/internal/auth"
	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/mcp"
)

func Main(stampedVersion string) {
	stamped = stampedVersion

	err := run()
	if err == nil {
		return
	}

	fmt.Fprintln(os.Stderr, dsxerr.Render(err, dsxerr.JSONRequested(os.Args[1:])))
	os.Exit(dsxerr.ExitCodeFor(err))
}

func run() error {
	if len(os.Args) < 2 {
		fmt.Println(usage)
		return nil
	}

	name, args := os.Args[1], os.Args[2:]

	entry, ok := commandIndex[name]
	if !ok {
		return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: "unknown command " + strconv.Quote(name) + " — run `dsx help`"}
	}

	err := dispatch(entry, args)
	if errors.Is(err, flag.ErrHelp) {
		// A help request is an answer, not a failure: exit 0, matching the
		// top-level `dsx -h`. The flag list comes back inside the error
		// because only the command's own FlagSet knows it; Form and Desc come
		// from the registry, which is the only place they are declared.
		return printCommandHelp(entry, err, args)
	}
	return err
}

// helpRequested spots the two spellings flag itself answers, and only in the
// one position where they cannot be another flag's value: nothing precedes
// args[0]. A later `-h` needs no relaxation — with a credential ParseArgs
// returns flag.ErrHelp and run() answers on the normal path.
func helpRequested(args []string) bool {
	return len(args) > 0 && (args[0] == "-h" || args[0] == "--help")
}

func dispatch(entry cmd.Command, args []string) error {
	// Help precedes auth: `dsx pull -h` is a question about pull, not an
	// attempt to run it, and answering it must not need a credential (nor a
	// signal context — nothing is begun that could be cut short). The client
	// is tokenless rather than nil so anything that reaches the wire from here
	// fails like any unauthenticated call instead of dereferencing nil.
	if helpRequested(args) {
		return entry.Dispatch(context.Background(), mcp.New(""), args)
	}

	if entry.Needs == cmd.NeedNothing {
		return entry.Dispatch(context.Background(), nil, args)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if entry.Needs == cmd.NeedAuth {
		return entry.Dispatch(ctx, nil, args)
	}

	token, err := auth.LoadToken()
	if err != nil {
		// NeedOptionalClient runs regardless: doctor must reach its own
		// credential and endpoint checks so it can report the auth failure
		// as a diagnosis rather than have run() abort with the generic
		// envelope. A tokenless client lets the endpoint check fail naturally.
		if entry.Needs == cmd.NeedOptionalClient {
			return entry.Dispatch(ctx, mcp.New(""), args)
		}
		return err
	}
	return entry.Dispatch(ctx, mcp.New(token), args)
}

func printCommandHelp(entry cmd.Command, help error, args []string) error {
	flags := help.Error()
	if !dsxerr.JSONRequested(args) {
		fmt.Println("usage: dsx " + entry.Form)
		if entry.Desc != "" {
			fmt.Println(entry.Desc)
		}
		fmt.Print(flags)
		return nil
	}
	b, err := json.Marshal(map[string]any{
		"usage": "dsx " + entry.Form,
		"desc":  entry.Desc,
		"flags": flags,
	})
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}
