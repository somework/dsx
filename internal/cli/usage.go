package cli

import "strings"

// usage is what `dsx help` prints, and what `dsx` with no arguments prints.
//
// It is generated from `groups` so that a command cannot be dispatched and
// undocumented at the same time — that used to be a hand-maintained const and a
// test that grepped it. Only the parts that are not commands are written out:
// the first line, and everything after the last group.
//
// Built in init() for the same reason as commandIndex: cmdHelp reads this var,
// and cmdHelp is itself a command in `groups`, so a var initialiser would close
// a loop Go refuses. Package-level var initialisers all finish before any
// init() runs, so `groups` is complete here regardless of file order.
var usage string

func init() { usage = renderUsage(groups) }

const usageHeader = `dsx — Claude Design sync. Reads Claude Code's own OAuth token; never writes it.`

const usageFooter = `GLOBAL
  --json      machine-readable output      -q  suppress the summary line
  -j N        concurrency (default 8)      -n  dry run

EXIT CODES
  0 ok   1 failed   2 usage   3 conflict (needs a human)
  4 transport (retry may help)   5 auth (run any ` + "`claude`" + ` command)

Env: DSX_TOKEN overrides the stored credential. DSX_ENDPOINT overrides the MCP URL.`

// usageDescCol is the column a description starts at, counted from the start of
// the line. "  dsx " is six of it, so a Form has 34 before it runs out of room.
const usageDescCol = 40

func renderUsage(gs []group) string {
	var sb strings.Builder
	// Every command line below already ends in a newline, so a section needs
	// exactly one more ahead of its title to leave a blank line.
	sb.WriteString(usageHeader + "\n")
	for _, g := range gs {
		sb.WriteString("\n" + g.Title + "\n")
		for _, c := range g.Cmds {
			sb.WriteString(usageLine(c))
		}
		if g.Note != "" {
			sb.WriteString(g.Note + "\n")
		}
	}
	sb.WriteString("\n" + usageFooter)
	return sb.String()
}

// usageLine lays out one command. A Form too long for the column keeps its
// description on the same line behind two spaces rather than wrapping: no
// command needs that today, but silently dropping the description would be a
// worse answer than a ragged line.
func usageLine(c command) string {
	line := "  dsx " + c.Form
	if c.Desc == "" {
		return line + "\n"
	}
	if pad := usageDescCol - len(line); pad > 0 {
		return line + strings.Repeat(" ", pad) + c.Desc + "\n"
	}
	return line + "  " + c.Desc + "\n"
}
