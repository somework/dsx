package plans

import (
	"slices"
	"testing"

	"github.com/somework/dsx/internal/clitest"
)

// These aliases keep the test body in its original spelling; every one is the
// clitest name. See internal/cli/fake_test.go for why the adapter lives in
// internal/clitest rather than being reimplemented here.
type fakeReply = clitest.Reply

var (
	newFakeMCP = clitest.New
	fakeClient = clitest.Client
)

func TestSupportJSSelfAuthorisesUsingTheServersDocumentedDefaultPath(t *testing.T) {
	// `dsx support-js p1` — the documented form — skipped the grant recovery on
	// the theory that there was nothing to name in a plan. The server's own
	// schema says otherwise: path "defaults to support.js at the project root".
	var planned []string
	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "create_support_js":
			if _, ok := args["plan_token"]; !ok {
				return fakeReply{
					HTTPStatus: 403,
					HTTPBody:   `{"error":"needs_project_grant","project_id":"p1"}`,
				}
			}
			return fakeReply{Text: `{"path":"support.js"}`}
		case "finalize_plan":
			if w, ok := args["writes"].([]any); ok {
				for _, p := range w {
					planned = append(planned, p.(string))
				}
			}
			return fakeReply{Text: `{"plan_token":"tok"}`}
		}
		return fakeReply{Text: "unexpected " + name, IsError: true}
	})

	if err := cmdSupportJS(t.Context(), fakeClient(f), []string{"p1"}); err != nil {
		t.Fatalf("support-js with no --path did not recover from needs_project_grant: %v", err)
	}
	if !slices.Contains(planned, "support.js") {
		t.Errorf("finalize_plan authorised %v, want the server's documented default support.js", planned)
	}
}
