package syncer

import (
	"context"
	"encoding/base64"
	"slices"
	"testing"
)

// A forced `push` of a binary-marked path writes Binary:true BESIDE a real SHA
// of the bytes on disk. That shape reaches the prune loop's sha check with the
// two SHAs EQUAL, so the sha check does not divert it.
//
// The ledger shape is produced by writeBatch rather than hand-rolled, so this
// also fails if the Binary carry in push.go is dropped.
func TestForcedPushOfABinaryEntryDoesNotArmAPlainPullPrune(t *testing.T) {
	body := []byte("\x89PNG\r\n\x1a\nuser-authored bytes")

	f := newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "finalize_plan":
			return fakeReply{Text: `{"plan_token":"tok"}`}
		case "write_files":
			return fakeReply{Text: `{"etags":{"og.png":"e2"},"written":1}`}
		}
		return fakeReply{Text: "[]"}
	})

	// The ledger as `pull` leaves it after read_file refuses the path.
	st := State{ProjectID: "p", Files: map[string]FileState{
		"og.png": {Etag: "e1", Binary: true},
	}}
	var rep PushReport
	batch := []writeSpec{{
		Path:     "og.png",
		Data:     base64.StdEncoding.EncodeToString(body),
		Encoding: "base64",
	}}
	if err := writeBatch(context.Background(), fakeClient(f), "p1", batch, &st, &rep); err != nil {
		t.Fatalf("forced push of a binary-marked path failed: %v", err)
	}

	after := st.Files["og.png"]
	if !after.Binary {
		t.Fatalf("push cleared the Binary marker: %+v", after)
	}
	if after.SHA == "" {
		t.Fatalf("push recorded no SHA: %+v — this test no longer exercises the sha-matching case it exists for", after)
	}

	// Next run: the server has dropped the path, disk still holds exactly what we
	// pushed. No --force anywhere.
	local := map[string]localFile{"og.png": {Path: "og.png", SHA: SHA256Hex(body), Size: int64(len(body))}}
	d := planPull(map[string]RemoteEntry{}, local, st, nil, false, true)

	if slices.Contains(d.Delete, "og.png") {
		t.Errorf("a PLAIN `pull --prune` schedules Delete for %q after a forced push: local sha %q equals ledger sha %q", "og.png", local["og.png"].SHA, after.SHA)
	}
	// Kept, and reported rather than silent.
	if !slices.Contains(d.PruneBinary, "og.png") {
		t.Errorf("%q was kept but not surfaced", "og.png")
	}
}
