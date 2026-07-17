package cli

import (
	"testing"

	"github.com/somework/dsx/internal/clitest"
)

// The adapter itself now lives in internal/clitest, because every command
// package needs it and each is tested by its own internal tests. Its doc carries
// the reasoning, including why internal/syncer's copy stays a duplicate.
//
// These aliases exist so that this package's tests keep their old spellings.
// They are not a second implementation: every one is the clitest name.

type (
	fakeMCP   = clitest.Server
	fakeReply = clitest.Reply
)

var (
	newFakeMCP    = clitest.New
	envelopeFor   = clitest.EnvelopeFor
	fakeClient    = clitest.Client
	listingFor    = clitest.ListingFor
	fileEntry     = clitest.FileEntry
	dirEntry      = clitest.DirEntry
	captureStdout = clitest.CaptureStdout
	mkfile        = clitest.Mkfile
	syncSeedState = clitest.SeedState

	syncLedgerExists = clitest.LedgerExists
)

func syncFirstCall(t *testing.T, f *fakeMCP, tool string) clitest.Call {
	t.Helper()
	return clitest.FirstCall(t, f, tool)
}
