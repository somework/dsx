package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
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
// offered every command the same flags, so `dsx files cat --<TAB>` proposed --prune,
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

	// bare is the set that gets no file completion; the shells spell the same
	// membership test three ways.
	bareSet := make(map[string]bool, len(noArgAddresses))
	var barePattern []string
	for _, n := range noArgAddresses {
		bareSet[n] = true
		barePattern = append(barePattern, strconv.Quote(n))
	}
	bareCase := strings.Join(barePattern, "|")

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
    *)
      case "$addr" in
        %s) return ;;
      esac
      COMPREPLY=( $(compgen -f -- "$cur") ) ;;
  esac
}
complete -F _dsx dsx
`, list, verbCases.String(), nounPattern, flagCases.String(), bareCase), nil

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
  case "$addr" in
    %s) _alternative "flags:flag:($opts)"; return ;;
  esac
  _alternative "flags:flag:($opts)" 'files:file:_files'
}
compdef _dsx dsx
`, list, verbCases.String(), flagCases.String(), bareCase), nil

	case "fish":
		var sb strings.Builder
		sb.WriteString(fishPreamble)
		for _, n := range top {
			fmt.Fprintf(&sb, "complete -c dsx -n '__dsx_want 1' -a %s\n", n)
		}
		for _, n := range nouns {
			fmt.Fprintf(&sb, "complete -c dsx -n '__dsx_want 2; and __dsx_at 1 %s' -a %q\n",
				n, strings.Join(nounVerbs(nounIndex[n]), " "))
		}
		// -f in the preamble turns file completion off for the whole command,
		// so each address asks for it back: every positional dsx takes is a
		// path or a project id, and a shell that offers nothing is worse than
		// one that offers files.
		for _, n := range addresses {
			cond := fishAddressCond(n)
			if !bareSet[n] {
				fmt.Fprintf(&sb, "complete -c dsx -n '%s' -F\n", cond)
			}
			for _, f := range commandFlags(commandIndex[n]) {
				spelling := "-l"
				if !strings.HasPrefix(f, "--") {
					spelling = "-o"
				}
				fmt.Fprintf(&sb, "complete -c dsx -n '%s' %s %s\n",
					cond, spelling, strings.TrimLeft(f, "-"))
			}
		}
		return sb.String(), nil

	default:
		return "", dsxerr.Usage("completion <bash|zsh|fish>")
	}
}

// takesAnArgument reads a Form for anything a caller can type that is not a
// flag spelling. Every address used to fall through to file completion,
// including the ones that take nothing: `dsx pull <TAB>` offered the whole
// directory and each of those names is refused with exit 2 a keystroke later.
//
// The question is deliberately "any argument", not "any positional". A flag's
// value is an argument the shell should help with — diff's only one lives
// inside [--out <dir>] — so reading it the narrower way would trade one wrong
// offer for a missing right one.
func takesAnArgument(c cmd.Command) bool {
	rest := strings.TrimPrefix(c.Form, c.Name)
	for _, f := range strings.Fields(strings.Map(unbracket, rest)) {
		if !strings.HasPrefix(f, "-") {
			return true
		}
	}
	return false
}

func unbracket(r rune) rune {
	if strings.ContainsRune("[]()|", r) {
		return ' '
	}
	return r
}

// fishAddressCond spells one address as a position test. bash and zsh index
// into the word array and were right from the start; fish has no such array in
// a completion predicate, so it gets one.
func fishAddressCond(name string) string {
	noun, verb, ok := strings.Cut(name, " ")
	if !ok {
		return "__dsx_at 1 " + name
	}
	return "__dsx_at 1 " + noun + "; and __dsx_at 2 " + verb
}

// fishPreamble carries the position helpers the generated rules key off.
//
// __fish_seen_subcommand_from, which these replace, answers "this word
// appeared somewhere on the line" — the same thing as a position test only
// while every word belongs to one command. dsx's do not: get is a verb of
// project and of conv, put of files and of conv, ls of four nouns. So every
// address was offered the union of the flags of everything sharing either of
// its tokens, and "dsx project get --" proposed --chat. That fires on a line
// that parses, which is what makes it worse than the noun-name-as-argument
// case the old comment excused.
const fishPreamble = `# dsx fish completion — dsx completion fish | source

# __dsx_args is the line as dsx's own parser sees it: the command name gone and
# the globals off the front, mirroring splitGlobalFlags. Skipping them is not
# cosmetic — "dsx -C dir files" is two tokens longer than "dsx files", so a
# count of raw tokens reads the wrong one for every -C line. The token under
# the cursor is absent, so the count is the index of the argument being
# completed, minus one.
function __dsx_args
    set -l t (commandline -pxc)
    set -e t[1]
    while set -q t[1]
        switch $t[1]
            case -C
                set -e t[1]
                if set -q t[1]
                    set -e t[1]
                end
            case '-C=*'
                set -e t[1]
            case '*'
                break
        end
    end
    if set -q t[1]
        printf '%s\n' $t
    end
end

# __dsx_at <n> <word…> — the nth argument is one of these words.
function __dsx_at
    set -l a (__dsx_args)
    test (count $a) -ge $argv[1]; or return 1
    contains -- $a[$argv[1]] $argv[2..-1]
end

# __dsx_want <n> — the token being completed is the nth argument.
function __dsx_want
    test (count (__dsx_args)) -eq (math $argv[1] - 1)
end

complete -c dsx -f
`
