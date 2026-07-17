package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/somework/dsx/internal/dsxerr"
)

// commandNames is the one list dsx dispatches, documents and completes from.
//
// It is load-bearing three ways: run() refuses anything not in it, `usage` must
// document every entry, and every shell completes against it. That is
// deliberate. It used to be only the shells' list, and `put` fell out of it
// unnoticed -- dispatched, documented, and invisible to everyone pressing Tab.
// Wiring dispatch to the same list means the next omission breaks loudly
// instead of quietly.
//
// Kept in step by TestUsageDocumentsEveryCommand and
// TestEveryDispatchedCommandIsCompletable, which walks run()'s AST rather than
// trusting anyone to update a list twice.
var commandNames = []string{
	"auth", "cat", "completion", "conv", "conv-put", "cp", "doctor", "help",
	"ls", "member-add", "member-rm", "member-role", "members", "new", "plan",
	"preview", "project", "projects", "prompt", "pull", "push", "put", "raw",
	"rm", "sharing", "status", "support-js", "systems", "tools", "tree",
	"version",
}

func isKnownCommand(name string) bool {
	for _, n := range commandNames {
		if n == name {
			return true
		}
	}
	return false
}

func cmdCompletion(args []string) error {
	flags := newFlagSet("completion")
	asJSON := jsonFlag(flags)
	pos, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	shell, _, err := need1(pos, "completion <bash|zsh|fish>")
	if err != nil {
		return err
	}
	script, err := completionScript(shell)
	if err != nil {
		return err
	}
	if *asJSON {
		// A shell script is not JSON, so under --json it is carried inside one.
		// `eval "$(dsx completion bash)"` is the prose form and stays the point.
		b, mErr := json.Marshal(map[string]any{"shell": shell, "script": script})
		if mErr != nil {
			return mErr
		}
		fmt.Println(string(b))
		return nil
	}
	fmt.Print(script)
	return nil
}

func completionScript(shell string) (string, error) {
	names := append([]string(nil), commandNames...)
	sort.Strings(names)
	list := strings.Join(names, " ")

	switch shell {
	case "bash":
		return fmt.Sprintf(`# dsx bash completion — eval "$(dsx completion bash)"
_dsx() {
  local cur=${COMP_WORDS[COMP_CWORD]}
  if [ "$COMP_CWORD" -eq 1 ]; then
    COMPREPLY=( $(compgen -W %q -- "$cur") )
    return
  fi
  case "$cur" in
    -*) COMPREPLY=( $(compgen -W "--json --force --prune -n -q -j" -- "$cur") ) ;;
    *)  COMPREPLY=( $(compgen -f -- "$cur") ) ;;
  esac
}
complete -F _dsx dsx
`, list), nil

	case "zsh":
		return fmt.Sprintf(`#compdef dsx
# dsx zsh completion — eval "$(dsx completion zsh)"
_dsx() {
  local -a cmds
  cmds=(%s)
  if (( CURRENT == 2 )); then
    _describe 'command' cmds
  else
    _alternative 'flags:flag:(--json --force --prune -n -q -j)' 'files:file:_files'
  fi
}
compdef _dsx dsx
`, list), nil

	case "fish":
		var sb strings.Builder
		sb.WriteString("# dsx fish completion — dsx completion fish | source\n")
		sb.WriteString("complete -c dsx -f\n")
		for _, n := range names {
			fmt.Fprintf(&sb, "complete -c dsx -n __fish_use_subcommand -a %s\n", n)
		}
		sb.WriteString("complete -c dsx -l json -d 'machine-readable output'\n")
		sb.WriteString("complete -c dsx -l force -d 'overwrite conflicts'\n")
		sb.WriteString("complete -c dsx -l prune -d 'remove files absent on the other side'\n")
		return sb.String(), nil

	default:
		return "", dsxerr.Usage("completion <bash|zsh|fish>")
	}
}
