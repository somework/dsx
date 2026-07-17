package main

import "github.com/somework/dsx/internal/cli"

// -X main.version aims here by full symbol path; renaming or moving it ships
// (unknown) with no build error. CI stamps a probe and greps for it.
var version = ""

func main() { cli.Main(version) }
