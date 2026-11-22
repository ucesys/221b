package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// What hack/check-db-backup.sh checked of the script of before, as tests
// of the loop: who takes the base backup and when. The stops are in
// TestTheContainerStopsWhenTold, which runs the program.

type fakeArchive struct {
	pushes  int
	retains []int
	backups []time.Time
	fail    bool
	now     func() time.Time
}

func (a *fakeArchive) Push(context.Context) error {
	a.pushes++
	if a.fail {
		return errors.New("wal-g failed")
	}
	a.backups = append(a.backups, a.now())
	return nil
}

func (a *fakeArchive) Retain(_ context.Context, n int) error {
	a.retains = append(a.retains, n)
	return nil
}

func (a *fakeArchive) Since(_ context.Context, t time.Time) bool {
	for _, b := range a.backups {
		if !b.Before(t) {
			return true
		}
	}
	return false
}

type world struct {
	t       *testing.T
	now     time.Time
	state   *State
	archive *fakeArchive
	logs    []string
}

const (
	alone = "alone"
	three = "three"
)

func (w *world) cluster(kind string, timeline int) {
	switch kind {
	case alone:
		w.state = &State{Who: "db-0", Leader: "db-0", Timeline: timeline}
	case three:
		w.state = &State{Who: "db-1", Leader: "db-0", Timeline: timeline}
	}
}

// runner is the member db-0, with schedule, to volume when given.
func (w *world) runner(schedule, volume string) *Runner {
	s, err := ParseSchedule(schedule)
	if err != nil {
		w.t.Fatal(err)
	}
	return &Runner{
		Me: "db-0", Schedule: s, Retain: 7, Volume: volume, Archive: w.archive,
		Now: func() time.Time { return w.now },
		Cluster: func(context.Context) (State, error) {
			if w.state == nil {
				return State{}, errors.New("no answer")
			}
			st := *w.state
			if volume != "" {
				st.Who = st.Leader
			}
			return st, nil
		},
		Log: func(f string, a ...any) { w.logs = append(w.logs, fmt.Sprintf(f, a...)) },
	}
}

func newWorld(t *testing.T, now string) *world {
	w := &world{t: t, now: at(now)}
	w.archive = &fakeArchive{now: func() time.Time { return w.now }}
	return w
}

// steps runs n looks, 30 seconds apart.
func (w *world) steps(r *Runner, n int) {
	for range n {
		r.Step(context.Background())
		w.now = w.now.Add(30 * time.Second)
	}
}

func marker(t *testing.T, vol string) string {
	b, _ := os.ReadFile(filepath.Join(vol, ".primary-timeline"))
	return strings.TrimSpace(string(b))
}

const (
	never = "59 23 * * *" // not due in these tests' hours
	due   = "0 0 * * *"   // due since midnight
)

func TestAVolumesPrimaryTakesABaseBackupWhenItsLogBegins(t *testing.T) {
	vol := t.TempDir()
	w := newWorld(t, "2026-11-20 10:00")

	// The first primary takes a base backup as soon as it runs, and writes
	// the timeline it led down.
	w.cluster(alone, 1)
	w.steps(w.runner(never, vol), 3)
	if w.archive.pushes != 1 || marker(t, vol) != "1" {
		t.Fatalf("the first primary: %d pushes, timeline %q", w.archive.pushes, marker(t, vol))
	}

	// Its own restart is one timeline on, and its volume has all the log.
	w.archive.pushes = 0
	w.cluster(alone, 2)
	w.steps(w.runner(never, vol), 3)
	if w.archive.pushes != 0 {
		t.Fatalf("after its own restart: %d pushes", w.archive.pushes)
	}

	// Another member led in between: this volume has no history file of
	// the timeline it took, so no log for that time.
	w.cluster(alone, 4)
	w.steps(w.runner(never, vol), 3)
	if w.archive.pushes != 1 || marker(t, vol) != "4" {
		t.Fatalf("another member led in between: %d pushes, timeline %q", w.archive.pushes, marker(t, vol))
	}

	// Its own restarts, too short to be written down: the volume has the
	// history files of the timelines in between, so it has all the log.
	w.archive.pushes = 0
	os.MkdirAll(filepath.Join(vol, "wal_005"), 0o700)
	os.WriteFile(filepath.Join(vol, "wal_005", "00000005.history.lz4"), nil, 0o600)
	os.WriteFile(filepath.Join(vol, "wal_005", "00000006.history.lz4"), nil, 0o600)
	w.cluster(alone, 7)
	w.steps(w.runner(never, vol), 3)
	if w.archive.pushes != 0 || marker(t, vol) != "7" {
		t.Fatalf("several short restarts of its own: %d pushes, timeline %q", w.archive.pushes, marker(t, vol))
	}

	// The timeline is written down at the first look, not after a wait.
	w.cluster(alone, 8)
	w.steps(w.runner(never, vol), 1)
	if marker(t, vol) != "8" {
		t.Fatalf("at the first look: timeline %q", marker(t, vol))
	}
	// A timeline lower than the one written down is a volume of another
	// history: a base backup at once.
	w.archive.pushes = 0
	w.cluster(alone, 3)
	w.steps(w.runner(never, vol), 1)
	if w.archive.pushes != 1 || marker(t, vol) != "3" {
		t.Fatalf("a lower timeline: %d pushes, timeline %q", w.archive.pushes, marker(t, vol))
	}
}

func TestAVolumesPrimaryTakesTheScheduledBackupWhereTheLogIs(t *testing.T) {
	vol := t.TempDir()
	os.WriteFile(filepath.Join(vol, ".primary-timeline"), []byte("8\n"), 0o600)
	w := newWorld(t, "2026-11-20 10:00")
	w.cluster(three, 8)
	w.steps(w.runner(due, vol), 3)
	if w.archive.pushes != 1 || len(w.archive.retains) != 1 || w.archive.retains[0] != 7 {
		t.Fatalf("%d pushes, retains %v", w.archive.pushes, w.archive.retains)
	}
}

func TestABucketsBackupIsTakenByOneMemberOnce(t *testing.T) {
	// With standbys: the first standby takes it, not the primary.
	w := newWorld(t, "2026-11-20 10:00")
	w.cluster(three, 1)
	w.steps(w.runner(due, ""), 3)
	if w.archive.pushes != 0 {
		t.Fatalf("the primary took a standby's base backup: %d", w.archive.pushes)
	}
	// Alone: once, and not again after a restart the same day.
	w = newWorld(t, "2026-11-20 10:00")
	w.cluster(alone, 1)
	w.steps(w.runner(due, ""), 3)
	if w.archive.pushes != 1 {
		t.Fatalf("alone: %d pushes", w.archive.pushes)
	}
	w.steps(w.runner(due, ""), 3)
	if w.archive.pushes != 1 {
		t.Fatalf("a restart after the time took it again: %d pushes", w.archive.pushes)
	}
	// The next time the schedule names, it takes the next one.
	w.now = at("2026-11-21 00:00")
	w.steps(w.runner(due, ""), 1)
	if w.archive.pushes != 2 {
		t.Fatalf("the next day: %d pushes", w.archive.pushes)
	}
}

func TestAScheduleOfSeveralTimesADay(t *testing.T) {
	w := newWorld(t, "2026-11-20 05:59")
	w.cluster(alone, 1)
	r := w.runner("0 */6 * * *", "")
	w.steps(r, 1) // 00:00 of today, not taken yet
	if w.archive.pushes != 1 {
		t.Fatalf("at 05:59: %d pushes", w.archive.pushes)
	}
	w.steps(r, 10) // 06:00 comes in these five minutes
	if w.archive.pushes != 2 {
		t.Fatalf("after 06:00: %d pushes", w.archive.pushes)
	}
	w.steps(r, 10)
	if w.archive.pushes != 2 {
		t.Fatalf("until 12:00: %d pushes", w.archive.pushes)
	}
}

func TestATimeOfAnEarlierDayIsNotMadeUpAtAStart(t *testing.T) {
	// Not yet due today: yesterday's time, which no member took, waits for
	// the next, as with the script of before.
	w := newWorld(t, "2026-11-20 10:00")
	w.cluster(alone, 1)
	w.steps(w.runner(never, ""), 3)
	if w.archive.pushes != 0 {
		t.Fatalf("%d pushes", w.archive.pushes)
	}
}

func TestPatroniSilentAtTheTimeDoesNotGiveItUp(t *testing.T) {
	w := newWorld(t, "2026-11-20 10:00")
	r := w.runner(due, "")
	w.steps(r, 3)
	if w.archive.pushes != 0 || len(w.logs) != 1 {
		t.Fatalf("silent: %d pushes, logs %q", w.archive.pushes, w.logs)
	}
	w.cluster(alone, 1)
	w.steps(r, 1)
	if w.archive.pushes != 1 {
		t.Fatalf("once it answers: %d pushes", w.archive.pushes)
	}
}

func TestAFailedBackupIsTriedAgainInTenMinutes(t *testing.T) {
	w := newWorld(t, "2026-11-20 10:00")
	w.cluster(alone, 1)
	w.archive.fail = true
	r := w.runner(due, "")
	w.steps(r, 19) // nine and a half minutes
	if w.archive.pushes != 1 {
		t.Fatalf("within ten minutes: %d pushes", w.archive.pushes)
	}
	w.archive.fail = false
	w.steps(r, 2)
	if w.archive.pushes != 2 || len(w.archive.backups) != 1 {
		t.Fatalf("after ten minutes: %d pushes", w.archive.pushes)
	}
}

func TestWhoTakesItIsChosenFromPatronisMembers(t *testing.T) {
	ms := []member{
		{Name: "db-0", Role: "leader", State: "running", Timeline: []byte("5")},
		{Name: "db-2", Role: "replica", State: "streaming"},
		{Name: "db-1", Role: "sync_standby", State: "streaming"},
		{Name: "db-3", Role: "replica", State: "starting"},
	}
	if st := choose(ms, false); st != (State{Who: "db-1", Leader: "db-0", Timeline: 5}) {
		t.Fatalf("a bucket: %+v", st)
	}
	if st := choose(ms, true); st.Who != "db-0" {
		t.Fatalf("a volume: %+v", st)
	}
	if st := choose(ms[:1], false); st.Who != "db-0" {
		t.Fatalf("alone: %+v", st)
	}
	if st := choose(nil, false); st.Who != "-" || st.Leader != "-" {
		t.Fatalf("no primary: %+v", st)
	}
}
