package backup

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
)

// Both repositories keep the same promises; each test runs against each.
func repos(t *testing.T) map[string]func(t *testing.T) Repo {
	return map[string]func(t *testing.T) Repo{
		"kv": func(*testing.T) Repo { return NewKVRepo(&kv.Memory{}) },
		"pg": func(t *testing.T) Repo { return NewPGRepo(dbtest.Open(t)) },
	}
}

func at(h, m int) time.Time { return time.Date(2026, 11, 18, h, m, 0, 0, time.UTC) }

func TestRepoSettings(t *testing.T) {
	for name, open := range repos(t) {
		t.Run(name, func(t *testing.T) {
			r, ctx := open(t), context.Background()
			if set, saved, err := r.Settings(ctx); err != nil || saved || !reflect.DeepEqual(set, DefaultSettings()) {
				t.Fatalf("nothing saved: %+v, saved %v, %v", set, saved, err)
			}
			in := DefaultSettings()
			in.Enabled, in.Bucket, in.Prefix, in.Since, in.By = true, "backups", "221b/", at(9, 0), "ada@corp.example"
			if err := r.SaveSettings(ctx, in); err != nil {
				t.Fatal(err)
			}
			in.Bucket = "backups-2"
			if err := r.SaveSettings(ctx, in); err != nil {
				t.Fatal(err)
			}
			if set, saved, err := r.Settings(ctx); err != nil || !saved || !reflect.DeepEqual(set, in) {
				t.Fatalf("read back %+v, saved %v, %v", set, saved, err)
			}
		})
	}
}

// A scheduled time is claimed once, by one replica; a run that ends is
// listed newest first; the newest that worked is kept apart.
func TestRepoRuns(t *testing.T) {
	for name, open := range repos(t) {
		t.Run(name, func(t *testing.T) {
			r, ctx := open(t), context.Background()
			if st, err := r.Status(ctx); err != nil || !st.Handled.IsZero() || st.Recent != nil || st.LastOK != nil {
				t.Fatalf("no runs yet: %+v %v", st, err)
			}
			var wg sync.WaitGroup
			var got atomic.Int64
			for range 6 {
				wg.Go(func() {
					ok, err := r.Claim(ctx, at(2, 0), nil)
					if err != nil {
						t.Errorf("claiming: %v", err)
					}
					if ok {
						got.Add(1)
					}
				})
			}
			wg.Wait()
			if got.Load() != 1 {
				t.Fatalf("%d replicas claimed 02:00", got.Load())
			}
			// Claimed and under way: handled, not yet listed.
			if st, _ := r.Status(ctx); !st.Handled.Equal(at(2, 0)) || len(st.Recent) != 0 {
				t.Fatalf("after the claim: %+v", st)
			}
			done := Run{Started: at(2, 0), Ended: at(2, 1), Slot: at(2, 0), By: ScheduledBy, Key: "221b/221b-backup-2026-11-18-020000.json.gz", Size: 4096, OK: true, Pruned: 2}
			if err := r.AddRun(ctx, done); err != nil {
				t.Fatal(err)
			}
			missed := Run{Started: at(3, 30), Ended: at(3, 30), Slot: at(3, 0), By: ScheduledBy, Missed: true, Error: "missed: the console was not running"}
			if ok, err := r.Claim(ctx, at(3, 0), &missed); err != nil || !ok {
				t.Fatalf("claiming 03:00 as missed: %v %v", ok, err)
			}
			if ok, err := r.Claim(ctx, at(2, 30), nil); err != nil || ok {
				t.Fatalf("a time before the newest handled was claimed: %v %v", ok, err)
			}
			manual := Run{Started: at(4, 0), Ended: at(4, 2), By: "ada@corp.example", Error: "the bucket refused the upload"}
			if err := r.AddRun(ctx, manual); err != nil {
				t.Fatal(err)
			}
			st, err := r.Status(ctx)
			if err != nil {
				t.Fatal(err)
			}
			want := Status{Handled: at(3, 0), Recent: []Run{manual, missed, done}, LastOK: &done}
			if !reflect.DeepEqual(st, want) {
				t.Fatalf("got  %+v\nwant %+v", st, want)
			}
		})
	}
}

// What the ConfigMap held is copied once: the settings, the runs and the
// newest time handled, so a time already run is not run again.
func TestPGRepoImport(t *testing.T) {
	ctx := context.Background()
	old := NewKVRepo(&kv.Memory{})
	set := DefaultSettings()
	set.Enabled, set.Bucket = true, "backups"
	if err := old.SaveSettings(ctx, set); err != nil {
		t.Fatal(err)
	}
	if ok, _ := old.Claim(ctx, at(2, 0), nil); !ok {
		t.Fatal("no claim")
	}
	done := Run{Started: at(2, 0), Ended: at(2, 1), Slot: at(2, 0), By: ScheduledBy, OK: true}
	if err := old.AddRun(ctx, done); err != nil {
		t.Fatal(err)
	}
	// 03:00 was claimed and its run never recorded: only the time is known.
	if ok, _ := old.Claim(ctx, at(3, 0), nil); !ok {
		t.Fatal("no claim")
	}
	r := NewPGRepo(dbtest.Open(t))
	if ran, err := r.Import(ctx, old, "test"); err != nil || !ran {
		t.Fatalf("first import: %v %v", ran, err)
	}
	if ran, err := r.Import(ctx, old, "test"); err != nil || ran {
		t.Fatalf("second import: %v %v", ran, err)
	}
	if got, saved, err := r.Settings(ctx); err != nil || !saved || got.Bucket != "backups" {
		t.Fatalf("settings %+v %v %v", got, saved, err)
	}
	st, err := r.Status(ctx)
	want := Status{Handled: at(3, 0), Recent: []Run{done}, LastOK: &done}
	if err != nil || !reflect.DeepEqual(st, want) {
		t.Fatalf("got  %+v\nwant %+v (%v)", st, want, err)
	}
	if ok, err := r.Claim(ctx, at(3, 0), nil); err != nil || ok {
		t.Fatalf("03:00 was claimed again after the import: %v %v", ok, err)
	}
}
