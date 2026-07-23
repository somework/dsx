package reply

import (
	"encoding/json"
	"strings"
	"testing"
)

// `--json` on conv never relayed the server's shape and could not: the reply is
// a tag, a body and a notice, which is not a JSON document at all, so it went
// out wrapped as {"text":…} — dsx's shape already, just an unusable one. `jq`
// could reach the whole transcript as one string and nothing inside it.
//
// So the choice here is between two dsx shapes, not between dsx's and the
// server's, which is what makes shaping it deliberate rather than a broken
// promise. The shape must hold for the case that motivated it: at the cap the
// inner body is cut mid-string and is NOT parseable, so "unwrap the tag" alone
// answers `jq: parse error` on exactly the projects that need it.
func TestConversationJSONIsAlwaysOneValidDocument(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, in string }{
		{"whole", realConvWhole},
		{"narrowable", realConvNarrowable},
		{"single chat over the cap", realConvSingleOverCap},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, ok := ConversationJSON(tc.in)
			if !ok {
				t.Fatalf("refused a measured reply:\n%s", tc.in)
			}
			if !json.Valid([]byte(out)) {
				t.Fatalf("emitted something jq cannot read:\n%s", out)
			}
			var doc map[string]any
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatal(err)
			}
			if doc["untrusted"] != true {
				t.Error("the untrusted marker did not survive unwrapping; the tag and its " +
					"warning line are the only one the wire carries, and stripping them " +
					"silently is how a transcript stops looking like user-authored data")
			}
			// transcript XOR body: a reader must be able to tell a parsed
			// transcript from a raw cut one without re-parsing to find out.
			_, parsed := doc["transcript"]
			_, raw := doc["body"]
			if parsed == raw {
				t.Errorf("transcript present=%v body present=%v; exactly one must be", parsed, raw)
			}
			if strings.Contains(out, "untrusted-project-content") {
				t.Errorf("the wire framing survived into the document:\n%s", out)
			}
		})
	}
}

func TestConversationJSONReportsWhatWasDroppedAndWhereToLook(t *testing.T) {
	t.Parallel()

	out, ok := ConversationJSON(realConvNarrowable)
	if !ok {
		t.Fatal("refused a measured truncated reply")
	}
	var doc struct {
		ProjectID string `json:"project_id"`
		Truncated *struct {
			BytesDropped int      `json:"bytes_dropped"`
			NarrowTo     []string `json:"narrow_to"`
		} `json:"truncated"`
		Transcript json.RawMessage `json:"transcript"`
		Body       string          `json:"body"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.ProjectID != "dddddddd-dddd-4ddd-8ddd-dddddddddddd" {
		t.Errorf("project_id = %q", doc.ProjectID)
	}
	if doc.Truncated == nil {
		t.Fatal("a truncated reply carries no truncated key")
	}
	if doc.Truncated.BytesDropped != 197193 {
		t.Errorf("bytes_dropped = %d, want 197193", doc.Truncated.BytesDropped)
	}
	if len(doc.Truncated.NarrowTo) != 1 || doc.Truncated.NarrowTo[0] != "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee" {
		t.Errorf("narrow_to = %v", doc.Truncated.NarrowTo)
	}
	// The cut body is still reachable: README calls --json the escape hatch for
	// the bytes the human path withholds, and dropping them here would leave no
	// way to see them at all.
	if !strings.Contains(doc.Body, "ELIDED-TRANSCRIPT-BODY") {
		t.Error("the cut body is not reachable through --json")
	}

	// A chat that is itself over the cap has nothing to narrow to, and an empty
	// list must be absent rather than present-and-empty: `jq -e .truncated.narrow_to`
	// is the natural test for "is there somewhere to go", and [] answers yes.
	out, ok = ConversationJSON(realConvSingleOverCap)
	if !ok {
		t.Fatal("refused the single-chat reply")
	}
	if strings.Contains(out, "narrow_to") {
		t.Errorf("offered somewhere to narrow to for a chat already over the cap:\n%s", out)
	}
}

// The whole case is the one where jq gets real structure rather than a string.
func TestAWholeTranscriptBecomesRealJSON(t *testing.T) {
	t.Parallel()
	out, ok := ConversationJSON(realConvWhole)
	if !ok {
		t.Fatal("refused the measured whole reply")
	}
	var doc struct {
		Transcript struct {
			Chats map[string]struct {
				Title string `json:"title"`
			} `json:"chats"`
		} `json:"transcript"`
		Truncated any `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Truncated != nil {
		t.Error("an untruncated reply carries a truncated key")
	}
	c, ok := doc.Transcript.Chats["12121212-1212-4121-8121-121212121212"]
	if !ok {
		t.Fatalf("the transcript did not survive as addressable JSON:\n%s", out)
	}
	if c.Title != "Chat" {
		t.Errorf("title = %q, want %q", c.Title, "Chat")
	}
}

// JSON counts \r, \n and \t as whitespace BETWEEN tokens, so a transcript can
// be entirely valid JSON and still carry bytes that rewrite a terminal line.
// Invariant 7 does not stop applying because the reader is a program: --json is
// read by people at a prompt too, and a RawMessage passed through verbatim is
// the one place in this package where server bytes reach stdout unexamined.
func TestValidJSONCannotSmuggleAControlByteThroughTheTranscript(t *testing.T) {
	t.Parallel()
	in := strings.Replace(realConvWhole,
		`{"chats":`, "{\r\"chats\":", 1)
	if !strings.ContainsRune(in, '\r') {
		t.Fatal("fixture did not take the carriage return")
	}
	out, ok := ConversationJSON(in)
	if !ok {
		t.Fatal("refused a reply that is valid JSON inside")
	}
	if !json.Valid([]byte(out)) {
		t.Fatalf("emitted invalid JSON:\n%q", out)
	}
	if strings.ContainsRune(out, '\r') {
		t.Errorf("a carriage return reached stdout through the transcript:\n%q", out)
	}
}

// Same rule as every renderer in this package: a shape dsx has not measured
// falls through to what the caller already did with it.
func TestConversationJSONRefusesWhatIsNotItsShape(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, in string }{
		{"a bare JSON reply", realFiles},
		{"prose", "the design system uses a 4px grid"},
		{"no closing tag", strings.Replace(realConvWhole, "</untrusted-project-content>", "", 1)},
		{"a notice with no byte count", strings.Replace(realConvNarrowable,
			"[+197193 bytes truncated", "[+ bytes truncated", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if out, ok := ConversationJSON(tc.in); ok {
				t.Errorf("shaped a reply it does not know:\n%s", out)
			}
		})
	}
}
