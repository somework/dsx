package projects

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/somework/dsx/internal/clitest"
)

type fakeReply = clitest.Reply

var (
	newFakeMCP    = clitest.New
	fakeClient    = clitest.Client
	captureStdout = clitest.CaptureStdout
)

// PROTOCOL.md: list_projects answers with a bare array of {id, name, url}.
const twoProjects = `[
  {"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","name":"dsx sandbox","url":"https://x/1"},
  {"id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","name":"Acme Design System","url":"https://x/2"}
]`

func projectsFake(t *testing.T, text string) *clitest.Server {
	t.Helper()
	return newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		return fakeReply{Text: text}
	})
}

func runProjects(t *testing.T, text string, args ...string) string {
	t.Helper()
	out, err := captureStdout(t, func() error {
		return cmdProjects(context.Background(), fakeClient(projectsFake(t, text)), args)
	})
	if err != nil {
		t.Fatalf("projects: %v", err)
	}
	return out
}

// projects is the documented entry point, and it printed the server's blob —
// the id had to be picked out with the mouse.
func TestProjectsPrintsIDThenName(t *testing.T) {
	out := runProjects(t, twoProjects)

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want two rows and a count:\n%s", len(lines), out)
	}
	// id first: names hold spaces, so `awk '{print $1}'` only works this way.
	if !strings.HasPrefix(lines[0], "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa") {
		t.Errorf("row does not open with the id:\n%s", lines[0])
	}
	if !strings.Contains(lines[0], "dsx sandbox") {
		t.Errorf("row does not carry the name:\n%s", lines[0])
	}
	if lines[2] != "2 projects" {
		t.Errorf("count line = %q, want \"2 projects\"", lines[2])
	}
}

func TestProjectsFirstFieldIsTheWholeID(t *testing.T) {
	out := runProjects(t, twoProjects)
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if strings.HasSuffix(line, "projects") {
			continue
		}
		if id := strings.Fields(line)[0]; len(id) != 36 {
			t.Errorf("first field %q is %d chars; a cut id is worse than no id", id, len(id))
		}
	}
}

// A name arrives from the server, so it is untrusted input (invariant 7): a
// carriage return would rewrite the row above it.
func TestAProjectNameCannotRewriteTheTerminal(t *testing.T) {
	hostile := `[{"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",` +
		`"name":"a\rWIPED\u001b[2K","url":"u"}]`
	out := runProjects(t, hostile)
	for _, bad := range []string{"\r", "\x1b"} {
		if strings.Contains(out, bad) {
			t.Errorf("output carries %q from a server-supplied name: %q", bad, out)
		}
	}
	if !strings.Contains(out, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa") {
		t.Errorf("the row was dropped rather than sanitised: %q", out)
	}
}

// The fallback is the path a hostile reply takes when it does not decode, so
// it cannot be the one place that prints server text raw.
func TestAnUnrecognisedReplyIsSanitisedToo(t *testing.T) {
	out := runProjects(t, "surprise\rWIPED\x1b[2K")
	for _, bad := range []string{"\r", "\x1b"} {
		if strings.Contains(out, bad) {
			t.Errorf("the passthrough branch printed %q raw: %q", bad, out)
		}
	}
}

// A reply that does not match the measured shape is passed through verbatim
// rather than rendered from guesses: PROTOCOL.md's claim is measured, not
// guaranteed, and three protocol details have already been guessed wrong.
func TestAnUnrecognisedReplyIsPassedThroughVerbatim(t *testing.T) {
	for _, text := range []string{
		`not json at all`,
		`{"projects":[]}`,
		`[{"name":"no id here"}]`,
		`[{"id":"","name":"empty id"}]`,
		`["a string, not an object"]`,
	} {
		out := runProjects(t, text)
		if strings.TrimSpace(out) != strings.TrimSpace(text) {
			t.Errorf("reply %q was reformatted into %q instead of passed through", text, out)
		}
	}
}

func TestAnEmptyListSaysSo(t *testing.T) {
	out := runProjects(t, `[]`)
	if strings.TrimSpace(out) != "0 projects" {
		t.Errorf("got %q, want \"0 projects\"", out)
	}
}

// --json gets dsx's own shape, as tree --json does. Reformatting the human
// half and leaving the machine half an unparsed blob would be backwards.
func TestProjectsJSONIsDsxsOwnShape(t *testing.T) {
	out := runProjects(t, twoProjects, "--json")

	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("--json is not a JSON array: %v (%q)", err, out)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	for _, key := range []string{"id", "name", "url"} {
		if _, ok := rows[0][key]; !ok {
			t.Errorf("--json row lost %q", key)
		}
	}
}

func TestProjectsJSONPassesAnUnrecognisedReplyThrough(t *testing.T) {
	out := runProjects(t, `{"projects":[]}`, "--json")
	if strings.TrimSpace(out) != `{"projects":[]}` {
		t.Errorf("--json reformatted an unrecognised reply: %q", out)
	}
}
