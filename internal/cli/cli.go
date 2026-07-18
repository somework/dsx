package cli

import (
	"context"
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
