package main

import (
	"encoding/json"
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// version is stamped at release time with -ldflags "-X main.version=v1.2.3".
// It stays empty for `go build` and `go install`, where the build info the
// toolchain embeds is the more truthful answer.
var version = ""

func versionString() string { return buildVersion(version, debug.ReadBuildInfo) }

// versionInfo is the --json shape. Field names are contract.
type versionInfo struct {
	Version  string `json:"version"`
	Revision string `json:"revision,omitempty"`
	Dirty    bool   `json:"dirty,omitempty"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Go       string `json:"go"`
}

// cmdVersion reports the build.
//
// It takes --json like everything else. The guarantee is that under --json
// stdout is one JSON document, with no carve-out; version used to be dispatched
// before any FlagSet and printed prose regardless, so the one command a caller
// runs to find out what it is talking to was the one that broke the contract.
func cmdVersion(args []string) error {
	flags := newFlagSet("version")
	asJSON := jsonFlag(flags)
	pos, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	if err := noPositionals(pos, "version [--json]"); err != nil {
		return err
	}
	if !*asJSON {
		fmt.Println(versionString())
		return nil
	}
	b, err := json.Marshal(buildVersionInfo(version, debug.ReadBuildInfo))
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

func buildVersionInfo(stamped string, readBuildInfo func() (*debug.BuildInfo, bool)) versionInfo {
	v := versionInfo{Version: stamped, OS: runtime.GOOS, Arch: runtime.GOARCH, Go: runtime.Version()}

	info, ok := readBuildInfo()
	if !ok {
		if v.Version == "" {
			v.Version = "(unknown)"
		}
		return v
	}
	if v.Version == "" {
		v.Version = info.Main.Version
	}
	if v.Version == "" {
		v.Version = "(unknown)"
	}
	if info.GoVersion != "" {
		v.Go = info.GoVersion
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			v.Revision = shortRev(s.Value)
		case "vcs.modified":
			v.Dirty = s.Value == "true"
		case "GOOS":
			v.OS = s.Value
		case "GOARCH":
			v.Arch = s.Value
		}
	}
	return v
}

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
