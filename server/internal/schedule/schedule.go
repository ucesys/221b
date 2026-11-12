// Package schedule says when something repeats: every day at a time, on
// weekdays, weekly on some days, every few hours, or by a cron expression,
// in a time zone. It works out the next time, reads the schedule back in
// plain words, and decides whether a run missed while the console was down
// is still worth making. The scheduled backups use it, and the jobs will.
package schedule

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	// The zones go with the binary (about 450 KB), so a schedule's zone
	// reads the same in any image.
	_ "time/tzdata"
)

// The kinds of a Spec.
const (
	// Daily runs every day at At.
	Daily = "daily"
	// Weekdays runs Monday to Friday at At.
	Weekdays = "weekdays"
	// Weekly runs on Days at At.
	Weekly = "weekly"
	// Hours runs every Every hours, from At each day.
	Hours = "hours"
	// Cron runs when the cron expression Cron says.
	Cron = "cron"
)

// Spec is a schedule as a person sets it, and as it is kept.
type Spec struct {
	Kind string `json:"kind"`
	// At is the time of day, "HH:MM", 24-hour: when a daily, weekday or
	// weekly schedule runs, and the first run of each day of an every-N-
	// hours one.
	At string `json:"at,omitempty"`
	// Days are a weekly schedule's days: "mon" to "sun".
	Days []string `json:"days,omitempty"`
	// Every is the hours between the runs of an every-N-hours schedule, 1
	// to 23; the count starts again from At each day.
	Every int `json:"every,omitempty"`
	// Cron is a cron expression of five fields (minute, hour, day of month,
	// month, day of week) or @hourly, @daily, @weekly, @monthly, @yearly.
	Cron string `json:"cron,omitempty"`
	// TimeZone is an IANA name, such as Europe/Berlin; empty is UTC.
	TimeZone string `json:"timeZone,omitempty"`
}

// Schedule is a Spec read and checked.
type Schedule struct {
	spec Spec
	loc  *time.Location
	c    cron
}

var dayNames = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// Parse checks a Spec and makes its Schedule.
func Parse(s Spec) (*Schedule, error) {
	loc := time.UTC
	if tz := strings.TrimSpace(s.TimeZone); tz != "" && tz != "UTC" {
		l, err := time.LoadLocation(tz)
		if err != nil {
			return nil, fmt.Errorf("the time zone %q is not known", tz)
		}
		loc = l
	}
	expr := ""
	switch s.Kind {
	case Daily, Weekdays, Weekly, Hours:
		h, m, err := clock(s.At)
		if err != nil {
			return nil, err
		}
		switch s.Kind {
		case Daily:
			expr = fmt.Sprintf("%d %d * * *", m, h)
		case Weekdays:
			expr = fmt.Sprintf("%d %d * * 1-5", m, h)
		case Weekly:
			if len(s.Days) == 0 {
				return nil, errors.New("a weekly schedule needs at least one day")
			}
			var days []string
			for _, d := range s.Days {
				i := indexOf(dayNames, strings.ToLower(strings.TrimSpace(d)))
				if i < 0 {
					return nil, fmt.Errorf("%q is not a day (mon to sun)", d)
				}
				days = append(days, strconv.Itoa(i))
			}
			expr = fmt.Sprintf("%d %d * * %s", m, h, strings.Join(days, ","))
		case Hours:
			if s.Every < 1 || s.Every > 23 {
				return nil, errors.New("every N hours needs N from 1 to 23")
			}
			expr = fmt.Sprintf("%d %d/%d * * *", m, h, s.Every)
		}
	case Cron:
		expr = s.Cron
	case "":
		return nil, errors.New("the schedule has no kind")
	default:
		return nil, fmt.Errorf("%q is not a kind of schedule (daily, weekdays, weekly, hours or cron)", s.Kind)
	}
	c, err := parseCron(expr)
	if err != nil {
		return nil, err
	}
	return &Schedule{spec: s, loc: loc, c: c}, nil
}

// Validate says what is wrong with a Spec, or nil.
func (s Spec) Validate() error {
	_, err := Parse(s)
	return err
}

// clock reads "HH:MM".
func clock(at string) (int, int, error) {
	h, m, ok := strings.Cut(strings.TrimSpace(at), ":")
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if !ok || err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 || len(m) != 2 {
		return 0, 0, fmt.Errorf("the time %q is not HH:MM", at)
	}
	return hh, mm, nil
}

func indexOf(list []string, s string) int {
	for i, x := range list {
		if x == s {
			return i
		}
	}
	return -1
}

// Spec is what the Schedule was made from.
func (s *Schedule) Spec() Spec { return s.spec }

// Location is the schedule's time zone.
func (s *Schedule) Location() *time.Location { return s.loc }

// Next is the first time after t the schedule runs; zero when it never
// does in the next five years (a cron expression for February 30).
//
// Times are wall-clock times in the schedule's zone. A time the clocks
// skip when they go forward (02:30 on a spring night in Europe) runs when
// they change, at once; a time they pass twice when they go back runs the
// first time only.
func (s *Schedule) Next(t time.Time) time.Time {
	from := t.In(s.loc).Truncate(time.Minute).Add(time.Minute)
	// The wall clock to start from: a minute after t, as a reading.
	fy, fmo, fd := from.Date()
	fh, fmi := from.Hour(), from.Minute()
	day := time.Date(fy, fmo, fd, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5*366+2; i, day = i+1, day.AddDate(0, 0, 1) {
		y, mo, d := day.Date()
		if !s.c.month.has(int(mo)) || !s.c.dayMatches(d, int(day.Weekday())) {
			continue
		}
		first := i == 0
		for h := 0; h < 24; h++ {
			if !s.c.hour.has(h) || first && h < fh {
				continue
			}
			for m := 0; m < 60; m++ {
				if !s.c.minute.has(m) || first && h == fh && m < fmi {
					continue
				}
				if at := resolve(y, mo, d, h, m, s.loc); at.After(t) {
					return at
				}
			}
		}
	}
	return time.Time{}
}

// resolve is the instant a wall-clock time stands for: the first of two
// when the clocks go back, the change itself when they skip it.
func resolve(y int, mo time.Month, d, h, m int, loc *time.Location) time.Time {
	at := time.Date(y, mo, d, h, m, 0, 0, loc)
	want := wall(time.Date(y, mo, d, h, m, 0, 0, time.UTC))
	if wall(at) == want {
		// Clocks that went back show this time twice: the earlier one.
		_, now := at.Zone()
		_, before := at.Add(-3 * time.Hour).Zone()
		if before > now {
			if e := at.Add(-time.Duration(before-now) * time.Second); wall(e) == want {
				return e
			}
		}
		return at
	}
	// Skipped: the first minute whose wall clock reads past it, which is
	// when the clocks changed.
	for u := at.Add(-3 * time.Hour).Truncate(time.Minute); !u.After(at); u = u.Add(time.Minute) {
		if wall(u) >= want {
			return u
		}
	}
	return at
}

// wall is a time's reading on its clock, as a number that orders.
func wall(t time.Time) int64 {
	y, mo, d := t.Date()
	return ((int64(y)*100+int64(mo))*100+int64(d))*10000 + int64(t.Hour())*100 + int64(t.Minute())
}

// Due decides a run at now. last is the newest slot already handled (run
// or let go), or, for a schedule never run, when it was set up. due is
// the newest slot after last and at or before now, zero when there is
// none; run says whether to run it, which is when it is no older than
// grace: a slot missed by more, while the console was down, is let go and
// the schedule waits for its next. Either way the caller records due as
// handled. A zero last is never due: pass when the schedule was set up.
func (s *Schedule) Due(last, now time.Time, grace time.Duration) (due time.Time, run bool) {
	if last.IsZero() {
		return time.Time{}, false
	}
	next := s.Next(last)
	if next.IsZero() || next.After(now) {
		return time.Time{}, false
	}
	for i := 0; i < 1<<20; i++ {
		n := s.Next(next)
		if n.IsZero() || n.After(now) {
			break
		}
		next = n
	}
	return next, now.Sub(next) <= grace
}

// Describe says the schedule in plain words: "Every day at 03:00,
// Europe/Berlin".
func (s *Schedule) Describe() string {
	sp := s.spec
	tz := s.loc.String()
	at := strings.TrimSpace(sp.At)
	if h, m, err := clock(at); err == nil {
		at = fmt.Sprintf("%02d:%02d", h, m)
	}
	var what string
	switch sp.Kind {
	case Daily:
		what = "Every day at " + at
	case Weekdays:
		what = "Weekdays (Monday to Friday) at " + at
	case Weekly:
		var days []string
		for i, n := range dayNames {
			for _, d := range sp.Days {
				if strings.EqualFold(strings.TrimSpace(d), n) && !contains(days, longDays[i]) {
					days = append(days, longDays[i])
				}
			}
		}
		// Monday first, as the page lists them.
		if len(days) > 0 && days[0] == "Sunday" {
			days = append(days[1:], "Sunday")
		}
		what = "Every week on " + andList(days) + " at " + at
	case Hours:
		if sp.Every == 1 {
			what = "Every hour from " + at
		} else {
			what = fmt.Sprintf("Every %d hours from %s", sp.Every, at)
		}
	default:
		what = describeCron(strings.TrimSpace(sp.Cron))
	}
	return what + ", " + tz
}

var longDays = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

func contains(list []string, s string) bool { return indexOf(list, s) >= 0 }

func andList(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

// describeCron reads the common shapes of an expression in words, and
// gives the others as they are.
func describeCron(expr string) string {
	switch expr {
	case "@hourly":
		return "Every hour at :00"
	case "@daily", "@midnight":
		return "Every day at 00:00"
	case "@weekly":
		return "Every week on Sunday at 00:00"
	case "@monthly":
		return "On the 1st of every month at 00:00"
	case "@yearly", "@annually":
		return "Every 1 January at 00:00"
	}
	f := strings.Fields(expr)
	if len(f) == 5 {
		m, err1 := strconv.Atoi(f[0])
		h, err2 := strconv.Atoi(f[1])
		if err1 == nil && err2 == nil && f[2] == "*" && f[3] == "*" {
			at := fmt.Sprintf("%02d:%02d", h, m)
			switch f[4] {
			case "*":
				return "Every day at " + at
			case "1-5", "mon-fri", "MON-FRI":
				return "Weekdays (Monday to Friday) at " + at
			}
		}
		if err1 == nil && f[1] == "*" && f[2] == "*" && f[3] == "*" && f[4] == "*" {
			return fmt.Sprintf("Every hour at :%02d", m)
		}
	}
	return "Cron “" + expr + "”"
}
