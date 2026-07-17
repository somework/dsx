package cli

import (
	"testing"

	"github.com/somework/dsx/internal/clitest"
)

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
)

func syncFirstCall(t *testing.T, f *fakeMCP, tool string) clitest.Call {
	t.Helper()
	return clitest.FirstCall(t, f, tool)
}
