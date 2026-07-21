package syncer

import (
	"fmt"
	"strings"
	"testing"
)

// etagVerdict judges the three writes TestLiveEtagIsRevisionDerivedNotContent-
// Derived makes. It carries no build tag on purpose: behind `live` the
// judgment is invisible to CI, and inverting a comparison here would fail
// nothing. Split out, the decision is ordinary code with an ordinary test and
// the live half keeps only the plumbing.
//
// first and again are writes of the same bytes; middle is different content
// between them, so its etag must differ from first's or nothing downstream
// means anything.
func etagVerdict(first, middle, again string) error {
	switch {
	case first == "" || middle == "" || again == "":
		return fmt.Errorf("write_files returned an empty etag: first=%q middle=%q again=%q", first, middle, again)
	case middle == first:
		return fmt.Errorf("positive control failed: writing different content kept the etag (%s == %s); "+
			"either the write did not land or the server hands out a constant etag, and either way "+
			"the rest of this test proves nothing", first, middle)
	case again == first:
		return fmt.Errorf("etag is content-derived, contradicting the measured behaviour: re-putting the "+
			"original bytes after an intervening different write (%s) produced the same etag again (%s)", middle, first)
	}
	return nil
}

func TestEtagVerdict(t *testing.T) {
	cases := []struct {
		name                 string
		first, middle, again string
		wantErr              bool
		wantMsgContains      string
	}{
		{name: "revision-derived: all three differ", first: "1", middle: "2", again: "3"},
		{name: "empty first", first: "", middle: "2", again: "3", wantErr: true, wantMsgContains: "empty etag"},
		{name: "empty middle", first: "1", middle: "", again: "3", wantErr: true, wantMsgContains: "empty etag"},
		{name: "empty again", first: "1", middle: "2", again: "", wantErr: true, wantMsgContains: "empty etag"},
		{name: "constant etag trips the positive control", first: "1", middle: "1", again: "1", wantErr: true, wantMsgContains: "positive control failed"},
		{name: "different content kept the etag", first: "1", middle: "1", again: "3", wantErr: true, wantMsgContains: "positive control failed"},
		{name: "content-derived: the re-put returned the original etag", first: "1", middle: "2", again: "1", wantErr: true, wantMsgContains: "content-derived"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := etagVerdict(c.first, c.middle, c.again)
			if c.wantErr && err == nil {
				t.Fatalf("etagVerdict(%q, %q, %q) = nil, want an error", c.first, c.middle, c.again)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("etagVerdict(%q, %q, %q) = %v, want nil", c.first, c.middle, c.again, err)
			}
			if err != nil && !strings.Contains(err.Error(), c.wantMsgContains) {
				t.Errorf("error %q does not name %q", err, c.wantMsgContains)
			}
		})
	}
}
