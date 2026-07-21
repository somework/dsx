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
  --prune     delete what the other side lacks — pull, push, status
  --force     overwrite conflicts — pull, push, status
  -q  -n      suppress the summary line, dry run — pull, push, status
  -j N        concurrency (default 8) — clone, pull, push, status, tree

WRITE GUARDS
  --if-match E  etag guard ("0" asserts new) — put, cp, support-js
  --plan T      plan_token from dsx plan — put, cp, support-js

GLOBAL
  dsx -C <dir> <command>  run as if dsx had been started in <dir>, like git's

EXIT CODES
  0 ok   1 failed   2 usage   3 conflict (needs a human)
  4 transport (retry may help)   5 auth (run any ` + "`claude`" + ` command)

Env: DSX_TOKEN overrides the stored credential. DSX_ENDPOINT overrides the MCP URL.
     DSX_PROGRESS=never|always overrides the pull/push transfer counter, which
     otherwise draws on stderr only when stderr is a terminal.`

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
