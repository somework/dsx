package syncer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeJoinRefusesEscapes(t *testing.T) {
	root := t.TempDir()

	escapes := []string{
		"../outside.txt",
		"../../etc/passwd",
		"a/../../outside.txt",
		"/etc/passwd",
		"",
		"..",
	}
	for _, rel := range escapes {
		t.Run(rel, func(t *testing.T) {
			if _, err := safeJoin(root, rel); err == nil {
				t.Errorf("safeJoin(%q) = nil error, want refusal", rel)
			}
		})
	}
}

func TestSafeJoinAcceptsProjectPaths(t *testing.T) {
	root := t.TempDir()

	for _, rel := range []string{"a.css", "tokens/colors.css", "a/b/c/d.txt", "./a.css"} {
		got, err := safeJoin(root, rel)
		if err != nil {
			t.Fatalf("safeJoin(%q) failed: %v", rel, err)
		}
		if !strings.HasPrefix(got, root) {
			t.Errorf("safeJoin(%q) = %q, want it under %q", rel, got, root)
		}
	}
}

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()

	st := State{
		ProjectID: "proj-1",
		Endpoint:  "https://example.test/mcp",
		Files: map[string]FileState{
			"a.css":  {Etag: "1", Size: 10, SHA: "abc"},
			"og.png": {Etag: "2", Binary: true},
		},
	}
	if err := st.save(dir); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got.ProjectID != "proj-1" || len(got.Files) != 2 {
		t.Fatalf("state = %+v", got)
	}
	if !got.Files["og.png"].Binary {
		t.Error("binary flag lost across save/load")
	}
	if got.Files["a.css"].SHA != "abc" {
		t.Error("sha lost across save/load")
	}
}

func TestLoadStateMissingIsEmptyNotError(t *testing.T) {
	st, err := LoadState(t.TempDir())
	if err != nil {
		t.Fatalf("LoadState on a fresh dir: %v", err)
	}
	if len(st.Files) != 0 {
		t.Errorf("files = %v, want empty", st.Files)
	}
}

func TestLoadStateCorruptIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(StatePath(dir), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(dir); err == nil {
		t.Error("LoadState on corrupt ledger = nil error, want failure")
	}
}

func TestScanLocalSkipsLedgerAndVCS(t *testing.T) {
	dir := t.TempDir()

	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.css", "body{}")
	write("tokens/colors.css", ":root{}")
	write(legacyStateFileName, "{}")
	write(".git/config", "[core]")
	write("node_modules/pkg/index.js", "x")

	ig, err := loadIgnore(dir)
	if err != nil {
		t.Fatalf("loadIgnore: %v", err)
	}
	got, err := scanLocal(dir, ig)
	if err != nil {
		t.Fatalf("scanLocal: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("scanned %d files (%v), want a.css and tokens/colors.css", len(got), SortedPaths(got))
	}
	if _, ok := got["tokens/colors.css"]; !ok {
		t.Error("nested path missing or not slash-separated")
	}
	if got["a.css"].SHA != SHA256Hex([]byte("body{}")) {
		t.Error("sha mismatch")
	}
}

func TestBatches(t *testing.T) {
	t.Run("splits on count", func(t *testing.T) {
		specs := make([]writeSpec, maxBatchFiles+5)
		for i := range specs {
			specs[i] = writeSpec{Path: "f", Data: "x"}
		}
		got := batches(specs)
		if len(got) != 2 || len(got[0]) != maxBatchFiles || len(got[1]) != 5 {
			t.Errorf("batch sizes = %d (%d, ...), want 2 batches of %d and 5",
				len(got), len(got[0]), maxBatchFiles)
		}
	})

	t.Run("splits on bytes", func(t *testing.T) {
		big := strings.Repeat("a", maxBatchBytes/2+1)
		got := batches([]writeSpec{
			{Path: "1", Data: big},
			{Path: "2", Data: big},
		})
		if len(got) != 2 {
			t.Errorf("batches = %d, want 2 — two half-cap files exceed the cap together", len(got))
		}
	})

	t.Run("an oversized single file still ships alone", func(t *testing.T) {
		got := batches([]writeSpec{{Path: "1", Data: strings.Repeat("a", maxBatchBytes*2)}})
		if len(got) != 1 || len(got[0]) != 1 {
			t.Errorf("batches = %v, want the file in one batch rather than dropped", got)
		}
	})

	t.Run("empty input", func(t *testing.T) {
		if got := batches(nil); len(got) != 0 {
			t.Errorf("batches(nil) = %v, want none", got)
		}
	})
}
