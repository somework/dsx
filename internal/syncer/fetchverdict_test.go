package syncer

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// fetchBaselineVerdict judges what TestLiveFetchBaselineMatchesReadFull
// observes: a text path must be fetched and recorded, a binary one must be
// neither, and every recorded sha must survive an independent re-read. It
// carries no build tag for the reason etagVerdict does not — behind `live` an
// inverted comparison here would fail nothing.
//
// reread maps every baseline path to the body an independent ReadFull
// returned; a path recorded but not re-read is an error rather than a pass, or
// a caller that re-read nothing would satisfy this vacuously.
func fetchBaselineVerdict(fetched []string, verified map[string]BaselineEntry, textPath, binPath string, reread map[string]string) error {
	if !containsPath(fetched, textPath) {
		return fmt.Errorf("Fetched = %v, want the text path %s present", fetched, textPath)
	}
	if containsPath(fetched, binPath) {
		return fmt.Errorf("Fetched = %v, must not record the binary path %s", fetched, binPath)
	}
	if _, ok := verified[binPath]; ok {
		return fmt.Errorf("baseline holds an entry for the binary path %s: %+v", binPath, verified[binPath])
	}
	if len(verified) == 0 {
		return fmt.Errorf("baseline holds no entries at all")
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

	good := func() ([]string, map[string]BaselineEntry, map[string]string) {
		return []string{text},
			map[string]BaselineEntry{text: {Etag: "e1", SHA: sha}},
			map[string]string{text: body}
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
		{"the binary path was fetched", func(f *[]string, v map[string]BaselineEntry, r map[string]string) {
			*f = append(*f, bin)
		}, "must not record the binary path"},
		{"the binary path got a baseline entry", func(f *[]string, v map[string]BaselineEntry, r map[string]string) {
			v[bin] = BaselineEntry{Etag: "e2", SHA: "whatever"}
		}, "holds an entry for the binary path"},
		{"the baseline is empty", func(f *[]string, v map[string]BaselineEntry, r map[string]string) {
			delete(v, text)
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
