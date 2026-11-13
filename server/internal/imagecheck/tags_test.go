package imagecheck

import "testing"

func TestNewestPicksTheNewerTagOfTheSameForm(t *testing.T) {
	tags := []string{
		"latest", "stable", "1.2.2", "1.2.3", "1.2.4", "1.3.0", "2.0.0-beta.1",
		"1.2.3-alpine", "1.2.9-alpine", "1.4.0-alpine3.19",
		"v1.2.3", "v1.5.0",
		"1.25.3-debian-12-r4", "1.25.3-debian-12-r7", "1.25.4-debian-12-r0", "1.26.0-debian-11-r0",
		"7", "8", "7.2",
		"20240101", "sha-abc1234", "0123abcd",
		"1.3.0-rc.1", "1.3.0-rc.2",
	}
	for _, c := range []struct{ running, want string }{
		{"1.2.3", "1.3.0"},                             // pre-releases are left out for a release
		{"1.3.0", ""},                                  // the newest
		{"1.2.3-alpine", "1.2.9-alpine"},               // the suffix's form: alpine3.19 is another
		{"v1.2.3", "v1.5.0"},                           // the prefix
		{"1.25.3-debian-12-r4", "1.25.4-debian-12-r0"}, // a build number, the same form
		{"1.25.4-debian-12-r0", ""},                    // debian-11 is another form
		{"7", "8"},                                     // one number; 20240101 is a date, not a major
		{"1.3.0-rc.1", "1.3.0"},                        // a pre-release: its release counts
		{"1.2.3-rc.1", "1.3.0"},                        // and the releases after it
		{"latest", ""},                                 // not a version
		{"", ""},
		{"sha-abc1234", ""},
		{"0123abcd", ""},
		{"stable", ""},
		{"9.9.9", ""},
	} {
		if got := Newest(c.running, tags); got != c.want {
			t.Errorf("Newest(%q) = %q, want %q", c.running, got, c.want)
		}
	}
}

func TestPreReleasesAreComparedWhenOneRuns(t *testing.T) {
	tags := []string{"2.0.0-rc.1", "2.0.0-rc.2", "2.0.0-beta.3"}
	if got := Newest("2.0.0-rc.1", tags); got != "2.0.0-rc.2" {
		t.Fatalf("got %q", got)
	}
	if got := Newest("1.9.0", tags); got != "" {
		t.Fatalf("a release was offered a pre-release: %q", got)
	}
}

func TestVersioned(t *testing.T) {
	for tag, want := range map[string]bool{
		"1.2.3": true, "v1": true, "release-1.2": true, "1.2.3-alpine": true, "2024.01.02": true,
		"latest": false, "": false, "main": false, "deadbeef1": false, "sha256-0123456789abcdef": false, "1.2.3a": false,
	} {
		if got := Versioned(tag); got != want {
			t.Errorf("Versioned(%q) = %v, want %v", tag, got, want)
		}
	}
}

func TestHostAndRepositoryOfDockerHub(t *testing.T) {
	for _, c := range []struct{ registry, repo, host, path string }{
		{"", "nginx", "registry-1.docker.io", "library/nginx"},
		{"docker.io", "bitnami/redis", "registry-1.docker.io", "bitnami/redis"},
		{"ghcr.io", "ucesys/221b", "ghcr.io", "ucesys/221b"},
		{"registry.example.com:5000", "team/app", "registry.example.com:5000", "team/app"},
	} {
		if h, p := Host(c.registry), Repository(c.registry, c.repo); h != c.host || p != c.path {
			t.Errorf("%s %s: %s %s", c.registry, c.repo, h, p)
		}
	}
}

func TestParseChallenge(t *testing.T) {
	scheme, p := parseChallenge(`Bearer realm="https://auth.example.com/token",service="registry.example.com",scope="repository:a/b:pull"`)
	if scheme != "Bearer" || p["realm"] != "https://auth.example.com/token" || p["service"] != "registry.example.com" || p["scope"] != "repository:a/b:pull" {
		t.Fatalf("%s %v", scheme, p)
	}
}

func TestNextPageStaysOnTheRegistry(t *testing.T) {
	cur := "https://r.example.com/v2/a/tags/list?n=1000"
	if got := nextPage(cur, `</v2/a/tags/list?last=x&n=1000>; rel="next"`); got != "https://r.example.com/v2/a/tags/list?last=x&n=1000" {
		t.Fatalf("got %q", got)
	}
	if got := nextPage(cur, `<https://elsewhere.example.com/v2/a/tags/list?last=x>; rel="next"`); got != "" {
		t.Fatalf("followed another host: %q", got)
	}
}
