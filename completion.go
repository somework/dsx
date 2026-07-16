package main

import (
	"fmt"
	"sort"
	"strings"
)

// commandNames is the single list the shells complete against. A command that
// exists but is missing here is invisible to a user pressing Tab, so
// TestUsageDocumentsEveryCommand keeps this list and `usage` in step.
var commandNames = []string{
	"auth", "cat", "completion", "conv", "conv-put", "cp", "doctor", "help",
	"ls", "member-add", "member-rm", "member-role", "members", "new", "plan",
	"preview", "project", "projects", "prompt", "pull", "push", "raw", "rm",
	"sharing", "status", "support-js", "systems", "tools", "tree", "version",
}

func cmdCompletion(args []string) error {
	shell, _, err := need1(args, "completion <bash|zsh|fish>")
	if err != nil {
		return err
	}
	script, err := completionScript(shell)
	if err != nil {
		return err
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
		return "", usageError("completion <bash|zsh|fish>")
	}
}
