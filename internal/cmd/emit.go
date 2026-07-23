package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/somework/dsx/internal/dsxerr"
	"github.com/somework/dsx/internal/fmtutil"
	"github.com/somework/dsx/internal/mcp"
	"github.com/somework/dsx/internal/syncer"
)

// Human renders one tool reply for a person. It returns false when the reply
// is not the shape it was written against, and the caller falls back to the
// reply itself: every shape dsx renders was measured against the real server
// rather than promised by it — no tool declares an output schema — and three
// protocol facts were guessed wrong before this rule existed.
type Human func(text string) (string, bool)

// Machine is Human's counterpart on the --json path, and it exists because
// "--json relays the server's shape" is only true where the server sent a JSON
// document. get_conversation does not: its reply is a tag, a body and a notice,
// so --json already emitted dsx's own {"text":…} wrapper — an unusable shape,
// but dsx's. Where that is the case a command may shape the reply properly
// instead, and it must emit valid JSON or refuse.
type Machine func(text string) (string, bool)

func Emit(ctx context.Context, c *mcp.Client, tool string, args map[string]any, asJSON bool, h Human) error {
	text, err := c.CallTool(ctx, tool, args)
	if err != nil {
		return err
	}
	PrintReply(text, asJSON, h)
	return nil
}

// EmitShaped is Emit for a command that shapes its own --json.
func EmitShaped(ctx context.Context, c *mcp.Client, tool string, args map[string]any, asJSON bool, h Human, m Machine) error {
	text, err := c.CallTool(ctx, tool, args)
	if err != nil {
		return err
	}
	PrintReplyShaped(text, asJSON, h, m)
	return nil
}

// PrintReply is the one place a tool reply reaches a person or a program.
//
// --json is untouched, renderer or not: README pins that where dsx relays a
// tool result the shape is the server's, and a machine reading it has already
// been told so.
func PrintReply(text string, asJSON bool, h Human) {
	PrintReplyShaped(text, asJSON, h, nil)
}

// PrintReplyShaped is PrintReply for the one command that owns its --json
// shape as well as its human one.
func PrintReplyShaped(text string, asJSON bool, h Human, m Machine) {
	fmt.Println(renderReply(text, asJSON, h, m))
}

// renderReply is PrintReply without the writing, so the decision is testable.
func renderReply(text string, asJSON bool, h Human, m Machine) string {
	if asJSON {
		if m != nil {
			if out, ok := m(text); ok {
				return out
			}
		}
		return JSONSafe(text, true)
	}
	if h != nil {
		if out, ok := h(text); ok {
			return out
		}
	}
	// Sanitised as a document, not as a field: invariant 7 wants the escapes
	// gone, and this is the path an unrecognised — so possibly hostile — reply
	// takes, but flattening dsx's own line breaks in the process would be the
	// cure doing the damage.
	return fmtutil.PrintableDoc(indentJSON(strings.TrimSpace(text)))
}

// indentJSON passes anything that is not JSON through: get_claude_design_prompt
// answers in prose, and a one-line JSON blob is the wire's shape rather than an
// answer to a person.
func indentJSON(text string) string {
	if !json.Valid([]byte(text)) {
		return text
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(text), "", "  "); err != nil {
		return text
	}
	return buf.String()
}

func JSONSafe(text string, asJSON bool) string {
	if !asJSON {
		return text
	}
	if json.Valid([]byte(text)) {
		return text
	}
	b, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return text
	}
	return string(b)
}

func EmitFlagged(ctx context.Context, c *mcp.Client, name string, args []string, build func(pos []string) (string, map[string]any, error), h Human) error {
	flags := NewFlagSet(name)
	asJSON := JSONFlag(flags)
	pos, err := ParseArgs(flags, args)
	if err != nil {
		return err
	}
	tool, toolArgs, err := build(pos)
	if err != nil {
		return err
	}
	return Emit(ctx, c, tool, toolArgs, *asJSON, h)
}

func EmitWrite(ctx context.Context, c *mcp.Client, tool string, args map[string]any, projectID string, paths []string, asJSON bool, h Human) error {
	var (
		text string
		err  error
	)
	if _, given := args["plan_token"]; given {
		text, err = c.CallTool(ctx, tool, args)
	} else {
		text, err = syncer.CallWithGrant(ctx, c, tool, args, projectID, paths)
	}
	if err != nil {
		if conflicts, ok := mcp.ConflictFromToolError(err); ok {
			return dsxerr.Conflict(conflicts, "the server changed since dsx read it; nothing was written")
		}
		return err
	}
	PrintReply(text, asJSON, h)
	return nil
}
