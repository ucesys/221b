package schedule

import (
	"strings"
	"testing"
	"time"
)

func must(t *testing.T, s Spec) *Schedule {
	t.Helper()
	sc, err := Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestThePresetsAndTheirWords(t *testing.T) {
	for _, c := range []struct {
		spec       Spec
		after      string
		next, then string
		words      string
	}{
		{Spec{Kind: Daily, At: "03:00"}, "2026-11-05T10:00:00Z", "2026-11-06T03:00:00Z", "2026-11-07T03:00:00Z", "Every day at 03:00, UTC"},
		{Spec{Kind: Daily, At: "3:05", TimeZone: "Europe/Berlin"}, "2026-11-05T10:00:00Z", "2026-11-06T02:05:00Z", "2026-11-07T02:05:00Z", "Every day at 03:05, Europe/Berlin"},
		// Thursday 5 November: the next weekday is Friday, then Monday.
		{Spec{Kind: Weekdays, At: "06:30"}, "2026-11-05T07:00:00Z", "2026-11-06T06:30:00Z", "2026-11-09T06:30:00Z", "Weekdays (Monday to Friday) at 06:30, UTC"},
		{Spec{Kind: Weekly, At: "23:00", Days: []string{"sun", "wed"}}, "2026-11-05T07:00:00Z", "2026-11-08T23:00:00Z", "2026-11-11T23:00:00Z", "Every week on Wednesday and Sunday at 23:00, UTC"},
		{Spec{Kind: Hours, Every: 6, At: "00:30"}, "2026-11-05T07:00:00Z", "2026-11-05T12:30:00Z", "2026-11-05T18:30:00Z", "Every 6 hours from 00:30, UTC"},
		// Every 5 hours from 22:00 starts again each day.
		{Spec{Kind: Hours, Every: 5, At: "22:00"}, "2026-11-05T22:30:00Z", "2026-11-06T22:00:00Z", "2026-11-07T22:00:00Z", "Every 5 hours from 22:00, UTC"},
		{Spec{Kind: Cron, Cron: "15 2 1 * *", TimeZone: "America/New_York"}, "2026-11-05T07:00:00Z", "2026-12-01T07:15:00Z", "2027-01-01T07:15:00Z", "Cron “15 2 1 * *”, America/New_York"},
		{Spec{Kind: Cron, Cron: "0 4 * * 1-5"}, "2026-11-06T05:00:00Z", "2026-11-09T04:00:00Z", "2026-11-10T04:00:00Z", "Weekdays (Monday to Friday) at 04:00, UTC"},
		{Spec{Kind: Cron, Cron: "@weekly"}, "2026-11-05T07:00:00Z", "2026-11-08T00:00:00Z", "2026-11-15T00:00:00Z", "Every week on Sunday at 00:00, UTC"},
		{Spec{Kind: Cron, Cron: "*/20 9-10 * * *"}, "2026-11-05T09:40:00Z", "2026-11-05T10:00:00Z", "2026-11-05T10:20:00Z", "Cron “*/20 9-10 * * *”, UTC"},
	} {
		sc := must(t, c.spec)
		next := sc.Next(utc(c.after))
		if !next.Equal(utc(c.next)) {
			t.Errorf("%+v: next %s, want %s", c.spec, next.UTC(), c.next)
		}
		if then := sc.Next(next); !then.Equal(utc(c.then)) {
			t.Errorf("%+v: then %s, want %s", c.spec, then.UTC(), c.then)
		}
		if got := sc.Describe(); got != c.words {
			t.Errorf("%+v: %q, want %q", c.spec, got, c.words)
		}
	}
}

// Cron's rule: with both days restricted, either one runs; 7 is Sunday;
// names work; a day that never comes is no time at all.
func TestCronDays(t *testing.T) {
	sc := must(t, Spec{Kind: Cron, Cron: "0 0 13 * fri"})
	// Friday 6 November 2026, then Friday 13 November (both), then Friday 20.
	got := []string{}
	at := utc("2026-11-05T12:00:00Z")
	for i := 0; i < 3; i++ {
		at = sc.Next(at)
		got = append(got, at.Format("2006-01-02"))
	}
	if strings.Join(got, " ") != "2026-11-06 2026-11-13 2026-11-20" {
		t.Fatalf("%v", got)
	}
	if n := must(t, Spec{Kind: Cron, Cron: "0 12 * jan-mar 7"}).Next(utc("2026-11-05T00:00:00Z")); !n.Equal(utc("2027-01-03T12:00:00Z")) {
		t.Fatalf("Sunday as 7 in January: %s", n)
	}
	if n := must(t, Spec{Kind: Cron, Cron: "0 0 30 2 *"}).Next(utc("2026-11-05T00:00:00Z")); !n.IsZero() {
		t.Fatalf("30 February: %s", n)
	}
}

// Europe/Berlin moves its clocks forward on 29 March 2026 (02:00 becomes
// 03:00) and back on 25 October 2026 (03:00 becomes 02:00 again).
func TestTheClocksChanging(t *testing.T) {
	sc := must(t, Spec{Kind: Daily, At: "02:30", TimeZone: "Europe/Berlin"})
	// 02:30 does not exist on 29 March: the run is when the clocks change.
	n := sc.Next(utc("2026-03-28T12:00:00Z"))
	if !n.Equal(utc("2026-03-29T01:00:00Z")) {
		t.Fatalf("spring: %s", n.UTC())
	}
	if n = sc.Next(n); !n.Equal(utc("2026-03-30T00:30:00Z")) {
		t.Fatalf("the day after spring: %s", n.UTC())
	}
	// 02:30 happens twice on 25 October: the first only.
	n = sc.Next(utc("2026-10-24T12:00:00Z"))
	if !n.Equal(utc("2026-10-25T00:30:00Z")) {
		t.Fatalf("autumn: %s", n.UTC())
	}
	if n = sc.Next(n); !n.Equal(utc("2026-10-26T01:30:00Z")) {
		t.Fatalf("the second 02:30 ran too: %s", n.UTC())
	}
	// An hourly schedule runs through the change in wall-clock hours.
	hourly := must(t, Spec{Kind: Hours, Every: 1, At: "00:00", TimeZone: "Europe/Berlin"})
	var got []string
	at := utc("2026-03-28T23:30:00Z")
	for i := 0; i < 3; i++ {
		at = hourly.Next(at)
		got = append(got, at.In(hourly.Location()).Format("15:04 MST"))
	}
	if strings.Join(got, " ") != "01:00 CET 03:00 CEST 04:00 CEST" {
		t.Fatalf("hourly in spring: %v", got)
	}
	// New York, 1 November 2026: 01:30 twice, the first only.
	ny := must(t, Spec{Kind: Daily, At: "01:30", TimeZone: "America/New_York"})
	if n := ny.Next(utc("2026-11-01T00:00:00Z")); !n.Equal(utc("2026-11-01T05:30:00Z")) {
		t.Fatalf("New York: %s", n.UTC())
	}
	if n := ny.Next(utc("2026-11-01T05:30:00Z")); !n.Equal(utc("2026-11-02T06:30:00Z")) {
		t.Fatalf("New York, the day after: %s", n.UTC())
	}
}

// A run missed while the console was down is made once if it is within the
// grace, and let go if older; nothing is due before the first slot.
func TestARunMissedWhileDown(t *testing.T) {
	sc := must(t, Spec{Kind: Daily, At: "03:00"})
	set := utc("2026-11-04T12:00:00Z")
	if _, run := sc.Due(set, utc("2026-11-05T02:59:00Z"), time.Hour); run {
		t.Fatal("due before its time")
	}
	due, run := sc.Due(set, utc("2026-11-05T03:40:00Z"), time.Hour)
	if !run || !due.Equal(utc("2026-11-05T03:00:00Z")) {
		t.Fatalf("40 minutes late: %s %v", due, run)
	}
	due, run = sc.Due(set, utc("2026-11-05T05:00:00Z"), time.Hour)
	if run || !due.Equal(utc("2026-11-05T03:00:00Z")) {
		t.Fatalf("two hours late: %s %v", due, run)
	}
	// Down for three days: only the newest slot counts.
	due, run = sc.Due(set, utc("2026-11-08T03:30:00Z"), time.Hour)
	if !run || !due.Equal(utc("2026-11-08T03:00:00Z")) {
		t.Fatalf("three days down: %s %v", due, run)
	}
	if _, run := sc.Due(time.Time{}, utc("2026-11-08T03:30:00Z"), time.Hour); run {
		t.Fatal("a zero last ran")
	}
}

func TestWhatIsRefused(t *testing.T) {
	for _, c := range []struct {
		spec Spec
		want string
	}{
		{Spec{}, "no kind"},
		{Spec{Kind: "monthly"}, "not a kind"},
		{Spec{Kind: Daily, At: "25:00"}, "not HH:MM"},
		{Spec{Kind: Daily, At: "3:5"}, "not HH:MM"},
		{Spec{Kind: Weekly, At: "03:00"}, "at least one day"},
		{Spec{Kind: Weekly, At: "03:00", Days: []string{"funday"}}, "not a day"},
		{Spec{Kind: Hours, At: "03:00", Every: 24}, "1 to 23"},
		{Spec{Kind: Daily, At: "03:00", TimeZone: "Mars/Olympus"}, "not known"},
		{Spec{Kind: Cron, Cron: "* * *"}, "five fields"},
		{Spec{Kind: Cron, Cron: "61 * * * *"}, "minute"},
		{Spec{Kind: Cron, Cron: "0 5-2 * * *"}, "backwards"},
		{Spec{Kind: Cron, Cron: "*/0 * * * *"}, "step"},
	} {
		if err := c.spec.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %q", c.spec, err, c.want)
		}
	}
}
