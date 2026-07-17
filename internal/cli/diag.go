package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/somework/dsx/internal/auth"
)

var diagGroup = group{
	Title: "DIAGNOSTICS",
	Cmds: []command{
		{Name: "help", Aliases: []string{"-h", "--help"}, Form: "help",
			Needs: needNothing, Run: noClient(cmdHelp)},
		{Name: "auth", Form: "auth", Desc: "token scopes and expiry (never the token)",
			Needs: needAuth, Run: noClient(cmdAuth)},
		{Name: "doctor", Form: "doctor", Desc: "token, endpoint, clock skew",
			Run: cmdDoctor},
		{Name: "version", Aliases: []string{"-v", "--version"}, Form: "version",
			Desc: "version, revision, platform", Needs: needNothing, Run: noClient(cmdVersion)},
		{Name: "completion", Form: "completion <bash|zsh|fish>",
			Needs: needNothing, Run: noClient(cmdCompletion)},
	},
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
