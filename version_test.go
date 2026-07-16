package main

import (
	"runtime/debug"
	"strings"
	"testing"
)

func buildInfo(mainVersion string, settings ...debug.BuildSetting) func() (*debug.BuildInfo, bool) {
	return func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			GoVersion: "go1.26.5",
			Main:      debug.Module{Path: "github.com/somework/dsx", Version: mainVersion},
			Settings:  settings,
		}, true
	}
}

func TestBuildVersionPrefersLdflagsOverBuildInfo(t *testing.T) {
	// A release binary is stamped by goreleaser; that name is the truth even
	// though the build info also carries a module version.
	got := buildVersion("v1.2.3", buildInfo("v0.0.9"))
	if !strings.HasPrefix(got, "dsx v1.2.3 ") {
		t.Fatalf("ldflags version ignored: %q", got)
	}
	if strings.Contains(got, "v0.0.9") {
		t.Fatalf("build-info version leaked past the stamp: %q", got)
	}
}

func TestBuildVersionUsesModuleVersionFromGoInstall(t *testing.T) {
	got := buildVersion("", buildInfo("v0.4.1"))
	if !strings.HasPrefix(got, "dsx v0.4.1 ") {
		t.Fatalf("module version not reported: %q", got)
	}
}

func TestBuildVersionReportsRevisionAndDirtyTree(t *testing.T) {
	got := buildVersion("", buildInfo("(devel)",
		debug.BuildSetting{Key: "vcs.revision", Value: "008db5712c0ffee"},
		debug.BuildSetting{Key: "vcs.modified", Value: "true"},
		debug.BuildSetting{Key: "GOOS", Value: "linux"},
		debug.BuildSetting{Key: "GOARCH", Value: "amd64"},
	))
	for _, want := range []string{"(devel)", "008db57", "dirty", "linux/amd64", "go1.26.5"} {
		if !strings.Contains(got, want) {
			t.Errorf("version %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "\n") {
		t.Errorf("version must stay one line: %q", got)
	}
}

func TestBuildVersionSurvivesMissingBuildInfo(t *testing.T) {
	got := buildVersion("", func() (*debug.BuildInfo, bool) { return nil, false })
	if got == "" || strings.Contains(got, "\n") {
		t.Fatalf("want a one-line fallback, got %q", got)
	}
}

func TestBuildVersionOmitsDirtyOnACleanTree(t *testing.T) {
	got := buildVersion("", buildInfo("(devel)",
		debug.BuildSetting{Key: "vcs.revision", Value: "008db5712c0ffee"},
		debug.BuildSetting{Key: "vcs.modified", Value: "false"},
	))
	if strings.Contains(got, "dirty") {
		t.Fatalf("clean tree reported as dirty: %q", got)
	}
}
