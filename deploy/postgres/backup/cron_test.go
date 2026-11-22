package main

import (
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestASchedulesLastTime(t *testing.T) {
	for _, c := range []struct {
		expr, now, want string
	}{
		{"0 2 * * *", "2026-11-20 10:00", "2026-11-20 02:00"},
		{"0 2 * * *", "2026-11-20 02:00", "2026-11-20 02:00"},
		{"0 2 * * *", "2026-11-20 01:59", "2026-11-19 02:00"},
		{"30 */6 * * *", "2026-11-20 13:00", "2026-11-20 12:30"},
		{"*/15 * * * *", "2026-11-20 13:44", "2026-11-20 13:30"},
		{"0 3 * * SUN", "2026-11-20 13:00", "2026-11-15 03:00"}, // a Friday
		{"0 3 * * 7", "2026-11-20 13:00", "2026-11-15 03:00"},
		{"0 3 * * mon-fri", "2026-11-22 13:00", "2026-11-20 03:00"}, // a Sunday
		{"0 0 1 * *", "2026-11-20 13:00", "2026-11-01 00:00"},
		{"0 0 1 jan *", "2026-11-20 13:00", "2026-01-01 00:00"},
		{"0 4 1,15 * *", "2026-11-20 13:00", "2026-11-15 04:00"},
		// Both days given: either one.
		{"0 5 1 * 3", "2026-11-20 13:00", "2026-11-18 05:00"},
		{"@daily", "2026-11-20 13:00", "2026-11-20 00:00"},
		{"@weekly", "2026-11-20 13:00", "2026-11-15 00:00"},
		{"@hourly", "2026-11-20 13:59", "2026-11-20 13:00"},
		{"0 2-4/2 * * *", "2026-11-20 03:59", "2026-11-20 02:00"},
	} {
		s, err := ParseSchedule(c.expr)
		if err != nil {
			t.Fatalf("%q: %v", c.expr, err)
		}
		got, ok := s.Last(at(c.now))
		if !ok || !got.Equal(at(c.want)) {
			t.Errorf("%q at %s: %v %v, want %s", c.expr, c.now, got, ok, c.want)
		}
	}
}

func TestATimeThatNeverComesIsNone(t *testing.T) {
	s, err := ParseSchedule("0 0 30 2 *")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Last(at("2026-11-20 13:00")); ok {
		t.Fatalf("the 30th of February: %v", got)
	}
}

func TestASchedulesMistakesAreRefused(t *testing.T) {
	for _, e := range []string{"", "0 2 * *", "0 2 * * * *", "60 2 * * *", "0 24 * * *", "0 2 0 * *", "0 2 * 13 *", "0 2 * * 8", "a 2 * * *", "0 2/0 * * *", "5/2 * * * *", "0 4-2 * * *", "@often", "02:00"} {
		if _, err := ParseSchedule(e); err == nil {
			t.Errorf("%q was taken", e)
		}
	}
}

func TestDailyAtIsTheSameSchedule(t *testing.T) {
	a, err := FromDailyAt("02:30")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ParseSchedule("30 2 * * *")
	for _, now := range []string{"2026-11-20 02:29", "2026-11-20 02:30", "2026-11-20 23:00"} {
		x, _ := a.Last(at(now))
		y, _ := b.Last(at(now))
		if !x.Equal(y) {
			t.Fatalf("at %s: %v and %v", now, x, y)
		}
	}
	for _, bad := range []string{"2:30", "24:00", "02:60", "noon", ""} {
		if _, err := FromDailyAt(bad); err == nil {
			t.Errorf("%q was taken", bad)
		}
	}
}
