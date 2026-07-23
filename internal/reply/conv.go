package reply

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/somework/dsx/internal/fmtutil"
)

// get_conversation's framing, measured 2026-07-23. The tool's blurb claims the
// reply is wrapped "the same way read_file wraps file content" and it is not:
// the tag is its own, the only attribute is project_id, and there is neither an
// etag nor a lines range — mcp.ParseEnvelope refuses it on the missing etag.
// The truncation notice is appended AFTER the closing tag, where read_file puts
// its own inside the body.
const (
	convOpenTag    = "<untrusted-project-content "
	convCloseTag   = "</untrusted-project-content>"
	convTruncMark  = " bytes truncated"
	convNarrowMark = "pass chat_id to narrow"
	convSingleMark = "this single chat exceeds the cap"
	convAvailMark  = "available: open: "
)

// ConversationReply is what a person needs from a get_conversation reply that
// hit the cap. The transcript itself is deliberately not a field: at the cap it
// is a quarter of a megabyte of unparseable JSON, and the reason dsx exists is
// that bytes like those must not pass through a caller's context to say one
// thing that fits on a line.
type ConversationReply struct {
	ProjectID string
	Truncated bool
	Dropped   int
	Chats     []string
}

// DecodeConversation accepts the measured framing and refuses everything else,
// including a truncation notice worded in a way dsx has not seen. Summarising
// an unrecognised notice would state a byte count and a chat id read out of
// prose that no longer means what it did.
func DecodeConversation(text string) (ConversationReply, bool) {
	c, _, tail, ok := convSplit(text)
	if !ok {
		return c, false
	}
	mark := strings.Index(tail, convTruncMark)
	if mark < 0 {
		return c, true
	}
	open := strings.LastIndex(tail[:mark], "[+")
	if open < 0 {
		return c, false
	}
	n, err := strconv.Atoi(tail[open+len("[+") : mark])
	if err != nil {
		return c, false
	}
	c.Truncated, c.Dropped = true, n

	notice := tail[mark:]
	switch {
	case strings.Contains(notice, convNarrowMark):
		// A narrowing notice that names no chat is the one shape that would
		// render as advice the caller cannot act on.
		if c.Chats = convChats(notice); len(c.Chats) == 0 {
			return c, false
		}
	case strings.Contains(notice, convSingleMark):
	default:
		return c, false
	}
	return c, true
}

// convSplit cuts the framing into the header's attributes, the wrapped body and
// the tail that may carry the notice.
//
// The body is returned here rather than carried on ConversationReply so that it
// reaches the machine path and cannot reach the human one: Conversation builds
// its summary from the struct alone, and at the cap that body is a quarter of a
// megabyte of unparseable JSON whose whole point is not to be printed.
//
// Splitting at the closing tag is also what keeps the notice honest. A
// transcript is user-authored and free to contain the same words, so a notice
// read from anywhere but the tail would let a hostile chat name a chat id of
// its choosing.
func convSplit(text string) (c ConversationReply, body, tail string, ok bool) {
	if !strings.HasPrefix(text, convOpenTag) {
		return c, "", "", false
	}
	tagEnd := strings.Index(text, ">")
	if tagEnd < 0 {
		return c, "", "", false
	}
	if c.ProjectID = attrValue(text[:tagEnd], "project_id"); c.ProjectID == "" {
		return c, "", "", false
	}
	closeAt := strings.Index(text, convCloseTag)
	if closeAt < 0 {
		return c, "", "", false
	}
	return c, strings.TrimSpace(text[tagEnd+1 : closeAt]), text[closeAt+len(convCloseTag):], true
}

// convChats reads the ids out of the notice's own grammar rather than by
// pattern-matching anything UUID-shaped, so a stray id inside the prose cannot
// be picked up. Only the measured `open:` list is understood; a notice that
// also names closed chats parses to nothing and the whole reply falls through
// unrendered, which is the right answer for a shape nobody has measured.
func convChats(notice string) []string {
	i := strings.Index(notice, convAvailMark)
	if i < 0 {
		return nil
	}
	rest := notice[i+len(convAvailMark):]
	if j := strings.Index(rest, ")"); j >= 0 {
		rest = rest[:j]
	}
	var out []string
	for _, s := range strings.Split(rest, ",") {
		if s = strings.TrimSpace(s); len(s) == idWidth {
			out = append(out, s)
		}
	}
	return out
}

func attrValue(tag, name string) string {
	i := strings.Index(tag, name+`="`)
	if i < 0 {
		return ""
	}
	rest := tag[i+len(name)+2:]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// Conversation refuses a complete transcript on purpose: that body is the
// answer `dsx conv get` was asked for and the caller must receive it whole.
// It answers only the truncated case, where the body is cut mid-JSON and the
// single actionable fact is which chat to ask for next.
func Conversation(text string) (string, bool) {
	c, ok := DecodeConversation(text)
	if !ok || !c.Truncated {
		return "", false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "transcript truncated at get_conversation's 256 KiB cap; %d bytes dropped\n", c.Dropped)
	switch {
	case len(c.Chats) == 1:
		id := fmtutil.Printable(c.Chats[0])
		fmt.Fprintf(&b, "  chat      %s\n", id)
		fmt.Fprintf(&b, "  narrow    dsx conv get <project> --chat %s\n", id)
	case len(c.Chats) > 1:
		for _, id := range c.Chats {
			fmt.Fprintf(&b, "  chat      %s\n", fmtutil.Printable(id))
		}
		b.WriteString("  narrow    dsx conv get <project> --chat <id>\n")
	default:
		// Naming --chat here would send the caller round a loop with no exit:
		// the one chat that exists is already the one over the cap.
		b.WriteString("  this single chat exceeds the cap; the tail cannot be fetched\n")
	}
	// Named `cut body`, not `raw body`: --json no longer relays the wire, it
	// carries this same cut body under a key of dsx's own. Promising raw bytes
	// there would be a line that stopped being true the moment the machine path
	// gained a shape.
	b.WriteString("  cut body  dsx conv get <project> --json")
	return b.String(), true
}

// ConversationDoc is dsx's own shape and says so: `--json` on conv never
// carried the server's, because the reply is a tag, a body and a notice rather
// than a JSON document, so it went out wrapped as {"text":…} and `jq` could
// reach the transcript only as one long string.
//
// transcript and body are exclusive and that is the contract: at the cap the
// wrapped body is cut mid-string and does not parse, so a reader must be able
// to tell structure from salvage without trying to parse it to find out.
type ConversationDoc struct {
	ProjectID string `json:"project_id"`
	// Untrusted is always true. The wire's own marker — the tag and the warning
	// line under it — is what unwrapping removes, and a transcript is
	// user-authored data that may read like instructions.
	Untrusted  bool            `json:"untrusted"`
	Transcript json.RawMessage `json:"transcript,omitempty"`
	Body       string          `json:"body,omitempty"`
	Truncated  *ConvTruncation `json:"truncated,omitempty"`
}

// ConvTruncation carries only what a reader can act on. There is no
// `narrowable` bool beside NarrowTo: two fields would admit a fourth state that
// cannot happen, and an empty list is omitted rather than sent as [] so that
// `jq -e .truncated.narrow_to` answers the question it looks like it asks.
type ConvTruncation struct {
	BytesDropped int      `json:"bytes_dropped"`
	NarrowTo     []string `json:"narrow_to,omitempty"`
}

// ConversationJSON shapes get_conversation for a program. It refuses anything
// that is not the measured framing, and the caller falls back to wrapping the
// reply as it arrived.
func ConversationJSON(text string) (string, bool) {
	_, body, _, ok := convSplit(text)
	if !ok {
		return "", false
	}
	c, ok := DecodeConversation(text)
	if !ok {
		return "", false
	}
	doc := ConversationDoc{ProjectID: c.ProjectID, Untrusted: true}
	if c.Truncated {
		doc.Truncated = &ConvTruncation{BytesDropped: c.Dropped, NarrowTo: c.Chats}
	}
	// Compacted rather than passed through: JSON counts \r as whitespace
	// between tokens, so a valid transcript can still move a terminal's cursor,
	// and invariant 7 does not stop applying because the reader is a program.
	// The check is the discriminator, not a repair: at the cap the body is cut
	// mid-string, and which of transcript/body is filled is the whole contract.
	//
	// Nothing compacts it here on purpose. json.Marshal compacts a RawMessage
	// itself, which also disarms the \r that JSON counts as whitespace between
	// tokens — invariant 7's concern on this path. A json.Compact of dsx's own
	// beside it was tried and removed: two mutations proved it guarded nothing
	// the stdlib was not already guarding.
	if json.Valid([]byte(body)) {
		doc.Transcript = json.RawMessage(body)
	} else {
		doc.Body = body
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return "", false
	}
	return string(out), true
}
