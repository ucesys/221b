// Command 221b-backup is the backup container of each pod of the bundled
// database (221b-postgres, DB-84). On the schedule (a cron expression in
// UTC), one member takes a base backup with WAL-G and removes the ones past
// the number kept. Which member is worked out from what Patroni says about
// the cluster, the same on every member. Nothing is run in another pod.
//
// With a bucket, every member writes to the one archive, so the standby
// whose name sorts first takes the backup and the primary is spared; alone,
// the primary takes it.
//
// With a volume (WALG_FILE_PREFIX), every member has a volume of its own and
// the write-ahead log goes to the primary's. A base backup is only of use
// next to the log that follows it, so there the primary takes it. A member
// that becomes the primary after another one was takes a base backup at
// once: its volume has no log for the time in between, so nothing older in
// it can be rolled forward to now. Whether another one was is read from the
// volume: the member that takes a timeline archives that timeline's history
// file, so a timeline whose history file is not here was another member's.
//
// It replaces the shell script of before (backup.sh) and does what it did,
// with a schedule that is more than a time of day.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// State is what Patroni says of the cluster that matters here: the member
// that takes the base backup, the primary, and the primary's timeline.
type State struct {
	Who, Leader string
	Timeline    int
}

// Archive is WAL-G, as far as the backups go.
type Archive interface {
	// Push takes a base backup of the data directory.
	Push(ctx context.Context) error
	// Retain removes the base backups past the newest n.
	Retain(ctx context.Context, n int) error
	// Since says whether the archive holds a base backup started at or
	// after t.
	Since(ctx context.Context, t time.Time) bool
}

// Runner is the loop's state and what it works with.
type Runner struct {
	Me       string
	Schedule Schedule
	Retain   int
	// Volume is the backups' volume (WALG_FILE_PREFIX), or empty for a
	// bucket.
	Volume  string
	Cluster func(ctx context.Context) (State, error)
	Archive Archive
	Now     func() time.Time
	Log     func(format string, args ...any)

	// done is the last scheduled time this member has dealt with: taken,
	// found taken, or left to another member.
	done time.Time
	// hold is when a failed base backup may be tried again.
	hold time.Time
	// silent says Patroni's silence was logged.
	silent bool
}

// Hold is how long a failed base backup waits before it is tried again.
const Hold = 10 * time.Minute

// Step looks once: at the cluster, then at the clock. It is what the loop
// does every 30 seconds.
func (r *Runner) Step(ctx context.Context) {
	st, err := r.Cluster(ctx)
	if err != nil {
		// Not an answer about who takes it, so the time is not marked as
		// dealt with.
		if !r.silent {
			r.Log("Patroni does not answer; trying again")
		}
		r.silent = true
		return
	}
	r.silent = false
	if r.Now().Before(r.hold) {
		return
	}
	if r.Volume != "" && st.Leader == r.Me && st.Timeline > 0 && !r.timeline(ctx, st.Timeline) {
		return
	}
	// Only a time of today (UTC) is taken late: a start does not make up
	// for a time of an earlier day that no member was running at, as the
	// script of before did not, and waits for the next one.
	now := r.Now().UTC()
	due, ok := r.Schedule.Last(now)
	if !ok || !due.After(r.done) || due.Before(now.Truncate(24*time.Hour)) {
		return
	}
	if st.Who != r.Me {
		r.Log("%s takes the base backup of %s", st.Who, due.Format(time.RFC3339))
		r.done = due
		return
	}
	// A restart after the time does not take its base backup a second time.
	if r.Archive.Since(ctx, due) {
		r.done = due
		return
	}
	if r.take(ctx, "") {
		r.done = due
	}
}

// timeline writes down the timeline this member leads on its volume, and
// takes a base backup at once when the volume has no log up to it. It
// says false when that base backup failed.
func (r *Runner) timeline(ctx context.Context, timeline int) bool {
	marker := filepath.Join(r.Volume, ".primary-timeline")
	seen := 0
	if b, err := os.ReadFile(marker); err == nil {
		seen, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	if seen == timeline {
		return true
	}
	// This member led the timeline written down. It leads this one. If it
	// took every timeline in between, its volume has all the log: its own
	// restarts, also ones too short to be written down. Otherwise it is
	// here for the first time, or another member led in between.
	if seen == 0 || timeline < seen || !r.led(seen+1, timeline-1) {
		if !r.take(ctx, fmt.Sprintf("this volume has no log up to timeline %d", timeline)) {
			return false
		}
		// It stands for the scheduled one due now, as the script's did
		// for the day's.
		if due, ok := r.Schedule.Last(r.Now()); ok {
			r.done = due
		}
	}
	if err := os.WriteFile(marker, []byte(strconv.Itoa(timeline)+"\n"), 0o600); err != nil {
		r.Log("writing down timeline %d: %v", timeline, err)
	}
	return true
}

// led says whether this member's volume has the history file of every
// timeline from a to b, both included: it was the primary through all of
// them.
func (r *Runner) led(a, b int) bool {
	for t := a; t <= b; t++ {
		m, _ := filepath.Glob(filepath.Join(r.Volume, "wal_005", fmt.Sprintf("%08X.history*", t)))
		if len(m) == 0 {
			return false
		}
	}
	return true
}

func (r *Runner) take(ctx context.Context, why string) bool {
	if why != "" {
		r.Log("taking a base backup: %s", why)
	} else {
		r.Log("taking a base backup")
	}
	if err := r.Archive.Push(ctx); err != nil {
		if ctx.Err() != nil {
			return false
		}
		r.Log("the base backup FAILED (%v); trying again in ten minutes", err)
		r.hold = r.Now().Add(Hold)
		return false
	}
	r.Log("the base backup is done")
	if err := r.Archive.Retain(ctx, r.Retain); err != nil && ctx.Err() == nil {
		r.Log("removing old backups failed: %v", err)
	}
	return true
}

// patroni reads the cluster from Patroni's REST API on this pod.
func patroni(url, volume string) func(ctx context.Context) (State, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	return func(ctx context.Context) (State, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return State{}, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return State{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return State{}, fmt.Errorf("patroni answered %s", resp.Status)
		}
		var c struct {
			Members []member `json:"members"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&c); err != nil {
			return State{}, err
		}
		return choose(c.Members, volume != ""), nil
	}
}

// member is one of Patroni's members.
type member struct {
	Name     string          `json:"name"`
	Role     string          `json:"role"`
	State    string          `json:"state"`
	Timeline json.RawMessage `json:"timeline"`
}

// choose picks who takes the base backup: to a volume the primary; to a
// bucket the standby whose name sorts first among those streaming or
// running, else the primary.
func choose(members []member, byVolume bool) State {
	var standbys []string
	st := State{Leader: "-"}
	for _, m := range members {
		if m.Role == "leader" {
			if st.Leader == "-" {
				st.Leader = m.Name
				st.Timeline, _ = strconv.Atoi(strings.TrimSpace(string(m.Timeline)))
			}
			continue
		}
		if m.State == "streaming" || m.State == "running" {
			standbys = append(standbys, m.Name)
		}
	}
	sort.Strings(standbys)
	st.Who = st.Leader
	if !byVolume && len(standbys) > 0 {
		st.Who = standbys[0]
	}
	return st
}
