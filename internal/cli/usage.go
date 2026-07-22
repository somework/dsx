package cli

import (
	"strings"

	"github.com/somework/dsx/internal/cmd"
)

var usage string

func init() { usage = renderUsage(groups) }

const usageHeader = `dsx — Claude Design sync. Reads Claude Code's own OAuth token; never writes it.`

// usageFooter documents the flags that Form does not: --json, which every
// command takes, and the ones several commands share. Each flag line names the
// commands it reaches after an em dash — the scope is load-bearing, not prose.
// TestEveryDeclaredFlagIsDocumented reads it as data in both directions: a
// command may declare no flag that neither its Form nor a scope here mentions,
// and a scope may name no command that does not declare the flag.
const usageFooter = `FLAGS
  --json      machine-readable output — every command
  --prune     delete what the other side lacks — pull, push
  --force     overwrite conflicts — pull, push
  -q          suppress the summary line — pull, push, status
  -n          dry run — pull, push
  -j N        concurrency (default 8) — clone, pull, push, files tree, fetch, diff

WRITE GUARDS
  --if-match E  etag guard ("0" asserts new) — files put, files cp, project support-js
  --plan T      plan_token from dsx plan new — files put, files cp, project support-js

GLOBAL
  dsx -C <dir> <command>  run as if dsx had been started in <dir>, like git's

EXIT CODES
  0 ok   1 failed   2 usage   3 conflict (needs a human)
  4 transport (retry may help)   5 auth (run any ` + "`claude`" + ` command)

Env: DSX_TOKEN overrides the stored credential. DSX_ENDPOINT overrides the MCP URL.
     DSX_PROGRESS=never|always overrides the pull/push transfer counter, which
     otherwise draws on stderr only when stderr is a terminal.`

const (
	usageDescCol    = 40
	usageDescColMax = 72
)

func renderUsage(gs []cmd.Group) string {
	var sb strings.Builder

	sb.WriteString(usageHeader + "\n")
	for _, g := range gs {
		sb.WriteString("\n" + g.Title + "\n")
		if g.Noun != "" {
			sb.WriteString(usageLine(cmd.Command{Form: g.Noun + " <verb>", Desc: g.Desc}))
		} else {
			for _, c := range g.Cmds {
				sb.WriteString(usageLine(c))
			}
		}
		if g.Note != "" {
			sb.WriteString(g.Note + "\n")
		}
	}
	sb.WriteString("\n" + usageFooter)
	return sb.String()
}

// renderNounHelp is what `dsx <noun>` prints: every verb in full, under its
// section heading when the group declares one. A group declaring no section
// renders one flat list — the heading is not invented for it.
func renderNounHelp(g cmd.Group) string {
	var sb strings.Builder
	sb.WriteString("usage: dsx " + g.Noun + " <verb>\n")
	if g.Desc != "" {
		sb.WriteString(g.Desc + "\n")
	}
	// The root's fixed column is set by the flat forms; every form here carries
	// the noun as well, so a shared column would leave the longest ones running
	// into their own description. A form past usageDescColMax is left out of the
	// measurement and overflows on its own line rather than pushing every
	// description in the group out to meet it.
	col := usageDescCol
	for _, c := range g.Cmds {
		if w := len("  dsx "+c.Form) + 2; w > col && w <= usageDescColMax {
			col = w
		}
	}
	section := ""
	for _, c := range g.Cmds {
		if c.Section != section {
			section = c.Section
			sb.WriteString("\n" + section + "\n")
		}
		sb.WriteString(usageLineAt(c, col))
	}
	if g.Note != "" {
		sb.WriteString("\n" + g.Note + "\n")
	}
	return sb.String()
}

func usageLine(c cmd.Command) string { return usageLineAt(c, usageDescCol) }

func usageLineAt(c cmd.Command, col int) string {
	line := "  dsx " + c.Form
	if c.Desc == "" {
		return line + "\n"
	}
	if pad := col - len(line); pad > 0 {
		return line + strings.Repeat(" ", pad) + c.Desc + "\n"
	}
	return line + "  " + c.Desc + "\n"
}
