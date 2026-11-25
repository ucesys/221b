// Package version holds the build version, set at link time with -ldflags.
package version

import "strings"

var (
	// Version is the release version, such as v0.1.0, or "dev" for local builds.
	Version = "dev"
	// Commit is the git commit the binary was built from.
	Commit = ""
	// Chart is the chart's version the binary was built with (the version
	// in deploy/helm/221b/Chart.yaml), stamped by the Makefile and the
	// Dockerfile; "" when the build did not stamp it.
	Chart = ""
)

// String returns the version with the short commit when it is known.
func String() string {
	if Commit == "" {
		return Version
	}
	return Version + " (" + ShortCommit() + ")"
}

// ShortCommit is the commit's first seven characters, "" when unknown.
func ShortCommit() string {
	c := Commit
	if len(c) > 7 {
		c = c[:7]
	}
	return c
}

// Console is the console's version as people read it (the account menu):
// the chart's version, or Version without its v when the build did not
// stamp the chart's.
func Console() string {
	if Chart != "" {
		return Chart
	}
	return strings.TrimPrefix(Version, "v")
}

// Released says whether the build is a release: Version is the tag of the
// chart's version (v0.13.0, or 0.13.0 as CI passes it for a tag), not a
// build between tags (v0.12.1-12-g9ef9683, ci-9ef9683…, dev).
func Released() bool {
	return Chart != "" && strings.TrimPrefix(Version, "v") == Chart
}

// Build is the build's short commit as the account menu shows it after the
// version, "" for a release (whose version says it all) or when unknown.
func Build() string {
	if Released() {
		return ""
	}
	return ShortCommit()
}
