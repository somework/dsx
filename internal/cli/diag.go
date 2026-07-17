package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/somework/dsx/internal/auth"
	"github.com/somework/dsx/internal/cmd"
)

var diagGroup = cmd.Group{
	Title: "DIAGNOSTICS",
	Cmds: []cmd.Command{
		{Name: "help", Aliases: []string{"-h", "--help"}, Form: "help",
			Needs: cmd.NeedNothing, Run: cmd.NoClient(cmdHelp)},
		{Name: "auth", Form: "auth", Desc: "token scopes and expiry (never the token)",
			Needs: cmd.NeedAuth, Run: cmd.NoClient(cmdAuth)},
		{Name: "doctor", Form: "doctor", Desc: "token, endpoint, clock skew",
			Run: cmdDoctor},
		{Name: "version", Aliases: []string{"-v", "--version"}, Form: "version",
			Desc: "version, revision, platform", Needs: cmd.NeedNothing, Run: cmd.NoClient(cmdVersion)},
		{Name: "completion", Form: "completion <bash|zsh|fish>",
			Needs: cmd.NeedNothing, Run: cmd.NoClient(cmdCompletion)},
	},
}

func cmdAuth(args []string) error {
	flags := cmd.NewFlagSet("auth")
	asJSON := flags.Bool("json", false, "JSON output")
	if _, err := cmd.ParseArgs(flags, args); err != nil {
		return err
	}

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

func cmdHelp(args []string) error {
	flags := cmd.NewFlagSet("help")
	asJSON := cmd.JSONFlag(flags)
	if _, err := cmd.ParseArgs(flags, args); err != nil {
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
