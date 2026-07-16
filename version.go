package main

import (
	"runtime"
	"runtime/debug"
	"strings"
)

// version is stamped at release time with -ldflags "-X main.version=v1.2.3".
// It stays empty for `go build` and `go install`, where the build info the
// toolchain embeds is the more truthful answer.
var version = ""

func versionString() string { return buildVersion(version, debug.ReadBuildInfo) }

// buildVersion renders one line naming the build precisely enough to reproduce
// it from a bug report: what it calls itself, which commit it came from, and
// whether that commit's tree was clean.
func buildVersion(stamped string, readBuildInfo func() (*debug.BuildInfo, bool)) string {
	info, ok := readBuildInfo()
	if !ok {
		if stamped == "" {
			return "dsx (unknown) " + runtime.GOOS + "/" + runtime.GOARCH + " " + runtime.Version()
		}
		return "dsx " + stamped + " " + runtime.GOOS + "/" + runtime.GOARCH + " " + runtime.Version()
	}

	name := stamped
	if name == "" {
		name = info.Main.Version
	}
	if name == "" {
		name = "(unknown)"
	}

	settings := map[string]string{}
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}

	var sb strings.Builder
	sb.WriteString("dsx ")
	sb.WriteString(name)

	if rev := settings["vcs.revision"]; rev != "" {
		sb.WriteString(" ")
		sb.WriteString(shortRev(rev))
		if settings["vcs.modified"] == "true" {
			sb.WriteString("-dirty")
		}
	}

	goos, goarch := settings["GOOS"], settings["GOARCH"]
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	sb.WriteString(" " + goos + "/" + goarch)

	gover := info.GoVersion
	if gover == "" {
		gover = runtime.Version()
	}
	sb.WriteString(" " + gover)

	return sb.String()
}

func shortRev(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}
