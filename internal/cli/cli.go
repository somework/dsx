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
	"strings"
	"syscall"

	"github.com/somework/dsx/internal/auth"
	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/mcp"
)

func Main(stampedVersion string) {
	stamped = stampedVersion
	cmd.Progress = progressWriter()

	err := run()
	if err == nil {
		return
	}

	fmt.Fprintln(os.Stderr, dsxerr.Render(err, dsxerr.JSONRequested(os.Args[1:])))
	os.Exit(dsxerr.ExitCodeFor(err))
}

func run() error {
	// dsx's own options come off first, before the command name is read, the
	// way `git -C <dir> status` works. Chdir rather than threading a root
	// through every command: -C means "run as if dsx had been started there",
	// and every path dsx resolves — the ledger walk, .dsxignore, safeJoin's
	// roots — already answers to the working directory.
	chdirs, args, err := splitGlobalFlags(os.Args[1:])
	if err != nil {
		return err
	}
	if err := applyChdirs(chdirs); err != nil {
		return err
	}

	if len(args) == 0 {
		fmt.Println(usage)
		return nil
	}

	name := args[0]
	args = args[1:]

	if g, isNoun := nounIndex[name]; isNoun {
		// A dash where the verb goes is a question about the noun, not a verb
		// spelled oddly: `dsx conv --json` must list the verbs rather than
		// refuse a flag by calling it unknown. TestNoVerbIsSpelledLikeAFlag
		// keeps that branch from swallowing a real verb.
		if len(args) == 0 || strings.HasPrefix(args[0], "-") {
			if i := misplacedVerb(g, args); i >= 0 {
				return &dsxerr.Error{Kind: dsxerr.KindUsage,
					Msg: "flags go after the verb — " + reorderVerbFirst(g.Noun, args, i)}
			}
			return printNounHelp(g, args)
		}
		verb := args[0]
		name, args = g.Noun+" "+verb, args[1:]
		if _, ok := commandIndex[name]; !ok {
			return &dsxerr.Error{Kind: dsxerr.KindUsage,
				Msg: "unknown " + g.Noun + " verb " + strconv.Quote(verb) + " — run `dsx " + g.Noun + " -h`"}
		}
	}

	entry, ok := commandIndex[name]
	if !ok {
		msg := "unknown command " + strconv.Quote(name) + " — run `dsx help`"
		if alts := didYouMean(name); len(alts) > 0 {
			msg = "unknown command " + strconv.Quote(name) +
				" — did you mean " + strings.Join(alts, ", ") + "? run `dsx help`"
		}
		return &dsxerr.Error{Kind: dsxerr.KindUsage, Msg: msg}
	}

	err = dispatch(entry, args)
	if errors.Is(err, flag.ErrHelp) {
		// A help request is an answer, not a failure: exit 0, matching the
		// top-level `dsx -h`. The flag list comes back inside the error
		// because only the command's own FlagSet knows it; Form and Desc come
		// from the registry, which is the only place they are declared.
		return printCommandHelp(entry, err, args)
	}
	return err
}

// didYouMean turns an unknown first token into the addresses it could have
// meant, already quoted. Two shapes cover the mistakes the migration created
// and nothing else does: a bare verb, which is how every removed flat spelling
// was written, and a pluralised noun, which is how the nouns themselves get
// mistyped — dsx members reads as natural English and the registry is singular
// there only because the server's own tools are. The mechanism is not new; the
// second token already had it, since a dead form whose first token is still a
// noun gets a specific refusal. Anything else gets none: a suggestion invented
// for every typo is worth nothing.
func didYouMean(name string) []string {
	quote := func(addrs []string) []string {
		out := make([]string, 0, len(addrs))
		for _, a := range addrs {
			out = append(out, "`dsx "+a+"`")
		}
		return out
	}
	if addrs := verbIndex[name]; len(addrs) > 0 {
		return quote(addrs)
	}
	for _, alt := range []string{strings.TrimSuffix(name, "s"), name + "s"} {
		if alt == name {
			continue
		}
		if _, ok := nounIndex[alt]; ok {
			return quote([]string{alt})
		}
	}
	return nil
}

// misplacedVerb finds this noun's verb standing behind a flag, and returns its
// index. The dash branch above reads a leading dash as "no verb was given",
// which is right for `dsx files -h` and silently wrong for `dsx member --json
// rm p uuid`: the verb is there, one token along, and answering with the verb
// list dropped a delete on the floor under exit 0 with an empty stderr. The
// root level refuses the same mistake (`dsx --json pull` is a usage error), so
// this branch was strictly the weaker of the two. Only a verb this noun
// declares counts, which is what keeps a flag's value or a foreign noun's verb
// from being read as one.
func misplacedVerb(g cmd.Group, args []string) int {
	for i, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		for _, v := range nounVerbs(g) {
			if a == v {
				return i
			}
		}
	}
	return -1
}

// reorderVerbFirst rebuilds the caller's own line with the verb in front.
// Invariant 18 wants a refusal to name a form that parses, and here the form
// that parses is the line they typed: ParseArgs takes flags before, between
// and after positionals once a verb's FlagSet is reached, so nothing needs
// dropping or reordering beyond the verb itself.
func reorderVerbFirst(noun string, args []string, verbAt int) string {
	out := make([]string, 0, len(args)+2)
	out = append(out, "dsx", noun, args[verbAt])
	out = append(out, args[:verbAt]...)
	out = append(out, args[verbAt+1:]...)
	return strings.Join(out, " ")
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
	// stderr, not stdout: the notice explains a wait, and stdout is the report
	// a caller may be piping.
	return entry.Dispatch(ctx, mcp.New(token, mcp.WithRetryNotice(os.Stderr)), args)
}

// printNounHelp answers `dsx <noun>` and `dsx <noun> -h`. It runs before any
// Command is resolved, so no Needs exists yet and no credential is read — the
// same ordering TestPullHelpAnswersBeforeAuth pins one level down.
func printNounHelp(g cmd.Group, args []string) error {
	if !dsxerr.JSONRequested(args) {
		fmt.Print(renderNounHelp(g))
		return nil
	}
	verbs := make([]commandSpec, 0, len(g.Cmds))
	for _, c := range g.Cmds {
		verbs = append(verbs, commandSpec{
			Group: g.Title, Noun: g.Noun, Name: c.Name,
			Form: c.Form, Desc: c.Desc, Section: c.Section, Aliases: c.Aliases,
		})
	}
	b, err := json.Marshal(map[string]any{"noun": g.Noun, "desc": g.Desc, "verbs": verbs})
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
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
