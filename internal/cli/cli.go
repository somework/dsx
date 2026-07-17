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

// Main is dsx. The caller is package main, which owns `version` because the
// release stamp aims at it; everything else is here.
func Main(stampedVersion string) {
	stamped = stampedVersion

	err := run()
	if err == nil {
		return
	}
	// The renderer runs here, outside every FlagSet, so that a failure raised
	// before flags were parsed still honours --json.
	fmt.Fprintln(os.Stderr, dsxerr.Render(err, dsxerr.JSONRequested(os.Args[1:])))
	os.Exit(dsxerr.ExitCodeFor(err))
}

// run resolves the command, then gives it exactly as much of dsx as it declared
// it needs. The order of the three stages below is the whole of run(): it used
// to also be a switch restating every command in the program.
func run() error {
	if len(os.Args) < 2 {
		fmt.Println(usage)
		return nil
	}

	name, args := os.Args[1], os.Args[2:]

	// Reject an unknown command before reaching for the credential. Loading it
	// first meant a typo on a machine with no login was reported as an auth
	// failure -- dsx blaming the user's credentials for their spelling, and
	// exit 5 inviting a re-authentication that could not possibly help.
	//
	// This is now the same lookup that dispatches, so the two cannot disagree.
	// They used to be a list and a switch, with an unreachable default arm
	// standing by for the day they did.
	entry, ok := commandIndex[name]
	if !ok {
		return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: "unknown command " + strconv.Quote(name) + " — run `dsx help`"}
	}

	// help, version and completion answer from the binary alone. They install no
	// signal handler because there is nothing to interrupt.
	if entry.Needs == cmd.NeedNothing {
		return entry.Dispatch(context.Background(), nil, args)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// auth reads the credential itself, and must not be handed a client built
	// from a token it is about to report on.
	if entry.Needs == cmd.NeedAuth {
		return entry.Dispatch(ctx, nil, args)
	}

	token, err := auth.LoadToken()
	if err != nil {
		return err
	}
	return entry.Dispatch(ctx, mcp.New(token), args)
}
