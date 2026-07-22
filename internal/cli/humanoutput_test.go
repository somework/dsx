package cli

import (
	"strings"
	"testing"
)

// Nine commands were given a renderer and nothing ran one end to end. Every
// existing test in those packages either passes --json (which bypasses the
// renderer entirely), calls the renderer directly, or asserts on the tool call
// rather than the output — so a renderer wired to the WRONG command was
// invisible: give `files rm` the write renderer and the delete still happens,
// the suite stays green, and a person is told "wrote" about a deletion.
//
// The fixtures are the bytes the real endpoint returned on 2026-07-23, the
// same ones internal/reply is table-tested against. What this adds on top is
// the wiring: which renderer each Command actually holds.
func TestEveryWiredCommandRendersItsOwnReply(t *testing.T) {
	t.Setenv("DSX_TOKEN", "test-token")
	diagPinCredentialStore(t)

	const project = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	for _, tc := range []struct {
		argv  []string
		reply string
		want  string
		// deny is what the output must NOT contain: the giveaway that another
		// command's renderer answered, or that the raw reply leaked through.
		deny []string
	}{
		{
			argv:  []string{"ds", "ls"},
			reply: `[{"id":"dddddddd-dddd-4ddd-8ddd-dddddddddddd","name":"Placeholder","is_default":true}]`,
			want:  "dddddddd-dddd-4ddd-8ddd-dddddddddddd  Placeholder  (default)\n1 design system",
			deny:  []string{`"is_default"`},
		},
		{
			argv:  []string{"project", "ls"},
			reply: `[{"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","name":"dsx-sandbox","url":"https://x"}]`,
			want:  "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa  dsx-sandbox\n1 project",
			deny:  []string{"design system", `"url"`},
		},
		{
			argv:  []string{"project", "get", project},
			reply: `{"id":"` + project + `","name":"dsx-sandbox","type":"PROJECT_TYPE_PROJECT","url":"https://x","sharing":{"scope":"invited","link_permission":"view","view_mode":"private"}}`,
			want:  "dsx-sandbox\n  id       " + project + "\n  sharing  invited (link: view)",
			deny:  []string{"1 project", `"view_mode"`},
		},
		{
			argv:  []string{"member", "ls", project},
			reply: `[]`,
			want:  "no members",
			deny:  []string{"[]", "0 files"},
		},
		{
			argv:  []string{"files", "ls", project},
			reply: `[{"path":"components","type":"directory"},{"path":"README.md","type":"file","size":64,"etag":"e1"}]`,
			want:  "components/\n64 B                     e1  README.md\n1 file, 1 directory, 64 B",
			deny:  []string{`"type"`, "wrote", "deleted"},
		},
		{
			argv:  []string{"files", "put", project, "a.css"},
			reply: `{"etags":{"a.css":"e2"},"written":1,"url":"https://x"}`,
			want:  "wrote a.css  etag e2",
			deny:  []string{"deleted", "copied", `"written"`},
		},
		{
			argv:  []string{"files", "rm", project, "a.css"},
			reply: `{"deleted":1}`,
			want:  "deleted 1 file",
			deny:  []string{"wrote", "copied"},
		},
		{
			argv:  []string{"files", "cp", project, "a.css", "b.css"},
			reply: `{"etags":{"b.css":"e3"},"results":[{"src":"a.css","dest":"b.css","copied":1}]}`,
			want:  "copied a.css → b.css  etag e3",
			deny:  []string{"wrote", "deleted"},
		},
		{
			argv:  []string{"project", "support-js", project},
			reply: `{"path":"support.js","bytes":66404,"etags":{"support.js":"e4"}}`,
			want:  "wrote support.js  64.8 KB  etag e4",
			deny:  []string{"deleted", `"bytes"`},
		},
		{
			argv:  []string{"plan", "new", project, "--scope", "project"},
			reply: `{"plan_token":"plan_abc","project_id":"` + project + `","scope":"project","expires_at":1784262307}`,
			want:  "plan_abc\n  scope    project\n  expires  1784262307",
			deny:  []string{"wrote", `"plan_token"`},
		},
	} {
		t.Run(strings.Join(tc.argv, " "), func(t *testing.T) {
			// finalize_plan is minted on the way to a delete, so a fixture
			// server has to answer it too; every other tool gets the case's
			// own reply.
			f := newFakeMCP(t, func(name string, _ map[string]any) fakeReply {
				if name == "finalize_plan" && tc.argv[1] != "new" {
					return fakeReply{Text: `{"plan_token":"t","scope":"paths"}`}
				}
				return fakeReply{Text: tc.reply}
			})
			t.Setenv("DSX_ENDPOINT", f.URL())

			out, err := maincliRun(t, append(tc.argv, "--", "")[:len(tc.argv)]...)
			if err != nil {
				t.Fatalf("dsx %s: %v", strings.Join(tc.argv, " "), err)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("stdout:\n%s\nwant it to contain:\n%s", out, tc.want)
			}
			for _, d := range tc.deny {
				if strings.Contains(out, d) {
					t.Errorf("stdout contains %q — another renderer answered, or the raw reply leaked:\n%s", d, out)
				}
			}
		})
	}
}

// The other half of the wiring: --json must reach none of them. A renderer on
// the machine path would break the one thing README promises about a relayed
// tool result.
func TestNoRendererReachesTheJSONPath(t *testing.T) {
	t.Setenv("DSX_TOKEN", "test-token")
	diagPinCredentialStore(t)

	const raw = `[{"id":"dddddddd-dddd-4ddd-8ddd-dddddddddddd","name":"Placeholder","is_default":true}]`
	f := newFakeMCP(t, func(string, map[string]any) fakeReply { return fakeReply{Text: raw} })
	t.Setenv("DSX_ENDPOINT", f.URL())

	out, err := maincliRun(t, "ds", "ls", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != raw {
		t.Errorf("--json = %q, want the server's own bytes %q", out, raw)
	}
}

// Every Human in the registry must be one of internal/reply's, reached through
// a Command. This is the structural half: a renderer declared nowhere in
// `groups` renders nothing, and `groups` is the only place a command is
// declared (invariant 11).
func TestEveryDeclaredHumanIsExercised(t *testing.T) {
	t.Parallel()
	wired := map[string]bool{}
	for _, g := range groups {
		for _, c := range g.Cmds {
			if c.Human != nil {
				wired[c.Name] = true
			}
		}
	}
	// The Run-based commands hold their renderer inside the function rather
	// than in the Command literal, so they are named here by hand and the
	// table above is what actually drives them.
	for _, name := range []string{
		"project ls", "files ls", "files put", "files rm", "files cp",
		"project support-js", "plan new",
	} {
		wired[name] = true
	}
	for _, name := range []string{"ds ls", "project get", "member ls"} {
		if !wired[name] {
			t.Errorf("%s lost its renderer", name)
		}
	}
	if len(wired) != 10 {
		t.Errorf("%d commands render for a person, expected 10 — add the new one to "+
			"TestEveryWiredCommandRendersItsOwnReply before widening this: %v", len(wired), wired)
	}
}
