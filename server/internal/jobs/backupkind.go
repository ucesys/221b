package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/backup"
	"github.com/ucesys/221b/server/internal/schedule"
)

// BackupKind is the scheduled backups to S3 as a job (JB-02): listed in
// AIOps › Jobs with their schedule and last run, run now and paused from
// there, and set in Administration › Backup. The backups' scheduler keeps
// its own schedule (ADR-81); nothing here runs it twice.
func BackupKind(b *backup.Scheduler) Kind {
	return Kind{ID: KindBackup, Label: "Backup to S3", Order: 50,
		Does: "Every record of the console, secrets as placeholders, put in a bucket",
		External: &External{
			Where: "Administration › Backup",
			Link:  "#/admin/backup",
			State: func(ctx context.Context) (ExternalState, error) {
				set, saved, err := b.Settings(ctx)
				if err != nil {
					return ExternalState{}, err
				}
				st := ExternalState{Ready: saved && set.Bucket != "", On: saved && set.Enabled, SetBy: set.By, SetAt: set.Since, Running: b.Running(), Next: b.Next(set)}
				if set.Bucket != "" {
					st.Detail = "s3://" + set.Bucket + "/" + set.Prefix
				}
				if sc, err := schedule.Parse(set.Schedule); err == nil {
					st.When = sc.Describe()
					sp := set.Schedule
					st.Spec = &sp
				}
				if !st.Ready {
					st.When = "Not set up: save a bucket in Administration › Backup"
				}
				status, err := b.Status(ctx)
				if err != nil {
					return st, nil
				}
				if len(status.Recent) > 0 {
					r := status.Recent[0]
					st.LastAt, st.LastOK = r.Started, r.OK
					switch {
					case r.Missed:
						st.Last = r.Error
					case !r.OK:
						st.Last = "failed: " + r.Error
					default:
						st.Last = fmt.Sprintf("%d bytes", r.Size)
						if r.Pruned > 0 {
							st.Last += fmt.Sprintf(", %d old ones removed", r.Pruned)
						}
					}
				}
				if set.Retention.Mode == backup.KeepCount && set.Retention.Count > 0 {
					st.Last += fmt.Sprintf(", kept %d", set.Retention.Count)
				}
				return st, nil
			},
			RunNow: func(ctx context.Context, by Person) error {
				set, saved, err := b.Settings(ctx)
				if err != nil {
					return err
				}
				if !saved || set.Bucket == "" {
					return backup.ErrOff
				}
				if b.Running() {
					return backup.ErrRunning
				}
				go func() {
					rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Minute)
					defer cancel()
					_, _ = b.RunNow(rctx, audit.Who{Subject: by.Subject, Email: by.Email, Name: by.Name}, by.From)
				}()
				return nil
			},
			SetOn: func(ctx context.Context, on bool, by Person) error {
				set, saved, err := b.Settings(ctx)
				if err != nil {
					return err
				}
				if !saved || set.Bucket == "" {
					return errors.New("the backups to S3 are not set up: save a bucket in Administration › Backup first")
				}
				set.Enabled = on
				_, _, err = b.Save(ctx, set, backup.KeysChange{}, by.Label())
				return err
			},
		}}
}
