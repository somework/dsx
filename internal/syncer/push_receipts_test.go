package syncer

import (
	"context"
	"os"
	"testing"
)

// pushFake serves one listing and acks every write with the etag given.
func pushFake(t *testing.T, listing []RemoteEntry, ack map[string]string) *fakeMCP {
	t.Helper()
	return newFakeMCP(t, func(name string, args map[string]any) fakeReply {
		switch name {
		case "list_files":
			return fakeReply{Text: listingFor(listing...)}
		case "write_files":
			files, _ := args["files"].([]any)
			etags := map[string]string{}
			for _, f := range files {
				m, _ := f.(map[string]any)
				p, _ := m["path"].(string)
				e, ok := ack[p]
				if !ok {
					return fakeReply{Text: "unexpected write " + p, IsError: true}
				}
				etags[p] = e
			}
			out := `{"etags":{`
			first := true
			for p, e := range etags {
				if !first {
					out += ","
				}
				out += `"` + p + `":"` + e + `"`
				first = false
			}
			return fakeReply{Text: out + `},"written":` + itoa(len(etags)) + `}`}
		case "delete_files":
			return fakeReply{Text: `{"deleted":1}`}
		}
		return fakeReply{Text: `{"plan_token":"tok"}`}
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestAPushRecordsItsReceiptsSoStatusDoesNotCryServerAhead is the defect a
// real run found: `dsx status` right after `dsx push` reported "server moved
// ahead" for every path the push had just written. `push` moves the LEDGER to
// the etag the server acked and left the snapshot behind, so `classifyStatus`
// compared one map that had updated against one that had not. The server did
// not move ahead of us — we are what moved it.
func TestAPushRecordsItsReceiptsSoStatusDoesNotCryServerAhead(t *testing.T) {
	dir := t.TempDir()
	body := "mine{}\n"
	mkfile(t, dir, "a.css", body)

	c := fakeClient(pushFake(t,
		[]RemoteEntry{fileEntry("a.css", "e-OLD", 4)},
		map[string]string{"a.css": "e-ACKED"}))

	st := State{ProjectID: "proj-A", Endpoint: c.Endpoint(), Files: map[string]FileState{
		"a.css": {Etag: "e-OLD", Size: 4, SHA: SHA256Hex([]byte("old\n"))},
	}}
	if err := st.save(dir); err != nil {
		t.Fatal(err)
	}
	prior := Baseline{
		ProjectID: "proj-A", Endpoint: c.Endpoint(),
		Verified: map[string]BaselineEntry{},
		Listing:  map[string]SnapshotEntry{"a.css": {Size: 4, Etag: "e-OLD"}},
	}
	if err := prior.save(dir); err != nil {
		t.Fatal(err)
	}

	rep, err := Push(context.Background(), c, PushOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 2})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if len(rep.Written) != 1 {
		t.Fatalf("the fixture no longer writes: %+v", rep)
	}

	got, err := Status(StatusOpts{ProjectID: "proj-A", Dir: dir})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(got.ServerAhead) != 0 {
		t.Errorf("status says the server moved ahead of %v — those are the bytes this push just sent",
			got.ServerAhead)
	}
	if len(got.Modified) != 0 || len(got.GoneFromServer) != 0 {
		t.Errorf("status = %+v, want clean after a push that landed everything", got)
	}
}

// TestAPushLeavesTheRestOfTheSnapshotAlone is the control that keeps the fix
// from becoming the thing invariant 20 forbids. A receipt is what the server
// acked for bytes WE sent; the listing a push happens to observe is not one.
// Recording the observation would tell the next `--force-with-lease` that we
// had looked at a path a third party moved, which is a blind `--force` under
// the safe flag's name.
func TestAPushLeavesTheRestOfTheSnapshotAlone(t *testing.T) {
	dir := t.TempDir()
	body := "mine{}\n"
	mkfile(t, dir, "a.css", body)

	// theirs.css sits on the server at an etag the snapshot has never seen —
	// exactly the third-party write a lease must still catch afterwards.
	c := fakeClient(pushFake(t,
		[]RemoteEntry{fileEntry("a.css", "e-OLD", 4), fileEntry("theirs.css", "e-THEIRS", 9)},
		map[string]string{"a.css": "e-ACKED"}))

	st := State{ProjectID: "proj-A", Endpoint: c.Endpoint(), Files: map[string]FileState{
		"a.css": {Etag: "e-OLD", Size: 4, SHA: SHA256Hex([]byte("old\n"))},
	}}
	if err := st.save(dir); err != nil {
		t.Fatal(err)
	}
	prior := Baseline{
		ProjectID: "proj-A", Endpoint: c.Endpoint(),
		Verified: map[string]BaselineEntry{},
		Listing: map[string]SnapshotEntry{
			"a.css":      {Size: 4, Etag: "e-OLD"},
			"theirs.css": {Size: 9, Etag: "e-MINE-ONCE"},
		},
	}
	if err := prior.save(dir); err != nil {
		t.Fatal(err)
	}

	if _, err := Push(context.Background(), c, PushOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 2}); err != nil {
		t.Fatalf("Push: %v", err)
	}

	bl, err := loadBaseline(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := bl.Listing["theirs.css"].Etag; got != "e-MINE-ONCE" {
		t.Errorf("snapshot[theirs.css].Etag = %q, want e-MINE-ONCE — this push observed the "+
			"server's newer etag but wrote nothing there, so it has not looked on our behalf", got)
	}
	if got := bl.Listing["a.css"].Etag; got != "e-ACKED" {
		t.Errorf("snapshot[a.css].Etag = %q, want the acked e-ACKED", got)
	}
}

// TestPushRecordsNoSnapshotWhereNoneExisted narrows the older blanket rule
// ("push writes no baseline") to the half that is actually load-bearing. With
// no snapshot on disk, the receipts have nothing to update, and writing them
// alone would forge a listing claiming the server holds only what this push
// sent: `status` would call every other server path gone, and a lease would
// read every missing path as free to take.
func TestPushRecordsNoSnapshotWhereNoneExisted(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "a.css", "mine{}\n")

	c := fakeClient(pushFake(t, nil, map[string]string{"a.css": "e-ACKED"}))

	rep, err := Push(context.Background(), c, PushOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 2})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if len(rep.Written) != 1 {
		t.Fatalf("the fixture no longer writes: %+v", rep)
	}
	if _, err := os.Stat(BaselinePath(dir)); !os.IsNotExist(err) {
		t.Fatalf("push created %s (stat err %v); a snapshot naming only what one push sent is "+
			"not a listing of the server", BaselinePath(dir), err)
	}
}

// TestAPrunedPathLeavesTheSnapshot: a delete is a receipt too. Left behind,
// the path reads as still on the server, so `status` would report it
// remote-only forever and a lease would check an etag for something gone.
func TestAPrunedPathLeavesTheSnapshot(t *testing.T) {
	dir := t.TempDir()

	c := fakeClient(pushFake(t, []RemoteEntry{fileEntry("gone.css", "e-GONE", 4)}, map[string]string{}))

	st := State{ProjectID: "proj-A", Endpoint: c.Endpoint(), Files: map[string]FileState{
		"gone.css": {Etag: "e-GONE", Size: 4, SHA: SHA256Hex([]byte("old\n"))},
	}}
	if err := st.save(dir); err != nil {
		t.Fatal(err)
	}
	prior := Baseline{
		ProjectID: "proj-A", Endpoint: c.Endpoint(),
		Verified: map[string]BaselineEntry{},
		Listing:  map[string]SnapshotEntry{"gone.css": {Size: 4, Etag: "e-GONE"}},
	}
	if err := prior.save(dir); err != nil {
		t.Fatal(err)
	}

	rep, err := Push(context.Background(), c, PushOpts{ProjectID: "proj-A", Dir: dir, Concurrency: 2, Prune: true})
	if err != nil {
		t.Fatalf("Push --prune: %v", err)
	}
	if len(rep.Deleted) != 1 {
		t.Fatalf("the fixture no longer deletes: %+v", rep)
	}

	bl, err := loadBaseline(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, still := bl.Listing["gone.css"]; still {
		t.Error("the snapshot still names a path this push deleted from the server")
	}
}
