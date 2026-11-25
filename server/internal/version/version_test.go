package version

import "testing"

// The account menu's line (item 6 of phase-13.1-ai): the chart's version
// and the short commit, the version alone for a release.
func TestTheConsolesVersionAndBuild(t *testing.T) {
	keep := [3]string{Version, Commit, Chart}
	t.Cleanup(func() { Version, Commit, Chart = keep[0], keep[1], keep[2] })
	for _, c := range []struct {
		version, commit, chart string
		console, build         string
		released               bool
	}{
		// Between tags: git describe names the last tag, the chart the next release.
		{"v0.12.1-40-g9ef9683", "9ef9683a1b2c", "0.13.0", "0.13.0", "9ef9683", false},
		{"v0.13.0-dirty", "9ef9683a1b2c", "0.13.0", "0.13.0", "9ef9683", false},
		{"ci-9ef9683a1b2c", "9ef9683a1b2c", "0.13.0", "0.13.0", "9ef9683", false},
		// A release: the tag as the Makefile gives it, and as CI does.
		{"v0.13.0", "9ef9683a1b2c", "0.13.0", "0.13.0", "", true},
		{"0.13.0", "9ef9683a1b2c", "0.13.0", "0.13.0", "", true},
		// A build that did not stamp the chart's version.
		{"v0.13.0", "9ef9683a1b2c", "", "0.13.0", "9ef9683", false},
		{"dev", "", "", "dev", "", false},
	} {
		Version, Commit, Chart = c.version, c.commit, c.chart
		if Console() != c.console || Build() != c.build || Released() != c.released {
			t.Errorf("%+v: console %q, build %q, released %v", c, Console(), Build(), Released())
		}
	}
	Version, Commit, Chart = "v0.13.0", "9ef9683a1b2c", ""
	if String() != "v0.13.0 (9ef9683)" {
		t.Errorf("String: %q", String())
	}
}
