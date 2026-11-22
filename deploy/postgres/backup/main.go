package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

// walg runs WAL-G. A run is ended with SIGTERM when the container is told
// to stop, and killed if it has not ended half a second later.
type walg struct{ pgdata string }

func (w walg) cmd(ctx context.Context, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, "wal-g", args...)
	c.Cancel = func() error { return c.Process.Signal(syscall.SIGTERM) }
	c.WaitDelay = 500 * time.Millisecond
	return c
}

func (w walg) Push(ctx context.Context) error {
	c := w.cmd(ctx, "backup-push", w.pgdata)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c.Run()
}

func (w walg) Retain(ctx context.Context, n int) error {
	c := w.cmd(ctx, "delete", "retain", "FULL", strconv.Itoa(n), "--confirm")
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c.Run()
}

func (w walg) Since(ctx context.Context, t time.Time) bool {
	c := w.cmd(ctx, "backup-list", "--detail", "--json")
	var out bytes.Buffer
	c.Stdout = &out
	if c.Run() != nil {
		return false
	}
	var list []struct {
		Start string `json:"start_time"`
		Time  string `json:"time"`
	}
	if json.Unmarshal(out.Bytes(), &list) != nil {
		return false
	}
	for _, b := range list {
		s := b.Start
		if s == "" {
			s = b.Time
		}
		if at, err := time.Parse(time.RFC3339Nano, s); err == nil && !at.Before(t) {
			return true
		}
	}
	return false
}

func logf(format string, args ...any) {
	fmt.Printf("%s backup: %s\n", time.Now().UTC().Format("2006-01-02T15:04:05Z"), fmt.Sprintf(format, args...))
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Println("221b-backup")
		return
	}
	if err := run(); err != nil {
		logf("%v", err)
		os.Exit(2)
	}
}

func run() error {
	pgdata := os.Getenv("PGDATA")
	if pgdata == "" {
		return fmt.Errorf("PGDATA is not set")
	}
	var (
		sched Schedule
		err   error
	)
	switch {
	case os.Getenv("DB_BACKUP_SCHEDULE") != "":
		sched, err = ParseSchedule(os.Getenv("DB_BACKUP_SCHEDULE"))
	case os.Getenv("DB_BACKUP_AT") != "":
		sched, err = FromDailyAt(os.Getenv("DB_BACKUP_AT"))
	default:
		sched, err = FromDailyAt("02:00")
	}
	if err != nil {
		return err
	}
	retain, err := strconv.Atoi(getenv("DB_BACKUP_RETAIN", "7"))
	if err != nil || retain < 1 {
		return fmt.Errorf("DB_BACKUP_RETAIN %q: give a number of 1 or more", os.Getenv("DB_BACKUP_RETAIN"))
	}
	poll, err := time.ParseDuration(getenv("DB_BACKUP_POLL", "30s"))
	if err != nil || poll <= 0 {
		return fmt.Errorf("DB_BACKUP_POLL %q: give a duration such as 30s", os.Getenv("DB_BACKUP_POLL"))
	}
	me := os.Getenv("PATRONI_NAME")
	if me == "" {
		me, _ = os.Hostname()
	}
	volume := os.Getenv("WALG_FILE_PREFIX")
	// WAL-G reaches PostgreSQL on its socket, as the superuser.
	os.Setenv("PGHOST", "/var/run/postgresql")
	os.Setenv("PGUSER", getenv("PATRONI_SUPERUSER_USERNAME", "postgres"))

	// The container's first process: a signal ends what runs and the loop
	// at once, so the pod does not take its whole grace period to stop.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	r := &Runner{
		Me: me, Schedule: sched, Retain: retain, Volume: volume,
		Cluster: patroni(getenv("DB_BACKUP_PATRONI", "http://127.0.0.1:8008/cluster"), volume),
		Archive: walg{pgdata: pgdata},
		Now:     time.Now,
		Log:     logf,
	}
	where := "the bucket"
	if volume != "" {
		where = "the volume of this member"
	}
	logf("a base backup on the schedule %q (UTC), keeping %d, to %s", sched, retain, where)
	// The first look is at once: a pod that lives less than the wait must
	// still write down the timeline it led.
	for {
		r.Step(ctx)
		select {
		case <-ctx.Done():
			logf("stopping")
			return nil
		case <-time.After(poll):
		}
	}
}
