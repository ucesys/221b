package imagecheck

import (
	"regexp"
	"strconv"
	"strings"
)

// A tag read as a version (HA-21): what comes before the numbers ("v",
// "release-"), the numbers, and what follows them ("-alpine",
// "-debian-12-r4", "-rc.1"). Two tags are compared only when they have
// the same form, so 1.2.3-alpine is compared with 1.2.4-alpine and never
// with 1.2.4 or 1.2.4-bookworm.
type version struct {
	prefix string
	nums   []int
	suffix string
	// sufNums are the numbers in the suffix: a build's r4, a candidate's
	// rc.2, compared after the version itself (only the last differs
	// between two tags of one form).
	sufNums []int
	pre     bool
	shape   string
}

var (
	tagForm = regexp.MustCompile(`^([A-Za-z]*[-_.]?)(\d+(?:\.\d+){0,3})([-+_].*)?$`)
	// preRelease marks a suffix that is a pre-release, compared only with
	// a running pre-release.
	preRelease = regexp.MustCompile(`(?i)^[-+_.]?(alpha|beta|rc|pre|preview|dev|snapshot|nightly|canary|edge|test|unstable|next)([-._]?\d+)*([-._]|$)`)
	digitRun   = regexp.MustCompile(`\d+`)
	// shaLike is a tag made from a commit or a digest: never a version.
	shaLike = regexp.MustCompile(`^(sha-?|sha256-)?[0-9a-f]{7,64}$`)
)

// parseVersion reads a tag as a version, or ok=false when it is not one:
// latest, stable, a commit's hash, a date with no other numbers.
func parseVersion(tag string) (version, bool) {
	t := strings.ToLower(tag)
	if t == "" || t == "latest" || shaLike.MatchString(t) && strings.ContainsAny(t, "abcdef") {
		return version{}, false
	}
	m := tagForm.FindStringSubmatch(tag)
	if m == nil {
		return version{}, false
	}
	v := version{prefix: strings.ToLower(m[1]), suffix: m[3]}
	for _, p := range strings.Split(m[2], ".") {
		n, err := strconv.Atoi(p)
		if err != nil || len(p) > 9 {
			return version{}, false
		}
		v.nums = append(v.nums, n)
	}
	for _, p := range digitRun.FindAllString(v.suffix, -1) {
		n, err := strconv.Atoi(p)
		if err != nil || len(p) > 9 {
			return version{}, false
		}
		v.sufNums = append(v.sufNums, n)
	}
	v.pre = preRelease.MatchString(v.suffix)
	// The form: the prefix, how many numbers, whether the first is a date
	// or a build number (six digits or more) rather than a major version,
	// and the suffix with its last number blanked.
	first := strings.Split(m[2], ".")[0]
	big := ""
	if len(first) >= 6 {
		big = "D"
	}
	v.shape = v.prefix + "|" + strconv.Itoa(len(v.nums)) + big + "|" + strings.ToLower(blankLast(v.suffix))
	return v, true
}

// blankLast blanks the suffix's last number, a build's or a
// pre-release's (r4, rc.2), and keeps the others as written: -debian-12-r4
// is the form -debian-12-r#, which -debian-11-r0 is not.
func blankLast(suffix string) string {
	runs := digitRun.FindAllStringIndex(suffix, -1)
	if len(runs) == 0 {
		return suffix
	}
	last := runs[len(runs)-1]
	return suffix[:last[0]] + "#" + suffix[last[1]:]
}

// compare orders two versions of one form: the numbers, then a release
// after its pre-releases, then the suffix's numbers.
func compare(a, b version) int {
	for i := 0; i < len(a.nums) || i < len(b.nums); i++ {
		x, y := at(a.nums, i), at(b.nums, i)
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	if a.pre != b.pre {
		if a.pre {
			return -1
		}
		return 1
	}
	for i := 0; i < len(a.sufNums) || i < len(b.sufNums); i++ {
		x, y := at(a.sufNums, i), at(b.sufNums, i)
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func at(s []int, i int) int {
	if i < len(s) {
		return s[i]
	}
	return 0
}

// Versioned says whether a tag reads as a version, so that newer tags of
// its form can be looked for.
func Versioned(tag string) bool {
	_, ok := parseVersion(tag)
	return ok
}

// Newest picks, among a repository's tags, the newest one of the running
// tag's form that is newer than it, or "" when there is none or the
// running tag is not a version (HA-21). Pre-releases count only when a
// pre-release runs: then the release it leads to counts too.
func Newest(running string, tags []string) string {
	rv, ok := parseVersion(running)
	if !ok {
		return ""
	}
	var best version
	bestTag := ""
	for _, t := range tags {
		cv, ok := parseVersion(t)
		if !ok || cv.pre && !rv.pre {
			continue
		}
		same := cv.shape == rv.shape
		// A running 1.3.0-rc.1 is followed by 1.3.0 itself.
		final := rv.pre && !cv.pre && cv.suffix == "" && cv.prefix == rv.prefix && len(cv.nums) == len(rv.nums)
		if !same && !final {
			continue
		}
		if compare(cv, rv) <= 0 {
			continue
		}
		if bestTag == "" || compare(cv, best) > 0 {
			best, bestTag = cv, t
		}
	}
	return bestTag
}
