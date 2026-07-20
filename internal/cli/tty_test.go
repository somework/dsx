package cli

import (
	"os"
	"testing"
)

// DSX_PROGRESS overrides in both directions, so a terminal can be silenced and
// a pipe can be made to draw — the second is what makes the drawing branch
// reachable under test at all.
func TestProgressWriterHonoursTheOverrideBothWays(t *testing.T) {
	t.Setenv("DSX_PROGRESS", "never")
	if progressWriter() != nil {
		t.Error("DSX_PROGRESS=never still returned a writer")
	}

	t.Setenv("DSX_PROGRESS", "always")
	if progressWriter() == nil {
		t.Error("DSX_PROGRESS=always returned no writer")
	}
}

// Under `go test` stderr is not a character device, so the default is silence.
// A counter drawn into a redirected stream is noise in a log file.
func TestProgressWriterIsSilentWhenStderrIsNotATerminal(t *testing.T) {
	t.Setenv("DSX_PROGRESS", "")

	fi, err := os.Stderr.Stat()
	if err != nil {
		t.Skipf("cannot stat stderr: %v", err)
	}
	if fi.Mode()&os.ModeCharDevice != 0 {
		t.Skip("stderr is a terminal here; nothing to assert")
	}
	if progressWriter() != nil {
		t.Error("a non-terminal stderr still got a writer")
	}
}

// An unrecognised value is not a third mode: it falls through to the terminal
// test rather than silently meaning one of the two.
func TestAnUnknownProgressValueFallsThroughToTheTerminalTest(t *testing.T) {
	t.Setenv("DSX_PROGRESS", "yes-please")

	fi, err := os.Stderr.Stat()
	if err != nil {
		t.Skipf("cannot stat stderr: %v", err)
	}
	wantWriter := fi.Mode()&os.ModeCharDevice != 0
	if got := progressWriter() != nil; got != wantWriter {
		t.Errorf("writer=%v, want %v — an unknown value must not be a mode of its own", got, wantWriter)
	}
}
