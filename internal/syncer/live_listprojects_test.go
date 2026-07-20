//go:build live

package syncer

import (
	"encoding/json"
	"testing"
)

// PROTOCOL.md claims list_projects answers with a bare JSON array of
// {id, name, url}. Every claim in PROTOCOL.md has a test here; if this fails,
// PROTOCOL.md is wrong. Read-only, so TestLiveRefusesToCreateProjects is not
// implicated.
func TestLiveListProjectsIsABareArrayOfIDNameURL(t *testing.T) {
	c, ctx := liveClient(t)

	text, err := c.CallTool(ctx, "list_projects", map[string]any{})
	if err != nil {
		t.Fatalf("list_projects: %v", err)
	}

	// A bare array, not an object wrapping one, and not a read_file-style
	// envelope: dsx decodes it directly.
	var rows []map[string]any
	if err := json.Unmarshal([]byte(text), &rows); err != nil {
		t.Fatalf("list_projects did not answer with a JSON array: %v", err)
	}
	if len(rows) == 0 {
		t.Skip("this account lists no projects; nothing to assert about a row")
	}

	for i, row := range rows {
		for _, key := range []string{"id", "name", "url"} {
			v, ok := row[key]
			if !ok {
				t.Errorf("row %d has no %q; keys are %v", i, key, keysOf(row))
				continue
			}
			if _, isString := v.(string); !isString {
				t.Errorf("row %d field %q is %T, want string", i, key, v)
			}
		}
		// The id is what every other command takes as <project>, so its shape
		// is the part dsx actually depends on.
		if id, _ := row["id"].(string); len(id) != 36 {
			t.Errorf("row %d id is %d chars, want the 36 of a UUID", i, len(id))
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
