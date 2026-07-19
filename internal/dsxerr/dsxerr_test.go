package dsxerr

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExitCodesAreDistinctPerKind(t *testing.T) {
	seen := map[int]Kind{}
	for _, k := range []Kind{KindUsage, KindConflict, KindTransport, KindAuth} {
		code := k.ExitCode()
		if code == ExitOK {
			t.Errorf("kind %q exits 0; failure would read as success", k)
		}
		if other, dup := seen[code]; dup {
			t.Errorf("kinds %q and %q share exit code %d", other, k, code)
		}
		seen[code] = k
	}
}

func TestClassifyFindsKindThroughWrapping(t *testing.T) {
	base := &Error{Kind: KindAuth, Msg: "token expired"}
	wrapped := fmt.Errorf("read_file: %w", fmt.Errorf("rpc: %w", base))

	got := Classify(wrapped)
	if got.Kind != KindAuth {
		t.Fatalf("kind through two wraps = %q, want %q", got.Kind, KindAuth)
	}
	if ExitCodeFor(wrapped) != ExitAuth {
		t.Fatalf("exit code = %d, want %d", ExitCodeFor(wrapped), ExitAuth)
	}
}

func TestClassifyUnknownErrorIsGenericFailureNotSuccess(t *testing.T) {
	got := Classify(errors.New("something we never labelled"))
	if got.Kind == "" {
		t.Error("unclassified error has no machine token")
	}
	if code := ExitCodeFor(errors.New("x")); code != ExitFailure {
		t.Fatalf("unclassified exit = %d, want %d", code, ExitFailure)
	}
}

func TestClassifyNilIsNil(t *testing.T) {
	if Classify(nil) != nil {
		t.Fatal("Classify(nil) must be nil")
	}
	if ExitCodeFor(nil) != ExitOK {
		t.Fatal("nil error must exit 0")
	}
}

func TestRenderErrorJSONIsMachineReadableWithPaths(t *testing.T) {
	err := Conflict([]string{"b.css", "a.css"}, "local differs")

	line := Render(err, true)
	var got struct {
		Error   string   `json:"error"`
		Message string   `json:"message"`
		Paths   []string `json:"paths"`
	}
	if jsonErr := json.Unmarshal([]byte(line), &got); jsonErr != nil {
		t.Fatalf("--json error is not JSON: %v\n%s", jsonErr, line)
	}
	if got.Error != string(KindConflict) {
		t.Errorf("error token = %q, want %q", got.Error, KindConflict)
	}
	if len(got.Paths) != 2 || got.Paths[0] != "a.css" {
		t.Errorf("paths = %v, want them present and sorted", got.Paths)
	}
	if strings.Contains(line, "\n") {
		t.Errorf("JSON error must be one line: %q", line)
	}
}

func TestRenderErrorProseCarriesTheSamePaths(t *testing.T) {
	err := Conflict([]string{"a.css"}, "local differs")
	line := Render(err, false)
	if !strings.Contains(line, "a.css") {
		t.Errorf("prose error dropped the path: %q", line)
	}
	if strings.HasPrefix(strings.TrimSpace(line), "{") {
		t.Errorf("prose mode emitted JSON: %q", line)
	}
}

func TestRenderErrorJSONNeverEmitsBarePathsKeyWhenThereAreNone(t *testing.T) {
	line := Render(&Error{Kind: KindTransport, Msg: "http 503"}, true)
	if strings.Contains(line, "paths") {
		t.Errorf("empty paths must be omitted, not emitted as null: %q", line)
	}
}

func TestJSONRequestedScansArgsBeforeFlagParsing(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"pull", "p", "d", "--json"}, true},
		{[]string{"pull", "--json", "p", "d"}, true},
		{[]string{"pull", "-json", "p"}, true},
		{[]string{"pull", "p", "d"}, false},
		{[]string{"put", "p", "--json=true"}, true},
		{[]string{"put", "p", "notes-about--json.txt"}, false},

		{[]string{"pull", "p", "d", "--json=false"}, false},
		{[]string{"pull", "p", "d", "-json=0"}, false},
	}
	for _, tc := range cases {
		if got := JSONRequested(tc.args); got != tc.want {
			t.Errorf("JSONRequested(%v) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

func TestDsxErrorUnwrapsToItsCause(t *testing.T) {
	cause := errors.New("dial tcp: refused")
	err := &Error{Kind: KindTransport, Msg: "unreachable", Err: cause}
	if !errors.Is(err, cause) {
		t.Fatal("Error must unwrap to its cause")
	}
	if !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("message lost: %q", err.Error())
	}
}

func TestClassifyLabelsLocalFilesystemErrors(t *testing.T) {
	dir := t.TempDir()

	_, pathErr := os.ReadFile(filepath.Join(dir, "absent", "file.css"))
	if pathErr == nil {
		t.Fatal("reading a nonexistent path succeeded")
	}
	linkErr := os.Rename(filepath.Join(dir, "absent", "a"), filepath.Join(dir, "absent", "b"))
	if linkErr == nil {
		t.Fatal("renaming a nonexistent path succeeded")
	}

	cases := []struct {
		name string
		err  error
		want Kind
	}{
		{"path error", pathErr, KindLocal},
		{"path error wrapped twice", fmt.Errorf("save: %w", fmt.Errorf("read: %w", pathErr)), KindLocal},
		{"link error", linkErr, KindLocal},
		{"link error wrapped twice", fmt.Errorf("save: %w", fmt.Errorf("rename: %w", linkErr)), KindLocal},
		{"labelled auth wins", &Error{Kind: KindAuth, Err: pathErr}, KindAuth},
		{"labelled transport wins", &Error{Kind: KindTransport, Err: linkErr}, KindTransport},
		{"plain error stays generic", errors.New("boom"), KindFailure},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.err).Kind; got != c.want {
				t.Errorf("Classify(%v).Kind = %q, want %q", c.err, got, c.want)
			}
			if got := ExitCodeFor(c.err); got != c.want.ExitCode() {
				t.Errorf("ExitCodeFor = %d, want %d", got, c.want.ExitCode())
			}
		})
	}

	if got := ExitCodeFor(pathErr); got != ExitFailure {
		t.Errorf("a local error changed the exit contract: got %d, want %d", got, ExitFailure)
	}
}
