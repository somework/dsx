package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestExitCodesAreDistinctPerKind(t *testing.T) {
	// An agent branches on these numbers. Two kinds sharing a code would make
	// "retry" and "fetch a human" indistinguishable.
	seen := map[int]errKind{}
	for _, k := range []errKind{kindUsage, kindConflict, kindTransport, kindAuth} {
		code := k.exitCode()
		if code == exitOK {
			t.Errorf("kind %q exits 0; failure would read as success", k)
		}
		if other, dup := seen[code]; dup {
			t.Errorf("kinds %q and %q share exit code %d", other, k, code)
		}
		seen[code] = k
	}
}

func TestClassifyFindsKindThroughWrapping(t *testing.T) {
	// Every error site wraps with %w on the way up. A classification that only
	// works on a bare error would silently degrade to the generic code.
	base := &dsxError{Kind: kindAuth, Msg: "token expired"}
	wrapped := fmt.Errorf("read_file: %w", fmt.Errorf("rpc: %w", base))

	got := classify(wrapped)
	if got.Kind != kindAuth {
		t.Fatalf("kind through two wraps = %q, want %q", got.Kind, kindAuth)
	}
	if exitCodeFor(wrapped) != exitAuth {
		t.Fatalf("exit code = %d, want %d", exitCodeFor(wrapped), exitAuth)
	}
}

func TestClassifyUnknownErrorIsGenericFailureNotSuccess(t *testing.T) {
	got := classify(errors.New("something we never labelled"))
	if got.Kind == "" {
		t.Error("unclassified error has no machine token")
	}
	if code := exitCodeFor(errors.New("x")); code != exitFailure {
		t.Fatalf("unclassified exit = %d, want %d", code, exitFailure)
	}
}

func TestClassifyNilIsNil(t *testing.T) {
	if classify(nil) != nil {
		t.Fatal("classify(nil) must be nil")
	}
	if exitCodeFor(nil) != exitOK {
		t.Fatal("nil error must exit 0")
	}
}

func TestRenderErrorJSONIsMachineReadableWithPaths(t *testing.T) {
	err := conflictError([]string{"b.css", "a.css"}, "local differs")

	line := renderError(err, true)
	var got struct {
		Error   string   `json:"error"`
		Message string   `json:"message"`
		Paths   []string `json:"paths"`
	}
	if jsonErr := json.Unmarshal([]byte(line), &got); jsonErr != nil {
		t.Fatalf("--json error is not JSON: %v\n%s", jsonErr, line)
	}
	if got.Error != string(kindConflict) {
		t.Errorf("error token = %q, want %q", got.Error, kindConflict)
	}
	if len(got.Paths) != 2 || got.Paths[0] != "a.css" {
		t.Errorf("paths = %v, want them present and sorted", got.Paths)
	}
	if strings.Contains(line, "\n") {
		t.Errorf("JSON error must be one line: %q", line)
	}
}

func TestRenderErrorProseCarriesTheSamePaths(t *testing.T) {
	err := conflictError([]string{"a.css"}, "local differs")
	line := renderError(err, false)
	if !strings.Contains(line, "a.css") {
		t.Errorf("prose error dropped the path: %q", line)
	}
	if strings.HasPrefix(strings.TrimSpace(line), "{") {
		t.Errorf("prose mode emitted JSON: %q", line)
	}
}

func TestRenderErrorJSONNeverEmitsBarePathsKeyWhenThereAreNone(t *testing.T) {
	line := renderError(&dsxError{Kind: kindTransport, Msg: "http 503"}, true)
	if strings.Contains(line, "paths") {
		t.Errorf("empty paths must be omitted, not emitted as null: %q", line)
	}
}

func TestJSONRequestedScansArgsBeforeFlagParsing(t *testing.T) {
	// The error renderer runs outside any FlagSet, so it has to see --json
	// wherever it landed among the positional arguments.
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
		// The flag package honours an explicit false; so must the renderer, or
		// a caller who disabled JSON still gets a JSON failure.
		{[]string{"pull", "p", "d", "--json=false"}, false},
		{[]string{"pull", "p", "d", "-json=0"}, false},
	}
	for _, tc := range cases {
		if got := jsonRequested(tc.args); got != tc.want {
			t.Errorf("jsonRequested(%v) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

func TestDsxErrorUnwrapsToItsCause(t *testing.T) {
	cause := errors.New("dial tcp: refused")
	err := &dsxError{Kind: kindTransport, Msg: "unreachable", Err: cause}
	if !errors.Is(err, cause) {
		t.Fatal("dsxError must unwrap to its cause")
	}
	if !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("message lost: %q", err.Error())
	}
}
