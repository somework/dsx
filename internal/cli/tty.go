package cli

import (
	"io"
	"os"
)

// progressWriter decides where a transfer counter draws, once, here. syncer
// never asks the terminal itself: a package that reads os.Stderr's mode would
// have a dark branch under any test that swaps the stream for a pipe.
//
// Only presentation hangs off this. Nothing semantic — no mutation, no exit
// code, not one byte of stdout — changes with the answer.
func progressWriter() io.Writer {
	switch os.Getenv("DSX_PROGRESS") {
	case "never":
		return nil
	case "always":
		return os.Stderr
	}
	fi, err := os.Stderr.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return nil
	}
	return os.Stderr
}
