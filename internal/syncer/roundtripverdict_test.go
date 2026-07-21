package syncer

import (
	"fmt"
	"strings"
	"testing"
)

// roundTripVerdict judges what TestLiveEndToEndPullPushRoundTrip observes: a
// cold push writes exactly the one path, the ledger records the project and
// the bytes it pushed, a warm push writes nothing, and the pull returns the
// bytes byte-exact. No build tag, for the reason etagVerdict has none.
func roundTripVerdict(written []string, st State, warmWritten []string, pulled []byte, path, projectID, body string) error {
	switch {
	case len(written) != 1 || written[0] != path:
		return fmt.Errorf("pushed %v, want just %s", written, path)
	case st.ProjectID != projectID:
		return fmt.Errorf("ledger pinned %q, want %q", st.ProjectID, projectID)
	case st.Files[path].SHA != SHA256Hex([]byte(body)):
		return fmt.Errorf("the ledger did not record the bytes it pushed (%s want %s); "+
			"the next sync would call this a conflict", st.Files[path].SHA, SHA256Hex([]byte(body)))
	case len(warmWritten) != 0:
		return fmt.Errorf("a warm push rewrote %v; unchanged files are supposed to cost nothing", warmWritten)
	case string(pulled) != body:
		return fmt.Errorf("push→pull is not byte-exact:\n want %q\n  got %q", body, pulled)
	}
	return nil
}

func TestRoundTripVerdict(t *testing.T) {
	const (
		path = "a.css"
		proj = "proj-1"
		body = "x { color: red }\n"
	)
	good := func() ([]string, State, []string, []byte) {
		return []string{path},
			State{ProjectID: proj, Files: map[string]FileState{path: {SHA: SHA256Hex([]byte(body))}}},
			nil,
			[]byte(body)
	}

	t.Run("the shape a round trip is supposed to produce", func(t *testing.T) {
		w, st, warm, pulled := good()
		if err := roundTripVerdict(w, st, warm, pulled, path, proj, body); err != nil {
			t.Fatalf("a correct round trip was rejected: %v", err)
		}
	})

	cases := []struct {
		name    string
		mutate  func(*[]string, *State, *[]string, *[]byte)
		wantMsg string
	}{
		{"nothing was pushed", func(w *[]string, st *State, warm *[]string, p *[]byte) { *w = nil }, "want just"},
		{"the wrong path was pushed", func(w *[]string, st *State, warm *[]string, p *[]byte) { *w = []string{"other.css"} }, "want just"},
		{"more than the one path was pushed", func(w *[]string, st *State, warm *[]string, p *[]byte) { *w = []string{path, "other.css"} }, "want just"},
		{"the ledger pinned another project", func(w *[]string, st *State, warm *[]string, p *[]byte) { st.ProjectID = "elsewhere" }, "ledger pinned"},
		{"the ledger recorded other bytes", func(w *[]string, st *State, warm *[]string, p *[]byte) {
			st.Files = map[string]FileState{path: {SHA: "not-the-sha"}}
		}, "did not record the bytes"},
		{"the ledger recorded nothing for the path", func(w *[]string, st *State, warm *[]string, p *[]byte) {
			st.Files = map[string]FileState{}
		}, "did not record the bytes"},
		{"the warm push rewrote the file", func(w *[]string, st *State, warm *[]string, p *[]byte) { *warm = []string{path} }, "warm push rewrote"},
		{"the pull came back with different bytes", func(w *[]string, st *State, warm *[]string, p *[]byte) { *p = []byte("tampered\n") }, "not byte-exact"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, st, warm, pulled := good()
			c.mutate(&w, &st, &warm, &pulled)
			err := roundTripVerdict(w, st, warm, pulled, path, proj, body)
			if err == nil {
				t.Fatalf("accepted a round trip where %s", c.name)
			}
			if !strings.Contains(err.Error(), c.wantMsg) {
				t.Errorf("error %q does not name %q", err, c.wantMsg)
			}
		})
	}
}
