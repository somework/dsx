package syncer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// missingFromReference is the live tools/list check's judgment, kept outside the
// live build tag so an ordinary `go test` compiles and exercises it. Under the
// tag it would be unreachable to CI — which never builds the live suite — and a
// mutation inside it would fail nothing.
//
// The direction it answers is the one nothing checked: the existing assertions
// ask whether what dsx knows about is still on the server, never whether the
// server has grown something dsx has not recorded.
func missingFromReference(serverTools, recorded map[string]bool) []string {
	var missing []string
	for name := range serverTools {
		if !recorded[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

func recordedToolNames(t *testing.T) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "reference", "mcp-tools.json"))
	if err != nil {
		t.Fatalf("reading the recorded tools/list: %v", err)
	}
	var m struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parsing the recorded tools/list: %v", err)
	}
	names := make(map[string]bool, len(m.Tools))
	for _, tool := range m.Tools {
		names[tool.Name] = true
	}
	if len(names) == 0 {
		t.Fatal("the recorded tools/list carried no tools; every check reading it would pass vacuously")
	}
	return names
}

func TestMissingFromReferenceNamesOnlyWhatTheServerGained(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		server   []string
		recorded []string
		want     []string
	}{
		{
			// The real shape of the defect this exists for: the server had
			// grown three tools and every offline guard stayed green because
			// the reference had never heard of them.
			name:     "the server gained tools",
			server:   []string{"read_file", "list_comments", "ack_comments", "read_design_skill"},
			recorded: []string{"read_file"},
			want:     []string{"ack_comments", "list_comments", "read_design_skill"},
		},
		{
			name:     "in step",
			server:   []string{"read_file", "list_files"},
			recorded: []string{"read_file", "list_files"},
			want:     nil,
		},
		{
			// The other direction is a different test's job and must not be
			// reported here: a reference naming something the server dropped is
			// staleness of the opposite kind, and conflating them would make one
			// message advise the wrong repair.
			name:     "the reference names more than the server",
			server:   []string{"read_file"},
			recorded: []string{"read_file", "retired_tool"},
			want:     nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			set := func(xs []string) map[string]bool {
				m := map[string]bool{}
				for _, x := range xs {
					m[x] = true
				}
				return m
			}
			got := missingFromReference(set(tc.server), set(tc.recorded))
			if len(got) != len(tc.want) {
				t.Fatalf("missing = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("missing = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// The offline half of the same concern: the reference must at least parse and
// name every tool dsx reaches by name. This cannot see a tool the server added
// — only the live run can — but it catches a reference truncated or corrupted
// in an edit, which is what would silently disarm every guard that reads it.
func TestTheRecordedToolsListNamesEveryToolDsxCallsByName(t *testing.T) {
	t.Parallel()
	recorded := recordedToolNames(t)
	for _, name := range []string{
		"list_projects", "get_project", "create_project", "update_sharing",
		"list_design_systems", "list_files", "read_file", "write_files",
		"delete_files", "copy_files", "finalize_plan", "render_preview",
		"create_support_js", "get_conversation", "put_conversation",
		"list_members", "add_member", "remove_member", "update_member_role",
		"get_claude_design_prompt", "list_comments", "ack_comments",
		"read_design_skill",
	} {
		if !recorded[name] {
			t.Errorf("reference/mcp-tools.json does not record %q", name)
		}
	}
}
