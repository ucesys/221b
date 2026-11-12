package issues

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Target is how soon a priority asks for a first human response and a
// resolution, in minutes; 0 sets no target (IS-05).
type Target struct {
	ResponseMinutes int `json:"responseMinutes"`
	ResolveMinutes  int `json:"resolveMinutes"`
}

// Settings are the issue service's own (IS-29), kept with its records.
type Settings struct {
	// Company is the name in the report page's header ("Example Corp IT").
	Company string `json:"company"`
	// StatusPage is where every known problem is published, on Confluence
	// say; empty hides the link.
	StatusPage string `json:"statusPage"`
	// ReportURL is the report page's address other pages link to; empty is
	// /report on the console's own address.
	ReportURL string `json:"reportURL"`
	// MatchThreshold is the confidence, 0 to 100, at which a report is
	// linked to a known issue without asking anyone (IS-10).
	MatchThreshold int `json:"matchThreshold"`
	// Targets are the SLA: P0 to P4's, by "0".."4", for every case with an
	// SLA clock (Settings › SLA, CU-06, CF-02).
	Targets map[string]Target `json:"targets"`
	// RetentionDays is how long a closed report's text is kept (IS-17).
	RetentionDays int `json:"retentionDays"`
	// RatePerHour is how many reports one reporter may send in an hour
	// (IS-25).
	RatePerHour int `json:"ratePerHour"`
	// The timers of IS-15, in days, and the hours within which alike
	// reports on one service are taken for duplicates (IS-12).
	ReopenDays     int `json:"reopenDays"`
	FixedDays      int `json:"fixedDays"`
	NeedInfoDays   int `json:"needInfoDays"`
	DuplicateHours int `json:"duplicateHours"`
	// BumpP2At and BumpP1At are how many open reports on one known issue
	// raise them all to P2 and to P1 (IS-13); 0 never.
	BumpP2At int `json:"bumpP2At"`
	BumpP1At int `json:"bumpP1At"`
	// Webhook is told of changes, with summaries only (IS-30).
	Webhook Webhook `json:"webhook"`
}

// Webhook is the outgoing notification: generic JSON with a text field a
// Slack incoming webhook takes as it is.
type Webhook struct {
	URL string `json:"url"`
	// Events are the kinds sent; empty sends every kind.
	Events []string `json:"events,omitempty"`
}

// DefaultSettings are what a new console starts with.
func DefaultSettings() Settings {
	return Settings{
		MatchThreshold: 80,
		Targets: map[string]Target{
			"0": {ResponseMinutes: 15, ResolveMinutes: 4 * 60},
			"1": {ResponseMinutes: 60, ResolveMinutes: 24 * 60},
			"2": {ResponseMinutes: 4 * 60, ResolveMinutes: 5 * 24 * 60},
			"3": {ResponseMinutes: 24 * 60},
			"4": {ResponseMinutes: 2 * 24 * 60},
		},
		RetentionDays:  365,
		RatePerHour:    10,
		ReopenDays:     7,
		FixedDays:      3,
		NeedInfoDays:   5,
		DuplicateHours: 6,
		BumpP2At:       3,
		BumpP1At:       10,
	}
}

// withDefaults fills what a stored copy leaves out, so a field added later
// has its default.
func (s Settings) withDefaults() Settings {
	d := DefaultSettings()
	if s.MatchThreshold == 0 {
		s.MatchThreshold = d.MatchThreshold
	}
	targets := map[string]Target{}
	for k, v := range d.Targets {
		if t, ok := s.Targets[k]; ok {
			v = t
		}
		targets[k] = v
	}
	s.Targets = targets
	for _, f := range []struct {
		v *int
		d int
	}{{&s.RetentionDays, d.RetentionDays}, {&s.RatePerHour, d.RatePerHour}, {&s.ReopenDays, d.ReopenDays}, {&s.FixedDays, d.FixedDays}, {&s.NeedInfoDays, d.NeedInfoDays}, {&s.DuplicateHours, d.DuplicateHours}} {
		if *f.v == 0 {
			*f.v = f.d
		}
	}
	return s
}

// ErrInvalid is a request the service refuses as it stands; the message
// says what to change.
var ErrInvalid = errors.New("invalid")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Validate says what is wrong with settings, in words for the person.
func (s Settings) Validate() error {
	if len(s.Company) > 120 {
		return invalid("the company name is longer than 120 characters")
	}
	for _, u := range []struct{ name, v string }{{"the status page", s.StatusPage}, {"the report page", s.ReportURL}} {
		if u.v != "" && !webURL(u.v) {
			return invalid("%s must be an http or https address", u.name)
		}
	}
	if s.Webhook.URL != "" && !webURL(s.Webhook.URL) {
		return invalid("the webhook must be an http or https address")
	}
	if s.MatchThreshold < 1 || s.MatchThreshold > 100 {
		return invalid("the match threshold is 1 to 100")
	}
	for k, t := range s.Targets {
		if n, err := strconv.Atoi(k); err != nil || strconv.Itoa(n) != k || n < MinPriority || n > MaxPriority {
			return invalid("targets are for priorities %d to %d, not %q", MinPriority, MaxPriority, k)
		}
		if t.ResponseMinutes < 0 || t.ResolveMinutes < 0 {
			return invalid("a target of %s is negative", k)
		}
	}
	for _, f := range []struct {
		name      string
		v, lo, hi int
	}{{"the retention", s.RetentionDays, 1, 3650}, {"the rate limit", s.RatePerHour, 1, 1000}, {"reopening", s.ReopenDays, 1, 90},
		{"closing a fixed report", s.FixedDays, 1, 90}, {"closing a question unanswered", s.NeedInfoDays, 1, 90}, {"the duplicate window", s.DuplicateHours, 1, 24 * 14}} {
		if f.v < f.lo || f.v > f.hi {
			return invalid("%s must be %d to %d", f.name, f.lo, f.hi)
		}
	}
	if s.BumpP2At < 0 || s.BumpP1At < 0 {
		return invalid("a priority bump is a number of reports, 0 for none")
	}
	return nil
}

func webURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// target is a priority's target.
func (s Settings) target(priority int) Target {
	return s.Targets[strconv.Itoa(priority)]
}
