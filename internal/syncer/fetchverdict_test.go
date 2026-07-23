package syncer

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// fetchBaselineVerdict judges what TestLiveFetchBaselineMatchesReadFull
// observes: BOTH paths must be fetched and recorded, and every recorded sha
// must survive an independent re-read. It carries no build tag for the reason
// etagVerdict does not — behind `live` an inverted comparison here would fail
// nothing.
//
// The binary half used to be the opposite claim — "fetched neither, recorded
// neither" — and it was true until `fetch` gained the preview lane, which it
// runs unconditionally (invariant 23). The live test then failed on its own
// plumbing before ever reaching this judgment, so the stale belief sat here
// unexercised: a verdict is only as current as the last time something ran it.
//
// reread maps every baseline path to the body an independent read returned —
// ReadFull for text, the preview lane for what ReadFull refuses. A path
// recorded but not re-read is an error rather than a pass, or a caller that
// re-read nothing would satisfy this vacuously.
func fetchBaselineVerdict(fetched []string, verified map[string]BaselineEntry, textPath, binPath string, reread map[string]string) error {
	if !containsPath(fetched, textPath) {
		return fmt.Errorf("Fetched = %v, want the text path %s present", fetched, textPath)
	}
	if !containsPath(fetched, binPath) {
		return fmt.Errorf("Fetched = %v, want the binary path %s present — fetch runs the preview lane unasked", fetched, binPath)
	}
	// Asked before the per-path checks below, so an empty baseline is reported
	// as the one thing it is rather than as whichever path is looked for first.
	if len(verified) == 0 {
		return fmt.Errorf("baseline holds no entries at all")
	}
	if _, ok := verified[binPath]; !ok {
		return fmt.Errorf("baseline holds no entry for the binary path %s, so the preview lane proved nothing", binPath)
	}
	for _, path := range sortedKeys(verified) {
		body, ok := reread[path]
		if !ok {
			return fmt.Errorf("baseline records %s but it was never re-read, so its sha is unchecked", path)
		}
		if want := SHA256Hex([]byte(body)); verified[path].SHA != want {
			return fmt.Errorf("baseline[%s].SHA = %s, want %s from an independent re-read", path, verified[path].SHA, want)
		}
	}
	return nil
}

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]BaselineEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestFetchBaselineVerdict(t *testing.T) {
	const (
		text = "a.css"
		bin  = "b.txt"
		body = "hello\n"
	)
	sha := SHA256Hex([]byte(body))

	const binBody = "\xff\xfe\x00\x01"
	binSHA := SHA256Hex([]byte(binBody))

	good := func() ([]string, map[string]BaselineEntry, map[string]string) {
		return []string{text, bin},
			map[string]BaselineEntry{text: {Etag: "e1", SHA: sha}, bin: {Etag: "e2", SHA: binSHA}},
			map[string]string{text: body, bin: binBody}
	}

	t.Run("the shape fetch is supposed to produce", func(t *testing.T) {
		f, v, r := good()
		if err := fetchBaselineVerdict(f, v, text, bin, r); err != nil {
			t.Fatalf("a correct fetch was rejected: %v", err)
		}
	})

	cases := []struct {
		name    string
		mutate  func(*[]string, map[string]BaselineEntry, map[string]string)
		wantMsg string
	}{
		{"the text path was not fetched", func(f *[]string, v map[string]BaselineEntry, r map[string]string) {
			*f = nil
		}, "want the text path"},
		{"the binary path was not fetched", func(f *[]string, v map[string]BaselineEntry, r map[string]string) {
			*f = []string{text}
		}, "want the binary path"},
		{"the binary path got no baseline entry", func(f *[]string, v map[string]BaselineEntry, r map[string]string) {
			delete(v, bin)
		}, "holds no entry for the binary path"},
		{"the baseline is empty", func(f *[]string, v map[string]BaselineEntry, r map[string]string) {
			delete(v, text)
			delete(v, bin)
		}, "no entries at all"},
		{"a recorded path was never re-read", func(f *[]string, v map[string]BaselineEntry, r map[string]string) {
			delete(r, text)
		}, "never re-read"},
		{"the recorded sha disagrees with the re-read", func(f *[]string, v map[string]BaselineEntry, r map[string]string) {
			v[text] = BaselineEntry{Etag: "e1", SHA: "not-the-sha"}
		}, "from an independent re-read"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, v, r := good()
			c.mutate(&f, v, r)
			err := fetchBaselineVerdict(f, v, text, bin, r)
			if err == nil {
				t.Fatalf("accepted a fetch that %s", c.name)
			}
			if !strings.Contains(err.Error(), c.wantMsg) {
				t.Errorf("error %q does not name %q", err, c.wantMsg)
			}
		})
	}
}
