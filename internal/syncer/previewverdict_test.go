package syncer

import (
	"fmt"
	"strings"
	"testing"
)

// previewVerdict judges what the preview lane returned for one path against
// what list_files says about it and against the bytes that were uploaded.
//
// It lives outside //go:build live on purpose: CI never compiles live_test.go,
// so a mutation inside it fails nothing, and the judgment is the half worth
// pinning by a bare `go test`. What stays under the tag is the plumbing —
// whether the right endpoint was called with the right arguments — which is
// irreducible without the network.
func previewVerdict(path string, sent, served []byte, listedSize int64, listedEtag string) error {
	switch {
	case listedEtag == "":
		return fmt.Errorf("%s: list_files reports no etag, so this run cannot tell a "+
			"steady path from one rewritten under it — the rest proves nothing", path)
	case listedSize != int64(len(sent)):
		return fmt.Errorf("%s: positive control failed — list_files reports %d bytes for content "+
			"that is %d long, so the upload under test did not land",
			path, listedSize, len(sent))
	case len(served) == 0:
		return fmt.Errorf("%s: the preview lane served nothing", path)
	case int64(len(served)) != listedSize:
		return fmt.Errorf("%s: the preview lane served %d bytes, list_files reports %d — "+
			"invariant 1 refuses this, and an .html is the shape that produces it "+
			"(the server prepends a preview harness)", path, len(served), listedSize)
	case string(served) != string(sent):
		return fmt.Errorf("%s: the preview lane served %d bytes of the right length but not the "+
			"right content — a binary round trip is not byte-exact", path, len(served))
	}
	return nil
}

func TestPreviewVerdict(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0xff, 0xfe, 0x00}
	other := []byte{0x89, 'P', 'N', 'G', 0x00, 0x01, 0x02}

	cases := []struct {
		name       string
		sent       []byte
		served     []byte
		size       int64
		etag       string
		wantErr    bool
		wantSubstr string
	}{
		{name: "byte-exact round trip", sent: png, served: png, size: int64(len(png)), etag: "e1"},
		{name: "no etag", sent: png, served: png, size: int64(len(png)), etag: "",
			wantErr: true, wantSubstr: "no etag"},
		{name: "the upload never landed", sent: png, served: png, size: 99, etag: "e1",
			wantErr: true, wantSubstr: "positive control failed"},
		{name: "nothing served", sent: png, served: nil, size: int64(len(png)), etag: "e1",
			wantErr: true, wantSubstr: "served nothing"},
		{name: "served longer than the listing", sent: png, served: append(png, 'x'), size: int64(len(png)), etag: "e1",
			wantErr: true, wantSubstr: "invariant 1"},
		{name: "served shorter than the listing", sent: png, served: png[:3], size: int64(len(png)), etag: "e1",
			wantErr: true, wantSubstr: "invariant 1"},
		{name: "right length, wrong bytes", sent: png, served: other, size: int64(len(png)), etag: "e1",
			wantErr: true, wantSubstr: "not byte-exact"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := previewVerdict("a.png", tc.sent, tc.served, tc.size, tc.etag)
			if tc.wantErr && err == nil {
				t.Fatalf("want an error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("want no error, got %v", err)
			}
			if err != nil && !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("error %q does not mention %q", err, tc.wantSubstr)
			}
		})
	}
}

// previewScopeVerdict judges the measured claim that the token in a serve_url
// is scoped to the PROJECT and not to the path: one issued for path A serves
// path B. It is a security fact about the transport, so it is recorded as one
// rather than assumed either way.
func previewScopeVerdict(bytesForOtherPath []byte, statusForOtherPath int) error {
	switch {
	case statusForOtherPath == 200 && len(bytesForOtherPath) > 0:
		return nil // measured: project-scoped
	case statusForOtherPath == 403 || statusForOtherPath == 401:
		return fmt.Errorf("the preview token is path-scoped after all (http %d for a second path) — "+
			"PROTOCOL.md and dsx's threat model both say project-scoped and would need correcting",
			statusForOtherPath)
	}
	return fmt.Errorf("a second path answered http %d with %d bytes: neither the measured "+
		"project-scoped behaviour nor a recognisable refusal", statusForOtherPath, len(bytesForOtherPath))
}

func TestPreviewScopeVerdict(t *testing.T) {
	for _, tc := range []struct {
		name       string
		body       []byte
		status     int
		wantErr    bool
		wantSubstr string
	}{
		{name: "project-scoped, as measured", body: []byte("x"), status: 200},
		{name: "path-scoped would be news", status: 403, wantErr: true, wantSubstr: "path-scoped"},
		{name: "unauthorized is the same news", status: 401, wantErr: true, wantSubstr: "path-scoped"},
		{name: "200 with an empty body is neither", status: 200, wantErr: true, wantSubstr: "neither"},
		{name: "404 is neither", status: 404, wantErr: true, wantSubstr: "neither"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := previewScopeVerdict(tc.body, tc.status)
			if tc.wantErr != (err != nil) {
				t.Fatalf("wantErr=%v, got %v", tc.wantErr, err)
			}
			if err != nil && !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("error %q does not mention %q", err, tc.wantSubstr)
			}
		})
	}
}
