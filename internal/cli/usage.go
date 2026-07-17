package cli

import (
	"strings"

	"github.com/somework/dsx/internal/cmd"
)

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

const usageDescCol = 40

func renderUsage(gs []cmd.Group) string {
	var sb strings.Builder

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

func usageLine(c cmd.Command) string {
	line := "  dsx " + c.Form
	if c.Desc == "" {
		return line + "\n"
	}
	if pad := usageDescCol - len(line); pad > 0 {
		return line + strings.Repeat(" ", pad) + c.Desc + "\n"
	}
	return line + "  " + c.Desc + "\n"
}
