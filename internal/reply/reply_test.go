package reply

import (
	"strings"
	"testing"
)

// The inputs below are the bytes the real endpoint returned on 2026-07-23,
// captured one call at a time against the sandbox project. They are the whole
// evidence for every renderer here — a fixture invented to match the code
// would only prove the code equals itself, and the three protocol facts dsx
// got wrong were all wrong in exactly that way.
const (
	realDesignSystems = `[{"id":"dddddddd-dddd-4ddd-8ddd-dddddddddddd","name":"Placeholder Design System","is_default":true}]`
	realProject       = `{"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","name":"dsx-sandbox","sharing":{"link_permission":"view","scope":"invited","view_mode":"private"},"type":"PROJECT_TYPE_PROJECT","url":"https://claude.ai/design/p/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}`
	realMembers       = `[]`
	realFiles         = `[{"path":"components","type":"directory"},{"path":"tokens","type":"directory"},{"path":"README.md","type":"file","size":64,"etag":"1784534579692889"}]`
	realWrite         = `{"etags":{".dsx-selftest-fmt.css":"1784746061595762"},"url":"https://claude.ai/design/p/aaaaaaaa?file=.dsx-selftest-fmt.css","written":1}`
	realCopy          = `{"copied":[".dsx-selftest-fmt2.css"],"etags":{".dsx-selftest-fmt2.css":"1784746062315787"},"results":[{"copied":1,"dest":".dsx-selftest-fmt2.css","src":".dsx-selftest-fmt.css"}],"url":"https://claude.ai/design/p/aaaaaaaa?file=.dsx-selftest-fmt2.css"}`
	realDelete        = `{"deleted":2}`
	realSupportJS     = `{"bytes":66404,"etags":{".dsx-selftest/support.js":"1784746103797534"},"path":".dsx-selftest/support.js"}`
	realPlan          = `{"expires_at":1784262307,"plan_token":"plan_eyJhIjoxfQ","project_id":"aaaaaaaa","scope":"project"}`
)

func TestEveryRendererAnswersItsMeasuredReply(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		render func(string) (string, bool)
		in     string
		want   []string
	}{
		{"design systems", DesignSystems, realDesignSystems,
			[]string{"dddddddd-dddd-4ddd-8ddd-dddddddddddd", "Placeholder Design System", "(default)", "1 design system"}},
		{"project", Project, realProject,
			[]string{"dsx-sandbox", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "invited (link: view)", "https://claude.ai/design/p/"}},
		{"members", Members, realMembers, []string{"no members"}},
		{"files", Files, realFiles,
			[]string{"components/", "tokens/", "README.md", "1784534579692889", "64 B", "1 file, 2 directories, 64 B"}},
		{"write", Written, realWrite, []string{"wrote .dsx-selftest-fmt.css", "etag 1784746061595762"}},
		{"copy", Copied, realCopy, []string{"copied .dsx-selftest-fmt.css → .dsx-selftest-fmt2.css", "1784746062315787"}},
		{"delete", Deleted, realDelete, []string{"deleted 2 files"}},
		{"support-js", SupportJS, realSupportJS, []string{"wrote .dsx-selftest/support.js", "64.8 KB"}},
		{"plan", Plan, realPlan, []string{"plan_eyJhIjoxfQ", "scope    project", "1784262307"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := tc.render(tc.in)
			if !ok {
				t.Fatalf("refused its own measured reply:\n%s", tc.in)
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("rendered:\n%s\nmissing %q", got, w)
				}
			}
		})
	}
}

// Refusing is the load-bearing half. Every renderer must say no to a reply
// that is not its shape, or the fallback never runs and dsx prints a table of
// blanks for a reply it does not understand.
func TestEveryRendererRefusesWhatIsNotItsShape(t *testing.T) {
	t.Parallel()
	renderers := map[string]func(string) (string, bool){
		"DesignSystems": DesignSystems, "Project": Project, "Files": Files,
		"Written": Written, "Copied": Copied, "Deleted": Deleted,
		"SupportJS": SupportJS, "Plan": Plan, "Members": Members,
	}
	// Each of these is a reply some other tool really makes, plus the shapes an
	// error or a future rename would arrive as.
	foreign := []string{
		``, `null`, `"just a string"`, `{}`, `[{}]`, `{"error":"nope"}`,
		`not json at all`, `[{"id":""}]`, `{"id":""}`,
	}
	// `[]` is deliberately not in that list. An empty project has no files and
	// an empty listing is a true answer worth rendering; refusing it would make
	// dsx print "[]" at a person. `null` is a different matter — it is not this
	// shape at all, and it unmarshals into a nil slice without erroring, so
	// only a nil check separates the two.
	listShaped := map[string]bool{"DesignSystems": true, "Files": true, "Members": true}
	for name, render := range renderers {
		if _, ok := render(`[]`); ok != listShaped[name] {
			t.Errorf("%s on `[]`: ok = %v, want %v", name, ok, listShaped[name])
		}
		for _, in := range foreign {
			if _, ok := render(in); ok {
				t.Errorf("%s accepted %q", name, in)
			}
		}
	}
}

// The reply shapes are close enough to be mistaken for one another, and two of
// the mistakes would print a wrong number rather than fail: create_support_js
// has no "written", and write_files has no "bytes".
func TestNeighbouringShapesDoNotAcceptEachOther(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		render func(string) (string, bool)
		in     string
	}{
		{"Written on a support-js reply", Written, realSupportJS},
		{"SupportJS on a write reply", SupportJS, realWrite},
		{"Files on a design-systems reply", Files, realDesignSystems},
		{"DesignSystems on a files reply", DesignSystems, realFiles},
		{"Project on a design-systems reply", Project, realDesignSystems},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if out, ok := tc.render(tc.in); ok {
				t.Errorf("accepted a neighbour's reply and rendered:\n%s", out)
			}
		})
	}
}

// `{}` and `{"deleted":0}` are different replies and only the second is this
// shape. An int field cannot tell them apart, which is why the field is a
// pointer.
func TestDeletedZeroIsAnAnswerAndAnEmptyObjectIsNot(t *testing.T) {
	t.Parallel()
	if got, ok := Deleted(`{"deleted":0}`); !ok || got != "deleted 0 files" {
		t.Errorf("Deleted(0) = %q, %v; want %q, true", got, ok, "deleted 0 files")
	}
	if _, ok := Deleted(`{}`); ok {
		t.Error("an empty object was read as a delete that removed nothing")
	}
}

// Invariant 7: a name, a path and an etag all arrive from the server.
func TestServerTextIsSanitisedInEveryColumn(t *testing.T) {
	t.Parallel()
	// The escape has to be JSON's own: a raw control byte inside a string is
	// invalid JSON, so the decoder would refuse it and the test would pass
	// without ever reaching the sanitiser.
	hostile := `[{"id":"i","name":"safe\rEVIL"}]`
	got, ok := DesignSystems(hostile)
	if !ok {
		t.Fatal("refused a well-shaped reply")
	}
	if strings.Contains(got, "\r") {
		t.Errorf("a carriage return reached the terminal: %q", got)
	}
	files := `[{"path":"a\rb","type":"file","size":1,"etag":"e"}]`
	if got, _ := Files(files); strings.Contains(got, "\r") {
		t.Errorf("a carriage return reached the terminal from a path: %q", got)
	}
}

func TestOneAndManyReadAsSentences(t *testing.T) {
	t.Parallel()
	one := `[{"path":"a","type":"file","size":1,"etag":"e"}]`
	got, _ := Files(one)
	if !strings.HasSuffix(got, "1 file, 1 B") {
		t.Errorf("count line = %q, want it to end %q", got, "1 file, 1 B")
	}
	if got, _ := Deleted(`{"deleted":1}`); got != "deleted 1 file" {
		t.Errorf("Deleted(1) = %q", got)
	}
}
