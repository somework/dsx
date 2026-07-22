package syncer

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func tracked(etag, body string) FileState {
	return FileState{Etag: etag, Size: int64(len(body)), SHA: SHA256Hex([]byte(body))}
}

func onDisk(path, body string) localFile {
	return localFile{Path: path, Size: int64(len(body)), SHA: SHA256Hex([]byte(body))}
}

// TestClassifyStatusSeparatesWhatIsKnownFromWhatIsRemembered is the shape of
// the whole verb. The ledger-versus-disk half is computed here and now, so it
// is exact; the snapshot half is a memory of the last fetch and may be stale.
// Mixing them into one list would make the exact half look as doubtful as the
// remembered one.
func TestClassifyStatusSeparatesWhatIsKnownFromWhatIsRemembered(t *testing.T) {
	st := State{ProjectID: "p", Files: map[string]FileState{
		"edited.css":  tracked("e1", "old\n"),
		"removed.css": tracked("e2", "gone\n"),
		"moved.css":   tracked("e3", "same\n"),
		"intact.css":  tracked("e4", "steady\n"),
	}}
	local := map[string]localFile{
		"edited.css": onDisk("edited.css", "NEW BYTES\n"),
		"moved.css":  onDisk("moved.css", "same\n"),
		"intact.css": onDisk("intact.css", "steady\n"),
		"scratch.md": onDisk("scratch.md", "mine\n"),
	}
	snapshot := map[string]SnapshotEntry{
		"edited.css":  {Etag: "e1", Size: 4},
		"removed.css": {Etag: "e2", Size: 5},
		"moved.css":   {Etag: "e3-NEW", Size: 9},
		"intact.css":  {Etag: "e4", Size: 7},
		"theirs.css":  {Etag: "e9", Size: 3},
	}

	got := classifyStatus(snapshot, local, st, map[string]BaselineEntry{})

	eq := func(name string, got, want []string) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	eq("Modified", got.Modified, []string{"edited.css"})
	eq("Deleted", got.Deleted, []string{"removed.css"})
	eq("Untracked", got.Untracked, []string{"scratch.md"})
	eq("ServerAhead", got.ServerAhead, []string{"moved.css"})
	eq("RemoteOnly", got.RemoteOnly, []string{"theirs.css"})

	// intact.css agrees on both halves and belongs to no list at all: a status
	// that named every unchanged path would bury the four that matter.
	for name, got := range map[string][]string{
		"Modified": got.Modified, "Deleted": got.Deleted, "Untracked": got.Untracked,
		"ServerAhead": got.ServerAhead, "RemoteOnly": got.RemoteOnly,
		"GoneFromServer": got.GoneFromServer,
	} {
		if slices.Contains(got, "intact.css") {
			t.Errorf("%s names intact.css, which changed on neither side", name)
		}
	}
}

// TestAPathTheServerDroppedIsNotAServerEdit: a tracked path missing from the
// snapshot is the server having lost it, which reads nothing like an etag
// that moved. Folding it into ServerAhead would tell the user to pull bytes
// that are not there.
func TestAPathTheServerDroppedIsNotAServerEdit(t *testing.T) {
	st := State{ProjectID: "p", Files: map[string]FileState{"orphan.css": tracked("e1", "x\n")}}
	local := map[string]localFile{"orphan.css": onDisk("orphan.css", "x\n")}

	got := classifyStatus(map[string]SnapshotEntry{}, local, st, map[string]BaselineEntry{})

	if !slices.Equal(got.GoneFromServer, []string{"orphan.css"}) {
		t.Errorf("GoneFromServer = %v, want [orphan.css]", got.GoneFromServer)
	}
	if len(got.ServerAhead) != 0 {
		t.Errorf("ServerAhead = %v, want empty — the path is absent, not edited", got.ServerAhead)
	}
}

// TestAnUntrackedPathTheBaselineProvedIsNotReportedAsDiffering is the point of
// the exercise: an adopted directory whose bytes already equal the server's
// must stop reading as a wall of conflicts. The proof needs both halves —
// the baseline's sha against the disk, and its etag against the snapshot the
// same fetch recorded — or a path the server has since rewritten would still
// pass on a stale sha.
func TestAnUntrackedPathTheBaselineProvedIsNotReportedAsDiffering(t *testing.T) {
	body := "shared\n"
	st := State{ProjectID: "p", Files: map[string]FileState{}}
	local := map[string]localFile{
		"proven.css": onDisk("proven.css", body),
		"stale.css":  onDisk("stale.css", body),
		"other.css":  onDisk("other.css", body),
	}
	snapshot := map[string]SnapshotEntry{
		"proven.css": {Etag: "e1", Size: int64(len(body))},
		"stale.css":  {Etag: "e2-MOVED", Size: int64(len(body))},
		"other.css":  {Etag: "e3", Size: int64(len(body))},
	}
	verified := map[string]BaselineEntry{
		"proven.css": {Etag: "e1", Size: int64(len(body)), SHA: SHA256Hex([]byte(body))},
		"stale.css":  {Etag: "e2", Size: int64(len(body)), SHA: SHA256Hex([]byte(body))},
		"other.css":  {Etag: "e3", Size: int64(len(body)), SHA: SHA256Hex([]byte("DIFFERENT"))},
	}

	got := classifyStatus(snapshot, local, st, verified)

	if !slices.Equal(got.UntrackedSame, []string{"proven.css"}) {
		t.Errorf("UntrackedSame = %v, want [proven.css]", got.UntrackedSame)
	}
	// stale.css: the sha still matches what was proved, but against an etag
	// the snapshot no longer shows. other.css: the etag is current, the bytes
	// are not. Neither is proof, and neither may claim to be.
	if !slices.Equal(got.UntrackedDiffers, []string{"other.css", "stale.css"}) {
		t.Errorf("UntrackedDiffers = %v, want [other.css stale.css]", got.UntrackedDiffers)
	}
}

// TestClassifyStatusNeverReportsAnIrregularPathAsModified: invariant 4's
// "a path that is not a regular file is not proof of anything", read here.
// Its sha is empty, so a naive comparison against the ledger calls every
// socket and symlink a local edit.
func TestClassifyStatusNeverReportsAnIrregularPathAsModified(t *testing.T) {
	st := State{ProjectID: "p", Files: map[string]FileState{"odd": tracked("e1", "bytes\n")}}
	local := map[string]localFile{"odd": {Path: "odd", Irregular: true}}

	got := classifyStatus(map[string]SnapshotEntry{"odd": {Etag: "e1"}}, local, st, map[string]BaselineEntry{})

	if slices.Contains(got.Modified, "odd") {
		t.Error("Modified names an irregular path; its empty sha is not evidence of an edit")
	}
	if !slices.Equal(got.Irregular, []string{"odd"}) {
		t.Errorf("Irregular = %v, want [odd]", got.Irregular)
	}
}

// TestClassifyStatusSortsEverySlice: the report is read by people and diffed
// by scripts, and Go's map iteration order is deliberately random.
func TestClassifyStatusSortsEverySlice(t *testing.T) {
	st := State{ProjectID: "p", Files: map[string]FileState{}}
	local := map[string]localFile{
		"z.css": onDisk("z.css", "1"), "a.css": onDisk("a.css", "2"), "m.css": onDisk("m.css", "3"),
	}
	snapshot := map[string]SnapshotEntry{
		"zz.css": {Etag: "e"}, "aa.css": {Etag: "e"}, "mm.css": {Etag: "e"},
	}

	got := classifyStatus(snapshot, local, st, map[string]BaselineEntry{})

	if !slices.IsSorted(got.Untracked) {
		t.Errorf("Untracked is unsorted: %v", got.Untracked)
	}
	if !slices.IsSorted(got.RemoteOnly) {
		t.Errorf("RemoteOnly is unsorted: %v", got.RemoteOnly)
	}
}

// TestStatusRefusesWhenNoFetchEverRan: an offline status that answered half
// the question would be indistinguishable from one that answered all of it.
// A missing snapshot is not an empty server, so the report is refused rather
// than shaded, and the refusal names both routes out.
func TestStatusRefusesWhenNoFetchEverRan(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "a.css", "x\n")
	if err := (State{ProjectID: "p", Endpoint: "https://e/mcp", Files: map[string]FileState{}}).save(dir); err != nil {
		t.Fatal(err)
	}

	_, err := Status(StatusOpts{ProjectID: "p", Dir: dir})
	if err == nil {
		t.Fatal("Status succeeded with no snapshot on disk; a half answer must not pass as a whole one")
	}
	for _, want := range []string{"dsx fetch", "dsx pull -n"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name %q: %v", want, err)
		}
	}
}

// TestStatusRefusesWhenTheSnapshotBelongsToAnotherBinding: bound() discards a
// baseline whose project or endpoint disagrees, so a foreign snapshot leaves
// status with nothing to report — which is the no-fetch case, not a quieter
// one. Reporting against it would describe another project's tree.
func TestStatusRefusesWhenTheSnapshotBelongsToAnotherBinding(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "a.css", "x\n")
	if err := (State{ProjectID: "p", Endpoint: "https://e/mcp", Files: map[string]FileState{}}).save(dir); err != nil {
		t.Fatal(err)
	}
	foreign := Baseline{
		ProjectID: "OTHER", Endpoint: "https://e/mcp",
		Verified: map[string]BaselineEntry{},
		Listing:  map[string]SnapshotEntry{"theirs.css": {Etag: "e"}},
	}
	if err := foreign.save(dir); err != nil {
		t.Fatal(err)
	}

	if _, err := Status(StatusOpts{ProjectID: "p", Dir: dir}); err == nil {
		t.Fatal("Status reported against a snapshot recorded for another project")
	}
}

// TestStatusReadsBothSidesWithoutANetworkCall is the verb's whole reason to
// exist. Status takes no *mcp.Client at all, so the compiler carries most of
// this claim; what it cannot carry is that the two halves actually reach the
// report.
func TestStatusReadsBothSidesWithoutANetworkCall(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "edited.css", "NEW\n")
	mkfile(t, dir, "scratch.md", "mine\n")

	st := State{ProjectID: "p", Endpoint: "https://e/mcp", Files: map[string]FileState{
		"edited.css": tracked("e1", "old\n"),
	}}
	if err := st.save(dir); err != nil {
		t.Fatal(err)
	}
	bl := Baseline{
		ProjectID: "p", Endpoint: "https://e/mcp",
		Verified: map[string]BaselineEntry{},
		Listing: map[string]SnapshotEntry{
			"edited.css": {Etag: "e1-MOVED", Size: 4},
			"theirs.css": {Etag: "e9", Size: 3},
		},
	}
	if err := bl.save(dir); err != nil {
		t.Fatal(err)
	}

	rep, err := Status(StatusOpts{ProjectID: "p", Dir: dir})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !slices.Equal(rep.Modified, []string{"edited.css"}) {
		t.Errorf("Modified = %v, want [edited.css]", rep.Modified)
	}
	if !slices.Equal(rep.ServerAhead, []string{"edited.css"}) {
		t.Errorf("ServerAhead = %v, want [edited.css] — both sides moved, and both halves must say so", rep.ServerAhead)
	}
	if !slices.Equal(rep.Untracked, []string{"scratch.md"}) {
		t.Errorf("Untracked = %v, want [scratch.md]", rep.Untracked)
	}
	if !slices.Equal(rep.RemoteOnly, []string{"theirs.css"}) {
		t.Errorf("RemoteOnly = %v, want [theirs.css]", rep.RemoteOnly)
	}
}

// TestStatusWritesNothing: invariant 14 — status mutates nothing, so it may
// read the ledger from wherever it is standing. The moment it writes, that
// permission stops holding.
func TestStatusWritesNothing(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "a.css", "x\n")
	if err := (State{ProjectID: "p", Endpoint: "https://e/mcp", Files: map[string]FileState{}}).save(dir); err != nil {
		t.Fatal(err)
	}
	bl := Baseline{ProjectID: "p", Endpoint: "https://e/mcp",
		Verified: map[string]BaselineEntry{}, Listing: map[string]SnapshotEntry{}}
	if err := bl.save(dir); err != nil {
		t.Fatal(err)
	}

	before := snapshotTree(t, dir)
	if _, err := Status(StatusOpts{ProjectID: "p", Dir: dir}); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if after := snapshotTree(t, dir); after != before {
		t.Errorf("Status changed the tree.\nbefore: %s\nafter:  %s", before, after)
	}
}

func snapshotTree(t *testing.T, dir string) string {
	t.Helper()
	var sb strings.Builder
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			fmt.Fprintf(&sb, "d %s\n", rel)
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fmt.Fprintf(&sb, "f %s %s\n", rel, SHA256Hex(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sb.String()
}

// TestStatusRenderKeepsTheTwoHalvesApart: the exact half and the remembered
// half are different kinds of claim, and the labelling is the only thing that
// tells a reader which is which. A path that moved on both sides appears in
// both, once under each heading.
func TestStatusRenderKeepsTheTwoHalvesApart(t *testing.T) {
	r := StatusReport{
		Modified:    []string{"tokens/color.css"},
		Untracked:   []string{"scratch.md"},
		ServerAhead: []string{"tokens/color.css"},
		RemoteOnly:  []string{"icons/new.svg"},
	}
	out := r.Render(false)

	localAt := strings.Index(out, "modified locally:")
	remoteAt := strings.Index(out, "as of the last dsx fetch:")
	if localAt < 0 || remoteAt < 0 {
		t.Fatalf("both headings must appear:\n%s", out)
	}
	if localAt > remoteAt {
		t.Errorf("the remembered half is printed above the exact one:\n%s", out)
	}
	// The same path under both headings is the honest rendering of "both
	// sides moved"; collapsing it to one line would drop half the fact.
	if strings.Count(out, "tokens/color.css") != 2 {
		t.Errorf("tokens/color.css moved on both sides and must appear under both headings:\n%s", out)
	}
	for _, want := range []string{"server moved ahead", "remote-only", "dsx fetch to refresh"} {
		if !strings.Contains(out, want) {
			t.Errorf("Render is missing %q:\n%s", want, out)
		}
	}
}

// TestStatusRenderSaysCleanRatherThanNothing: an empty report printed as an
// empty string is indistinguishable from a command that failed silently.
func TestStatusRenderSaysCleanRatherThanNothing(t *testing.T) {
	out := StatusReport{}.Render(false)
	if strings.TrimSpace(out) == "" {
		t.Fatal("an unchanged tree rendered to nothing at all")
	}
	if !strings.Contains(out, "as of the last dsx fetch") {
		t.Errorf("the clean line drops the staleness caveat, which still applies:\n%s", out)
	}
}

// TestStatusRenderSanitisesServerText: invariant 7 — a remote path is
// untrusted, and a \r in one rewrites the line the terminal already showed.
func TestStatusRenderSanitisesServerText(t *testing.T) {
	r := StatusReport{RemoteOnly: []string{"ok.css\rHACKED"}}
	if out := r.Render(false); strings.ContainsRune(out, '\r') {
		t.Errorf("a carriage return from a server-supplied path reached the terminal: %q", out)
	}
}

// TestStatusRenderJSONCarriesBothHalves: --json is the agent's read, and an
// agent branching on the local half alone would act on half the picture.
func TestStatusRenderJSONCarriesBothHalves(t *testing.T) {
	r := StatusReport{Modified: []string{"a.css"}, ServerAhead: []string{"b.css"}}
	var got map[string]any
	if err := json.Unmarshal([]byte(r.Render(true)), &got); err != nil {
		t.Fatalf("--json is not valid JSON: %v", err)
	}
	for _, key := range []string{"modified", "deleted", "untracked", "server_ahead", "remote_only"} {
		if _, ok := got[key]; !ok {
			t.Errorf("--json omits %q; a key that vanishes when empty cannot be branched on", key)
		}
	}
}

// TestStatusRefusesABaselineWrittenBeforeSnapshotsExisted is the case the
// nil-versus-empty distinction was introduced for, and the one a refusal
// keyed on bound() alone silently passes: the binding agrees, the file is
// readable, and it simply predates the listing field. Treated as an empty
// listing it would report every tracked path as gone from the server and
// every local file as untracked — a confident answer assembled from nothing.
func TestStatusRefusesABaselineWrittenBeforeSnapshotsExisted(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "a.css", "x\n")
	st := State{ProjectID: "p", Endpoint: "https://e/mcp", Files: map[string]FileState{
		"a.css": tracked("e1", "x\n"),
	}}
	if err := st.save(dir); err != nil {
		t.Fatal(err)
	}
	// Hand-written, not Baseline{}.save: the point is bytes an older dsx
	// wrote, and a save through today's struct cannot produce them.
	legacy := `{"project_id":"p","endpoint":"https://e/mcp","verified":{}}` + "\n"
	if err := os.WriteFile(BaselinePath(dir), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	rep, err := Status(StatusOpts{ProjectID: "p", Dir: dir})
	if err == nil {
		t.Fatalf("Status answered from a baseline that holds no listing: %+v", rep)
	}
	if !strings.Contains(err.Error(), "dsx fetch") {
		t.Errorf("the refusal does not name the repair: %v", err)
	}
}

// TestStatusStillReadsASnapshotWhenTheLedgerNamesNoEndpoint: invariant 13's
// guards short-circuit on an empty endpoint, so a ledger written before it
// existed carries none. Status holds no client, so the ledger is its only
// source for this run's endpoint — comparing a recorded one against an empty
// one fails every time, and every such tree would be told to fetch forever.
func TestStatusStillReadsASnapshotWhenTheLedgerNamesNoEndpoint(t *testing.T) {
	dir := t.TempDir()
	mkfile(t, dir, "a.css", "x\n")
	if err := (State{ProjectID: "p", Files: map[string]FileState{}}).save(dir); err != nil {
		t.Fatal(err)
	}
	bl := Baseline{ProjectID: "p", Endpoint: "https://e/mcp",
		Verified: map[string]BaselineEntry{},
		Listing:  map[string]SnapshotEntry{"theirs.css": {Etag: "e9"}}}
	if err := bl.save(dir); err != nil {
		t.Fatal(err)
	}

	rep, err := Status(StatusOpts{ProjectID: "p", Dir: dir})
	if err != nil {
		t.Fatalf("Status refused a snapshot whose ledger simply names no endpoint: %v", err)
	}
	if !slices.Equal(rep.RemoteOnly, []string{"theirs.css"}) {
		t.Errorf("RemoteOnly = %v, want [theirs.css]", rep.RemoteOnly)
	}
	// The project half is still checked: only the endpoint axis is unasked.
	if _, err := Status(StatusOpts{ProjectID: "OTHER", Dir: dir}); err == nil {
		t.Error("Status accepted a snapshot recorded for another project")
	}
}
