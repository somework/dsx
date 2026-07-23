package syncer

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// reference/mcp-tools.json is described as the server's tools/list "verbatim",
// and it is not: the wire reply is compact, 30 KB on one line, and the file is
// indented with sorted keys. That is the right call — a one-line 30 KB blob
// makes every schema change an unreadable diff — but it means "verbatim" cannot
// be checked by comparing bytes, and a regeneration done with a different
// serialiser produces a whole-file diff that hides the one real change inside
// it. That is how the file went three tools stale without anyone seeing it.
//
// So the form is pinned instead: the file must equal its own canonical
// re-encoding. json.Encoder with SetEscapeHTML(false) and a two-space indent
// reproduces it byte for byte — measured — which makes regeneration
// deterministic and a hand-edit visible.
func TestTheRecordedToolsListIsInCanonicalForm(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "reference", "mcp-tools.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the recorded tools/list does not parse: %v", err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// The server's descriptions carry < and > and non-ASCII; escaping either
	// would make the file diverge from what a person reads on the wire.
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, buf.Bytes()) {
		t.Errorf("reference/mcp-tools.json is not in canonical form (%d bytes on disk, %d canonical). "+
			"Regenerate it so the next real change shows as a small diff:\n"+
			"  dsx tools --schema --json | <canonicalise>", len(raw), buf.Len())
	}
}
