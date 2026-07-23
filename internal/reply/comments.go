package reply

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/somework/dsx/internal/fmtutil"
)

// CommentsReply is list_comments' measured envelope. server_time is a watermark
// to pass back as changed_since, not decoration — it is the one incremental-read
// mechanism this server offers, and get_conversation notably lacks it.
type CommentsReply struct {
	Comments   []json.RawMessage `json:"comments"`
	ServerTime string            `json:"server_time"`
}

// DecodeComments accepts the measured envelope. Both fields are required: a
// reply carrying comments but no watermark would be rendered as though the
// caller could resume from it.
func DecodeComments(text string) (CommentsReply, bool) {
	var r CommentsReply
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		return r, false
	}
	if r.Comments == nil || r.ServerTime == "" {
		return r, false
	}
	return r, true
}

// Comments renders the empty case only, for the same reason Members does: every
// project reachable from here has zero comments, and there is no tool to create
// one — they are left by people clicking pins in Claude Design. So the element's
// shape is unmeasured, and a table drawn from the description rather than from
// bytes is exactly the guess this package refuses to make.
func Comments(text string) (string, bool) {
	r, ok := DecodeComments(text)
	if !ok || len(r.Comments) > 0 {
		return "", false
	}
	return "no comments — pin-anchored feedback left in Claude Design; none here\n" +
		"  since  " + fmtutil.Printable(r.ServerTime), true
}

// AckReply is ack_comments' measured reply. not_queued is not an error list:
// the server returns ids whose flag was already clear, which is how a
// crash-safe read/act/ack loop stays idempotent.
type AckReply struct {
	Acked     []string `json:"acked"`
	NotQueued []string `json:"not_queued"`
}

func DecodeAcked(text string) (AckReply, bool) {
	var r AckReply
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		return r, false
	}
	// Neither may be nil. Both keys are present in the measured reply even when
	// empty, and a decoder that tolerated a missing one would accept a
	// write_files ack — which carries neither — as an ack of nothing.
	if r.Acked == nil || r.NotQueued == nil {
		return r, false
	}
	return r, true
}

func Acked(text string) (string, bool) {
	r, ok := DecodeAcked(text)
	if !ok {
		return "", false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s handled", plural(len(r.Acked), "comment", "comments"))
	if len(r.NotQueued) > 0 {
		// Said plainly rather than counted away: an id that was already clear
		// usually means someone else answered it, and that is worth noticing.
		fmt.Fprintf(&b, ", %d already clear", len(r.NotQueued))
	}
	for _, id := range r.Acked {
		fmt.Fprintf(&b, "\n  acked   %s", fmtutil.Printable(id))
	}
	for _, id := range r.NotQueued {
		fmt.Fprintf(&b, "\n  clear   %s", fmtutil.Printable(id))
	}
	return b.String(), true
}
