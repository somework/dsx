package main

import "testing"

// localCovers has exactly one call site -- planPush's prune loop (plan.go:212)
// -- and until now no test of its own. `go tool cover` reported it at 100%,
// which was incidental coverage through planPush, not a test: mutation showed
// the suite catches only half its contract.
//
// It answers invariant 4's hardest question: does the scan account for this
// remote path? A symlinked *directory* is recorded once, at the link, because
// WalkDir does not descend it -- so nothing underneath was ever looked at, and
// reading that silence as "the user deleted every file under here" is how one
// symlink took out a whole server-side subtree.
//
// The two directions fail differently, which is why both need pinning:
//
//   - too strict (an irregular prefix stops covering) -> prune deletes a subtree
//     nobody touched. Data loss. The suite already catches this.
//   - too loose (any prefix covers) -> prune skips paths it should remove. Stale
//     files, no data loss -- and the suite is blind to it. Measured: mutating
//     `lf.Irregular` to `lf.Path != ""` leaves all 604 tests green.
//
// Phase 2 reshapes this file. An untested edge in it is not one to reshape past.
func TestLocalCovers(t *testing.T) {
	for _, tc := range []struct {
		name  string
		local map[string]localFile
		path  string
		want  bool
		why   string
	}{
		{
			name:  "exact member",
			local: map[string]localFile{"a.css": {Path: "a.css"}},
			path:  "a.css", want: true,
			why: "the scan saw it",
		},
		{
			name:  "absent",
			local: map[string]localFile{"other.css": {Path: "other.css"}},
			path:  "a.css", want: false,
			why: "the scan looked and it was not there -- prune's whole premise",
		},
		{
			name:  "empty scan",
			local: map[string]localFile{},
			path:  "a.css", want: false,
			why: "nothing on disk covers anything",
		},
		{
			name:  "irregular exact member",
			local: map[string]localFile{"link.css": {Path: "link.css", Irregular: true}},
			path:  "link.css", want: true,
			why: "an irregular path counts as present; it is the only copy left",
		},
		{
			name:  "under an irregular first segment",
			local: map[string]localFile{"vendor": {Path: "vendor", Irregular: true}},
			path:  "vendor/lib/a.css", want: true,
			why: "the walk never entered the symlinked directory, so its silence is not a deletion",
		},
		{
			name:  "under an irregular deeper segment",
			local: map[string]localFile{"a/b": {Path: "a/b", Irregular: true}},
			path:  "a/b/c/d.css", want: true,
			why: "the link can be at any depth",
		},
		{
			// The discriminating case, and the one the suite was blind to. A
			// REGULAR file at a prefix must not vouch for anything beneath it:
			// the walk did enter that far, so absence below really is absence.
			name:  "under a REGULAR prefix does not count",
			local: map[string]localFile{"vendor": {Path: "vendor"}},
			path:  "vendor/lib/a.css", want: false,
			why: "only an irregular prefix hides a subtree; a regular one hides nothing",
		},
		{
			name: "regular prefix beside an irregular one, path under the regular",
			local: map[string]localFile{
				"regular":   {Path: "regular"},
				"irregular": {Path: "irregular", Irregular: true},
			},
			path: "regular/a.css", want: false,
			why: "the irregular sibling must not vouch for the regular one's subtree",
		},
		{
			name:  "prefix that is not a path boundary does not count",
			local: map[string]localFile{"ven": {Path: "ven", Irregular: true}},
			path:  "vendor/a.css", want: false,
			why: "`ven` is a string prefix of `vendor` but not a parent directory of it",
		},
		{
			name:  "no separator, no prefix walk",
			local: map[string]localFile{"": {Path: "", Irregular: true}},
			path:  "a.css", want: false,
			why: "a top-level path has no parent segment to be covered by",
		},
		{
			name:  "irregular leaf does not cover its siblings",
			local: map[string]localFile{"a/link.css": {Path: "a/link.css", Irregular: true}},
			path:  "a/other.css", want: false,
			why: "a symlinked FILE hides nothing; only a symlinked directory does",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := localCovers(tc.local, tc.path); got != tc.want {
				t.Errorf("localCovers(%v, %q) = %v, want %v\n  %s", tc.local, tc.path, got, tc.want, tc.why)
			}
		})
	}
}
