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

// toolShape is the part of a tool's schema dsx actually depends on. Descriptions
// are deliberately excluded: server prose churns, and a guard that cries on
// every rewording is a guard people learn to ignore.
type toolShape struct {
	Props    map[string]bool
	Required map[string]bool
	ReadOnly bool
}

// schemaDrift is the check one level below missingFromReference, and it exists
// because that one compares NAMES only. Both defects this session found lived
// inside a schema whose name never changed: list_files silently gained `depth`
// (a whole tree in one call, unused for who knows how long) and render_preview
// silently lost `render` (a dsx flag that went on being accepted, documented
// and inert). Comparing name sets could not see either.
func schemaDrift(recorded, live map[string]toolShape) []string {
	var out []string
	for name, r := range recorded {
		l, ok := live[name]
		if !ok {
			continue // a tool the server dropped is missingFromReference's mirror, not this
		}
		for p := range l.Props {
			if !r.Props[p] {
				out = append(out, name+": server gained argument "+p)
			}
		}
		for p := range r.Props {
			if !l.Props[p] {
				out = append(out, name+": server dropped argument "+p)
			}
		}
		for p := range l.Required {
			if !r.Required[p] {
				out = append(out, name+": "+p+" became required")
			}
		}
		for p := range r.Required {
			if !l.Required[p] {
				out = append(out, name+": "+p+" stopped being required")
			}
		}
		if r.ReadOnly != l.ReadOnly {
			out = append(out, name+": readOnlyHint changed")
		}
	}
	sort.Strings(out)
	return out
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

func TestSchemaDriftNamesArgumentsGainedAndLost(t *testing.T) {
	t.Parallel()
	set := func(xs ...string) map[string]bool {
		m := map[string]bool{}
		for _, x := range xs {
			m[x] = true
		}
		return m
	}
	recorded := map[string]toolShape{
		"list_files":     {Props: set("project_id", "path"), Required: set("project_id"), ReadOnly: true},
		"render_preview": {Props: set("project_id", "path", "render"), Required: set("project_id", "path")},
		"steady":         {Props: set("a"), Required: set("a"), ReadOnly: true},
		"retired":        {Props: set("a")},
	}
	live := map[string]toolShape{
		// The two real defects, as they actually arrived.
		"list_files":     {Props: set("project_id", "path", "depth"), Required: set("project_id"), ReadOnly: true},
		"render_preview": {Props: set("project_id", "path", "validators"), Required: set("project_id", "path")},
		"steady":         {Props: set("a"), Required: set("a"), ReadOnly: true},
	}
	want := []string{
		"list_files: server gained argument depth",
		"render_preview: server dropped argument render",
		"render_preview: server gained argument validators",
	}
	got := schemaDrift(recorded, live)
	if len(got) != len(want) {
		t.Fatalf("drift = %v\nwant %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("drift = %v\nwant %v", got, want)
		}
	}
}

func TestSchemaDriftIsSilentWhenNothingMoved(t *testing.T) {
	t.Parallel()
	same := map[string]toolShape{
		"read_file": {Props: map[string]bool{"project_id": true}, Required: map[string]bool{"project_id": true}, ReadOnly: true},
	}
	if got := schemaDrift(same, same); len(got) != 0 {
		t.Errorf("drift = %v on identical inputs; a guard that always fires is one people switch off", got)
	}
}

// decodeToolShapes reads the shape map out of a tools/list document — the
// recorded one on disk or the live one from the server, which are the same
// format. Untagged, so the parsing is exercised by an ordinary `go test`.
func decodeToolShapes(b []byte) (map[string]toolShape, error) {
	var m struct {
		Tools []struct {
			Name        string `json:"name"`
			Annotations struct {
				ReadOnlyHint bool `json:"readOnlyHint"`
			} `json:"annotations"`
			InputSchema struct {
				Properties map[string]json.RawMessage `json:"properties"`
				Required   []string                   `json:"required"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	out := make(map[string]toolShape, len(m.Tools))
	for _, t := range m.Tools {
		s := toolShape{
			Props:    map[string]bool{},
			Required: map[string]bool{},
			ReadOnly: t.Annotations.ReadOnlyHint,
		}
		for p := range t.InputSchema.Properties {
			s.Props[p] = true
		}
		for _, p := range t.InputSchema.Required {
			s.Required[p] = true
		}
		out[t.Name] = s
	}
	return out, nil
}

func recordedToolShapes(t *testing.T) map[string]toolShape {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "reference", "mcp-tools.json"))
	if err != nil {
		t.Fatal(err)
	}
	shapes, err := decodeToolShapes(b)
	if err != nil {
		t.Fatalf("parsing the recorded tools/list: %v", err)
	}
	if len(shapes) == 0 {
		t.Fatal("no shapes read; the drift check would pass vacuously")
	}
	return shapes
}

// The offline floor under decodeToolShapes: the real file must yield the two
// facts the drift check compares, or the live guard would run on empty sets.
func TestTheRecordedShapesCarryArgumentsAndHints(t *testing.T) {
	t.Parallel()
	shapes := recordedToolShapes(t)
	lf, ok := shapes["list_files"]
	if !ok {
		t.Fatal("list_files has no recorded shape")
	}
	for _, p := range []string{"project_id", "path", "depth"} {
		if !lf.Props[p] {
			t.Errorf("list_files' recorded shape is missing %q", p)
		}
	}
	if !lf.Required["project_id"] || !lf.ReadOnly {
		t.Error("list_files' recorded shape lost its required set or its readOnlyHint")
	}
	if shapes["render_preview"].Props["render"] {
		t.Error("render_preview still records a `render` argument; the server dropped it " +
			"and dsx dropped the flag that sent it")
	}
}
