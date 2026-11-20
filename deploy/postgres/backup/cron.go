package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a cron expression of five fields, read in UTC: minute
// (0-59), hour (0-23), day of the month (1-31), month (1-12 or JAN-DEC)
// and day of the week (0-7 or SUN-SAT, 0 and 7 both Sunday). A field is
// "*", a number, a range "a-b", a list "a,b", and any of these but a
// number with a step "/n". As in cron, a day matches when the day of the
// month or the day of the week does, when both are given; when one is
// "*", the other alone decides. @hourly, @daily (@midnight), @weekly,
// @monthly and @yearly (@annually) stand for their five fields.
type Schedule struct {
	text                     string
	minute, hour, dom, month uint64
	dow                      uint64
	domStar, dowStar         bool
}

var macros = map[string]string{
	"@hourly":   "0 * * * *",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@weekly":   "0 0 * * 0",
	"@monthly":  "0 0 1 * *",
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
}

var monthNames = map[string]int{"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6, "JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12}
var dayNames = map[string]int{"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6}

// ParseSchedule reads a cron expression.
func ParseSchedule(s string) (Schedule, error) {
	text := strings.TrimSpace(s)
	expr := text
	if m, ok := macros[strings.ToLower(expr)]; ok {
		expr = m
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return Schedule{}, fmt.Errorf("the schedule %q is a cron expression of five fields (minute hour day-of-month month day-of-week), such as \"0 2 * * *\"", s)
	}
	sc := Schedule{text: text}
	var err error
	if sc.minute, err = field(f[0], 0, 59, nil); err != nil {
		return Schedule{}, fmt.Errorf("the schedule's minute: %w", err)
	}
	if sc.hour, err = field(f[1], 0, 23, nil); err != nil {
		return Schedule{}, fmt.Errorf("the schedule's hour: %w", err)
	}
	if sc.dom, err = field(f[2], 1, 31, nil); err != nil {
		return Schedule{}, fmt.Errorf("the schedule's day of the month: %w", err)
	}
	if sc.month, err = field(f[3], 1, 12, monthNames); err != nil {
		return Schedule{}, fmt.Errorf("the schedule's month: %w", err)
	}
	if sc.dow, err = field(f[4], 0, 7, dayNames); err != nil {
		return Schedule{}, fmt.Errorf("the schedule's day of the week: %w", err)
	}
	if sc.dow&(1<<7) != 0 {
		sc.dow |= 1
	}
	sc.domStar = strings.HasPrefix(f[2], "*")
	sc.dowStar = strings.HasPrefix(f[4], "*")
	return sc, nil
}

// FromDailyAt is the schedule of a base backup each day at hh:mm UTC, the
// value of before (dailyAt).
func FromDailyAt(at string) (Schedule, error) {
	t, err := time.Parse("15:04", at)
	if err != nil || len(at) != 5 {
		return Schedule{}, fmt.Errorf("the time of day %q is HH:MM in UTC, such as \"02:00\"", at)
	}
	return ParseSchedule(fmt.Sprintf("%d %d * * *", t.Minute(), t.Hour()))
}

func (s Schedule) String() string { return s.text }

func field(f string, lo, hi int, names map[string]int) (uint64, error) {
	var bits uint64
	for _, part := range strings.Split(f, ",") {
		rng, step := part, 1
		if i := strings.IndexByte(part, '/'); i >= 0 {
			n, err := strconv.Atoi(part[i+1:])
			if err != nil || n < 1 {
				return 0, fmt.Errorf("%q: a step is a number of 1 or more", part)
			}
			rng, step = part[:i], n
		}
		var a, b int
		switch {
		case rng == "*":
			a, b = lo, hi
		case strings.Contains(rng, "-"):
			i := strings.IndexByte(rng, '-')
			var err error
			if a, err = value(rng[:i], names); err != nil {
				return 0, err
			}
			if b, err = value(rng[i+1:], names); err != nil {
				return 0, err
			}
		default:
			v, err := value(rng, names)
			if err != nil {
				return 0, err
			}
			if step != 1 {
				return 0, fmt.Errorf("%q: a step needs a range or *", part)
			}
			a, b = v, v
		}
		if a < lo || b > hi || a > b {
			return 0, fmt.Errorf("%q is outside %d-%d", part, lo, hi)
		}
		for v := a; v <= b; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, nil
}

func value(s string, names map[string]int) (int, error) {
	if v, ok := names[strings.ToUpper(s)]; ok {
		return v, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	return v, nil
}

func (s Schedule) day(t time.Time) bool {
	dom := s.dom&(1<<uint(t.Day())) != 0
	dow := s.dow&(1<<uint(t.Weekday())) != 0
	switch {
	case s.domStar || s.dowStar:
		return dom && dow
	default:
		return dom || dow
	}
}

// Last is the latest time at or before t that the schedule names, in UTC,
// looking back a year and a day at most; false when there is none in that
// time (a 30th of February).
func (s Schedule) Last(t time.Time) (time.Time, bool) {
	t = t.UTC().Truncate(time.Minute)
	stop := t.AddDate(-1, 0, -1)
	for !t.Before(stop) {
		if s.month&(1<<uint(t.Month())) == 0 || !s.day(t) {
			// The last minute of the day before.
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).Add(-time.Minute)
			continue
		}
		if s.hour&(1<<uint(t.Hour())) == 0 {
			t = t.Truncate(time.Hour).Add(-time.Minute)
			continue
		}
		if s.minute&(1<<uint(t.Minute())) == 0 {
			t = t.Add(-time.Minute)
			continue
		}
		return t, true
	}
	return time.Time{}, false
}
