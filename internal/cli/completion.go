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
	top := append([]string(nil), topNames...)
	sort.Strings(top)
	list := strings.Join(top, " ")

	// nouns is what turns a first token into a two-token address: the shell has
	// to know which words take a verb before it can key a flag list off the
	// full name.
	nouns := make([]string, 0, len(nounIndex))
	for n := range nounIndex {
		nouns = append(nouns, n)
	}
	sort.Strings(nouns)

	addresses := append([]string(nil), commandNames...)
	sort.Strings(addresses)

	flagsFor := func(name string) string {
		return strings.Join(commandFlags(commandIndex[name]), " ")
	}

	switch shell {
	case "bash":
		var verbCases, flagCases strings.Builder
		for _, n := range nouns {
			fmt.Fprintf(&verbCases, "      %s) verbs=%q ;;\n", n, strings.Join(nounVerbs(nounIndex[n]), " "))
		}
		for _, n := range addresses {
			fmt.Fprintf(&flagCases, "      %q) opts=%q ;;\n", n, flagsFor(n))
		}
		nounPattern := strings.Join(nouns, "|")
		return fmt.Sprintf(`# dsx bash completion — eval "$(dsx completion bash)"
_dsx() {
  local cur=${COMP_WORDS[COMP_CWORD]}
  if [ "$COMP_CWORD" -eq 1 ]; then
    COMPREPLY=( $(compgen -W %q -- "$cur") )
    return
  fi
  local verbs=""
  case "${COMP_WORDS[1]}" in
%s  esac
  if [ -n "$verbs" ] && [ "$COMP_CWORD" -eq 2 ] && [[ "$cur" != -* ]]; then
    COMPREPLY=( $(compgen -W "$verbs" -- "$cur") )
    return
  fi
  local addr="${COMP_WORDS[1]}"
  case "${COMP_WORDS[1]}" in
    %s) addr="${COMP_WORDS[1]} ${COMP_WORDS[2]}" ;;
  esac
  case "$cur" in
    -*)
      local opts=""
      case "$addr" in
%s      esac
      COMPREPLY=( $(compgen -W "$opts" -- "$cur") )
      ;;
    *)  COMPREPLY=( $(compgen -f -- "$cur") ) ;;
  esac
}
complete -F _dsx dsx
`, list, verbCases.String(), nounPattern, flagCases.String()), nil

	case "zsh":
		var verbCases, flagCases strings.Builder
		for _, n := range nouns {
			fmt.Fprintf(&verbCases, "      %s) verbs=(%s) ;;\n", n, strings.Join(nounVerbs(nounIndex[n]), " "))
		}
		for _, n := range addresses {
			fmt.Fprintf(&flagCases, "      %q) opts=(%s) ;;\n", n, flagsFor(n))
		}
		return fmt.Sprintf(`#compdef dsx
# dsx zsh completion — eval "$(dsx completion zsh)"
_dsx() {
  local -a cmds verbs opts
  local addr
  cmds=(%s)
  if (( CURRENT == 2 )); then
    _describe 'command' cmds
    return
  fi
  verbs=()
  case "${words[2]}" in
%s  esac
  if (( CURRENT == 3 )) && (( ${#verbs} )) && [[ "${words[CURRENT]}" != -* ]]; then
    _describe 'verb' verbs
    return
  fi
  addr="${words[2]}"
  if (( ${#verbs} )); then
    addr="${words[2]} ${words[3]}"
  fi
  case "$addr" in
%s  esac
  _alternative "flags:flag:($opts)" 'files:file:_files'
}
compdef _dsx dsx
`, list, verbCases.String(), flagCases.String()), nil

	case "fish":
		var sb strings.Builder
		sb.WriteString("# dsx fish completion — dsx completion fish | source\n")
		sb.WriteString("complete -c dsx -f\n")
		for _, n := range top {
			fmt.Fprintf(&sb, "complete -c dsx -n __fish_use_subcommand -a %s\n", n)
		}
		// -f above turns file completion off for the whole command, so each
		// subcommand asks for it back: every positional dsx takes is a path or
		// a project id, and a shell that offers nothing is worse than one that
		// offers files.
		//
		// A noun's verbs hang off __fish_seen_subcommand_from, which knows the
		// word is present but not that it came first, so `dsx cat conv` would
		// offer conv's verbs too. That invocation does not parse, so the cost is
		// a suggestion inside an already-broken line.
		for _, n := range nouns {
			g := nounIndex[n]
			fmt.Fprintf(&sb, "complete -c dsx -n '__fish_seen_subcommand_from %s' -a %q\n",
				n, strings.Join(nounVerbs(g), " "))
		}
		// A two-token address reaches fish as both its words: the predicate is
		// a membership test, so "conv get" reads as "saw conv or get".
		for _, n := range addresses {
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
