package tunnel

import "testing"

// A minimum version is checked as Older reads it (the toolbox review,
// finding 11): one Older cannot read would turn the minimum off.
func TestValidVersion(t *testing.T) {
	for v, want := range map[string]bool{"0.8.0": true, "v0.11.1": true, "0.11": true, "1.2.3-rc1": true, ">=0.11.1": false, "0.11.*": false, "V0.11.1": false, "": false, "1.2.3.4": false, "dev": false} {
		if got := ValidVersion(v); got != want {
			t.Errorf("%q: %v", v, got)
		}
	}
	if !Older("0.10.0", "0.11.1") || Older("0.11.1", "0.11.1") || Older("v1.0.0", "0.11.1") {
		t.Error("Older")
	}
}
