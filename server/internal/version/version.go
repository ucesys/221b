// Package version holds the build version, set at link time with -ldflags.
package version

var (
	// Version is the release version, such as v0.1.0, or "dev" for local builds.
	Version = "dev"
	// Commit is the git commit the binary was built from.
	Commit = ""
)

// String returns the version with the short commit when it is known.
func String() string {
	if Commit == "" {
		return Version
	}
	c := Commit
	if len(c) > 7 {
		c = c[:7]
	}
	return Version + " (" + c + ")"
}
