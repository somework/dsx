// Command dsx moves files between a Claude Design project and a local
// directory without the bytes passing through a model's context.
//
// Everything it does lives under internal/. This file holds the two things that
// cannot: the entry point, and the symbol the release stamp aims at.
package main

import "github.com/somework/dsx/internal/cli"

// version is stamped at release time with -ldflags "-X main.version=v1.2.3".
// It stays empty for `go build` and `go install`, where the build info the
// toolchain embeds is the more truthful answer.
//
// It must stay in package main, and it must stay named `version`: -X names a
// symbol, and a stale -X against one that no longer exists builds with no error
// and no warning. The binary would ship reporting "(unknown)" while every test
// stayed green, because version_test.go passes the stamp in itself rather than
// reading this. Nothing in the test suite can see a link-time flag; CI builds
// with a probe stamp and greps for it, which is the only check that can.
var version = ""

func main() { cli.Main(version) }
