package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/somework/dsx/internal/cmd"
	"github.com/somework/dsx/internal/dsxerr"
)

func cmdCompletion(args []string) error {
	flags := cmd.NewFlagSet("completion")
	asJSON := cmd.JSONFlag(flags)
	pos, err := cmd.ParseArgs(flags, args)
	if err != nil {
		return err
	}
	shell, rest, err := cmd.Need1(pos, "completion <bash|zsh|fish>")
	if err != nil {
		return err
	}
	if err := cmd.NoExtra(rest, "completion <bash|zsh|fish>"); err != nil {
		return err
	}
	script, err := completionScript(shell)
	if err != nil {
		return err
	}
	if *asJSON {
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

// completionScript generates per-command flag lists. A single hardcoded list
// offered every command the same flags, so `dsx cat --<TAB>` proposed --prune,
// which cat rejects as unknown — the shell taught a spelling the binary
// refuses. Command names come from commandNames, derived from `groups`
// (invariant 11); flags come from commandFlags, derived from Form and
// usageFooter.
func completionScript(shell string) (string, error) {
	names := append([]string(nil), commandNames...)
	sort.Strings(names)
	list := strings.Join(names, " ")

	flagsFor := func(name string) string {
		return strings.Join(commandFlags(commandIndex[name]), " ")
	}

	switch shell {
	case "bash":
		var cases strings.Builder
		for _, n := range names {
			fmt.Fprintf(&cases, "      %s) opts=%q ;;\n", n, flagsFor(n))
		}
		return fmt.Sprintf(`# dsx bash completion — eval "$(dsx completion bash)"
_dsx() {
  local cur=${COMP_WORDS[COMP_CWORD]}
  if [ "$COMP_CWORD" -eq 1 ]; then
    COMPREPLY=( $(compgen -W %q -- "$cur") )
    return
  fi
  case "$cur" in
    -*)
      local opts=""
      case "${COMP_WORDS[1]}" in
%s      esac
      COMPREPLY=( $(compgen -W "$opts" -- "$cur") )
      ;;
    *)  COMPREPLY=( $(compgen -f -- "$cur") ) ;;
  esac
}
complete -F _dsx dsx
`, list, cases.String()), nil

	case "zsh":
		var cases strings.Builder
		for _, n := range names {
			fmt.Fprintf(&cases, "      %s) opts=(%s) ;;\n", n, flagsFor(n))
		}
		return fmt.Sprintf(`#compdef dsx
# dsx zsh completion — eval "$(dsx completion zsh)"
_dsx() {
  local -a cmds opts
  cmds=(%s)
  if (( CURRENT == 2 )); then
    _describe 'command' cmds
  else
    case "${words[2]}" in
%s    esac
    _alternative "flags:flag:($opts)" 'files:file:_files'
  fi
}
compdef _dsx dsx
`, list, cases.String()), nil

	case "fish":
		var sb strings.Builder
		sb.WriteString("# dsx fish completion — dsx completion fish | source\n")
		sb.WriteString("complete -c dsx -f\n")
		for _, n := range names {
			fmt.Fprintf(&sb, "complete -c dsx -n __fish_use_subcommand -a %s\n", n)
		}
		// -f above turns file completion off for the whole command, so each
		// subcommand asks for it back: every positional dsx takes is a path or
		// a project id, and a shell that offers nothing is worse than one that
		// offers files.
		for _, n := range names {
			fmt.Fprintf(&sb, "complete -c dsx -n '__fish_seen_subcommand_from %s' -F\n", n)
			for _, f := range commandFlags(commandIndex[n]) {
				spelling := "-l"
				if !strings.HasPrefix(f, "--") {
					spelling = "-o"
				}
				fmt.Fprintf(&sb, "complete -c dsx -n '__fish_seen_subcommand_from %s' %s %s\n",
					n, spelling, strings.TrimLeft(f, "-"))
			}
		}
		return sb.String(), nil

	default:
		return "", dsxerr.Usage("completion <bash|zsh|fish>")
	}
}
