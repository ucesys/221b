package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/awsv4"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/s3"
	"github.com/ucesys/221b/server/internal/schedule"
)

// The audit log's actions of the scheduled backups.
const (
	ActionS3Settings = "backup.s3.settings"
	ActionS3Test     = "backup.s3.test"
	ActionS3Run      = "backup.s3.run"
	ActionS3Read     = "backup.s3.read"
)

// Grace is how late a run missed while the console was down may still be
// made: within it, once; past it, the schedule waits for its next time.
const Grace = time.Hour

// ScheduledBy names a scheduled run where a person would be.
const ScheduledBy = "scheduled backup"

// ErrRunning refuses a run while another is under way.
var ErrRunning = errors.New("a backup to S3 is running; wait for it to end")

// ErrOff is a run asked of a console with nothing configured.
var ErrOff = errors.New("backups to S3 are not set up: save a bucket first")

// Run is one backup to S3, as the page lists it.
type Run struct {
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitzero"`
	// Slot is the scheduled time the run is for; zero for Run now.
	Slot time.Time `json:"slot,omitzero"`
	By   string    `json:"by"`
	Key  string    `json:"key,omitempty"`
	Size int       `json:"size,omitempty"`
	OK   bool      `json:"ok"`
	// Missed is a scheduled time let go: the console was down past the
	// grace, or the previous backup was still running.
	Missed bool   `json:"missed,omitempty"`
	Error  string `json:"error,omitempty"`
	// Pruned counts the old backups the retention deleted.
	Pruned int `json:"pruned,omitempty"`
}

// Status is what the replicas share of the runs.
type Status struct {
	// Handled is the newest scheduled time run or let go.
	Handled time.Time `json:"handled,omitzero"`
	// Recent are the newest runs, newest first.
	Recent []Run `json:"recent,omitempty"`
	// LastOK is the newest run that worked.
	LastOK *Run `json:"lastOk,omitempty"`
}

// recentRuns is how many runs Status keeps.
const recentRuns = 12

// Options configure the scheduler.
type Options struct {
	Registry *Registry
	// Store keeps the settings and the status (the settings ConfigMap).
	Store kv.Backend
	// Secrets keeps the access key and the passphrase.
	Secrets SecretStore
	// Leads says whether this replica is the leader (ADR-70): only the
	// leader runs the schedule. nil: always.
	Leads func() bool
	Audit *audit.Log
	// Version is written into each backup.
	Version string
	// Identity is the pod's AWS identity, for CredentialsIdentity.
	Identity *awsv4.Identity
	Logger   *slog.Logger
	Now      func() time.Time
	// HTTP replaces the S3 client's (tests).
	HTTP *http.Client
	// Every is how often the schedule is looked at; 30 seconds.
	Every time.Duration
}

// Scheduler runs the backups to S3 on the leader, and serves the page.
type Scheduler struct {
	o       Options
	running atomic.Bool
	wg      sync.WaitGroup
}

// NewScheduler prepares the scheduled backups.
func NewScheduler(o Options) *Scheduler {
	if o.Registry == nil {
		o.Registry = Default
	}
	if o.Store == nil {
		o.Store = &kv.Memory{}
	}
	if o.Secrets == nil {
		o.Secrets = &MemorySecret{}
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Every <= 0 {
		o.Every = 30 * time.Second
	}
	if o.Identity == nil {
		o.Identity = &awsv4.Identity{Session: "221b-backup"}
	}
	return &Scheduler{o: o}
}

// Run looks at the schedule until ctx ends, and waits for a run under way.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(s.o.Every)
	defer t.Stop()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			s.wg.Wait()
			return
		case <-t.C:
		}
	}
}

// Wait waits for the runs under way.
func (s *Scheduler) Wait() { s.wg.Wait() }

// Running says whether a backup is under way on this replica.
func (s *Scheduler) Running() bool { return s.running.Load() }

// IdentityAvailable says whether the pod has an AWS identity of its own.
func (s *Scheduler) IdentityAvailable() bool { return s.o.Identity.Available() }

// Settings are the saved settings, or the defaults.
func (s *Scheduler) Settings(ctx context.Context) (S3Settings, bool, error) {
	b, _, err := s.o.Store.LoadKey(ctx, SettingsKey)
	if errors.Is(err, kv.ErrNotFound) {
		return DefaultSettings(), false, nil
	}
	if err != nil {
		return DefaultSettings(), false, err
	}
	set := DefaultSettings()
	if err := json.Unmarshal(b, &set); err != nil {
		return DefaultSettings(), false, fmt.Errorf("the saved settings of the backups to S3 cannot be read: %w", err)
	}
	return set, true, nil
}

// Keys says which of the Secret's keys are set.
func (s *Scheduler) Keys(ctx context.Context) (KeysState, error) {
	k, _, err := loadKeys(ctx, s.o.Secrets)
	return k.state(), err
}

// Save checks and saves the settings and the keys changed, and returns
// what it saved and what it changed, for the audit log. A changed schedule
// counts from now: a time already past is not made up.
func (s *Scheduler) Save(ctx context.Context, in S3Settings, kc KeysChange, by string) (S3Settings, string, error) {
	in = in.Normalize()
	if err := in.Validate(); err != nil {
		return in, "", err
	}
	keys, kversion, err := loadKeys(ctx, s.o.Secrets)
	if err != nil {
		return in, "", err
	}
	next := kc.apply(keys)
	if err := check(in, next); err != nil {
		return in, "", err
	}
	before, saved, err := s.Settings(ctx)
	if err != nil {
		return in, "", err
	}
	in.Since, in.By = before.Since, before.By
	if !saved || !in.Enabled || !before.Enabled || fmt.Sprint(in.Schedule) != fmt.Sprint(before.Schedule) {
		in.Since = s.o.Now().UTC()
	}
	in.By = by
	if next != keys {
		b, _ := json.Marshal(next)
		if err := s.o.Secrets.SaveKeyIf(ctx, SecretKey, b, kversion); err != nil {
			return in, "", fmt.Errorf("saving the backup's Secret: %w", err)
		}
	}
	b, _ := json.Marshal(in)
	if _, err := s.o.Store.SaveKey(ctx, SettingsKey, b, ""); err != nil {
		return in, "", err
	}
	return in, describeChange(before, in, saved, kc), nil
}

// describeChange is a settings change in words, for the audit log.
func describeChange(a, b S3Settings, saved bool, kc KeysChange) string {
	var parts []string
	add := func(what string, x, y any) {
		if fmt.Sprint(x) != fmt.Sprint(y) || !saved {
			parts = append(parts, fmt.Sprintf("%s %v", what, y))
		}
	}
	add("enabled", a.Enabled, b.Enabled)
	add("bucket", a.Bucket, b.Bucket)
	add("prefix", a.Prefix, b.Prefix)
	add("region", a.Region, b.Region)
	add("endpoint", a.Endpoint, b.Endpoint)
	add("path-style", a.PathStyle, b.PathStyle)
	add("private CA", a.CAPEM != "", b.CAPEM != "")
	add("credentials", a.Credentials, b.Credentials)
	if sa, sb := describeSchedule(a.Schedule), describeSchedule(b.Schedule); sa != sb || !saved {
		parts = append(parts, "schedule "+sb)
	}
	add("retention", a.Retention, b.Retention)
	add("server-side encryption", a.SSE+" "+a.KMSKeyID, b.SSE+" "+b.KMSKeyID)
	add("encrypted on the console", a.Encrypt, b.Encrypt)
	add("optional parts", a.Optional, b.Optional)
	parts = append(parts, kc.changed()...)
	if len(parts) == 0 {
		return "no change"
	}
	return strings.Join(parts, ", ")
}

func describeSchedule(sp schedule.Spec) string {
	sc, err := schedule.Parse(sp)
	if err != nil {
		return err.Error()
	}
	return sc.Describe()
}

// Status is the runs so far.
func (s *Scheduler) Status(ctx context.Context) (Status, error) {
	var st Status
	b, _, err := s.o.Store.LoadKey(ctx, StatusKey)
	if errors.Is(err, kv.ErrNotFound) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(b, &st)
}

// update changes the status over the version read, again when another
// replica wrote it first.
func (s *Scheduler) update(ctx context.Context, f func(*Status)) error {
	for try := 0; try < 8; try++ {
		var st Status
		b, version, err := s.o.Store.LoadKey(ctx, StatusKey)
		if err == nil {
			if err := json.Unmarshal(b, &st); err != nil {
				st = Status{}
			}
		} else if !errors.Is(err, kv.ErrNotFound) {
			return err
		}
		f(&st)
		if len(st.Recent) > recentRuns {
			st.Recent = st.Recent[:recentRuns]
		}
		out, _ := json.Marshal(st)
		if _, err = s.o.Store.SaveKey(ctx, StatusKey, out, version); errors.Is(err, kv.ErrConflict) {
			continue
		}
		return err
	}
	return errors.New("the backups' status kept changing")
}

// Next is when the schedule runs next, zero when it is off.
func (s *Scheduler) Next(set S3Settings) time.Time {
	if !set.Enabled {
		return time.Time{}
	}
	sc, err := schedule.Parse(set.Schedule)
	if err != nil {
		return time.Time{}
	}
	return sc.Next(s.o.Now())
}

// Tick looks at the schedule once: on the leader, a time come within the
// grace starts a run; one missed by more, or while a run is under way, is
// let go and noted.
func (s *Scheduler) Tick(ctx context.Context) {
	if s.o.Leads != nil && !s.o.Leads() {
		return
	}
	set, saved, err := s.Settings(ctx)
	if err != nil || !saved || !set.Enabled {
		return
	}
	sc, err := schedule.Parse(set.Schedule)
	if err != nil {
		return
	}
	st, err := s.Status(ctx)
	if err != nil {
		s.o.Logger.Warn("the backups' status cannot be read", "err", err)
		return
	}
	last := st.Handled
	if last.Before(set.Since) {
		last = set.Since
	}
	now := s.o.Now()
	due, run := sc.Due(last, now, Grace)
	if due.IsZero() {
		return
	}
	why := ""
	switch {
	case !run:
		why = fmt.Sprintf("missed: the console was not running at %s, and a run more than %s late is not made; the next is %s", due.In(sc.Location()).Format("2 Jan 15:04 MST"), Grace, sc.Next(now).In(sc.Location()).Format("2 Jan 15:04 MST"))
	case s.running.Load():
		why = "missed: the previous backup was still running"
	}
	// The time is claimed before the run starts, over the version read, so
	// a new leader, or an old one that has not yet seen it lost the Lease,
	// does not run it again.
	claimed := false
	if err := s.update(ctx, func(st *Status) {
		if claimed = st.Handled.Before(due); !claimed {
			return
		}
		st.Handled = due
		if why != "" {
			st.Recent = append([]Run{{Started: now.UTC(), Ended: now.UTC(), Slot: due, By: ScheduledBy, Missed: true, Error: why}}, st.Recent...)
		}
	}); err != nil || !claimed {
		if err != nil {
			s.o.Logger.Warn("the backups' status cannot be saved", "err", err)
		}
		return
	}
	who := audit.Who{Subject: "221b", Name: ScheduledBy}
	if why != "" {
		s.record(who, "", audit.OutcomeFailed, why)
		return
	}
	if !s.running.CompareAndSwap(false, true) {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.running.Store(false)
		rctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		s.backup(rctx, set, who, "", due)
	}()
}

// RunNow runs a backup at once, as who, and returns how it went.
func (s *Scheduler) RunNow(ctx context.Context, who audit.Who, from string) (Run, error) {
	set, saved, err := s.Settings(ctx)
	if err != nil {
		return Run{}, err
	}
	if !saved || set.Bucket == "" {
		return Run{}, ErrOff
	}
	if !s.running.CompareAndSwap(false, true) {
		return Run{}, ErrRunning
	}
	defer s.running.Store(false)
	return s.backup(ctx, set, who, from, time.Time{}), nil
}

func (s *Scheduler) record(who audit.Who, from, outcome, detail string) {
	if s.o.Audit != nil {
		s.o.Audit.Record(audit.Event{Action: ActionS3Run, Who: who, Target: "backup", Outcome: outcome, Detail: detail, From: from})
	}
}

// client is the S3 client for settings, with the Secret's keys or the
// pod's identity.
func (s *Scheduler) client(set S3Settings, keys Keys) (*s3.Client, error) {
	creds := func(ctx context.Context) (awsv4.Credentials, error) {
		if set.Credentials == CredentialsIdentity {
			return s.o.Identity.Credentials(ctx, set.Region)
		}
		if keys.AccessKeyID == "" || keys.SecretAccessKey == "" {
			return awsv4.Credentials{}, errors.New("no access key is saved: give one, or use the pod's identity")
		}
		return awsv4.Credentials{AccessKeyID: keys.AccessKeyID, SecretAccessKey: keys.SecretAccessKey}, nil
	}
	return s3.New(s3.Config{Endpoint: set.Endpoint, Region: set.Region, Bucket: set.Bucket, PathStyle: set.PathStyle, CAPEM: set.CAPEM, Credentials: creds, HTTP: s.o.HTTP, Now: s.o.Now})
}

// backup takes one backup and puts it in the bucket, prunes, and records
// the run: in the status and in the audit log.
func (s *Scheduler) backup(ctx context.Context, set S3Settings, who audit.Who, from string, slot time.Time) Run {
	by := who.Email
	if by == "" {
		by = who.Name
	}
	r := Run{Started: s.o.Now().UTC(), Slot: slot, By: by}
	err := func() error {
		keys, _, err := loadKeys(ctx, s.o.Secrets)
		if err != nil {
			return err
		}
		c, err := s.client(set, keys)
		if err != nil {
			return err
		}
		f, err := s.o.Registry.Export(ctx, &Env{By: by, Taken: r.Started, Version: s.o.Version, Optional: set.Optional})
		if err != nil {
			return err
		}
		body, err := f.Gzip()
		if err != nil {
			return err
		}
		r.Key = set.Prefix + f.Name()
		ctype := "application/gzip"
		if set.Encrypt {
			if body, err = Seal(body, keys.Passphrase); err != nil {
				return err
			}
			r.Key += SealedSuffix
			ctype = "application/octet-stream"
		}
		r.Size = len(body)
		if err := c.Put(ctx, r.Key, body, s3.PutOptions{ContentType: ctype, SSE: set.SSE, KMSKeyID: set.KMSKeyID}); err != nil {
			return fmt.Errorf("putting %s in %s: %w", r.Key, set.Bucket, err)
		}
		n, err := prune(ctx, c, set, r.Key, s.o.Now())
		r.Pruned = n
		if err != nil {
			return fmt.Errorf("the backup is in the bucket, but removing old ones failed: %w", err)
		}
		return nil
	}()
	r.Ended = s.o.Now().UTC()
	r.OK = err == nil
	outcome, detail := audit.OutcomeOK, fmt.Sprintf("put s3://%s/%s, %d bytes", set.Bucket, r.Key, r.Size)
	if r.Pruned > 0 {
		detail += fmt.Sprintf(", %d old backups removed", r.Pruned)
	}
	if err != nil {
		r.Error = err.Error()
		outcome, detail = audit.OutcomeFailed, "failed: "+r.Error
		s.o.Logger.Warn("the backup to S3 failed", "bucket", set.Bucket, "err", err)
	}
	if !slot.IsZero() {
		detail = "scheduled for " + slot.UTC().Format(time.RFC3339) + ": " + detail
	}
	// Recorded even when the run was cut short by a shutdown.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if uerr := s.update(sctx, func(st *Status) {
		st.Recent = append([]Run{r}, st.Recent...)
		if r.OK {
			rr := r
			st.LastOK = &rr
		}
	}); uerr != nil {
		s.o.Logger.Warn("the backups' status cannot be saved", "err", uerr)
	}
	s.record(who, from, outcome, detail)
	return r
}

// nameRE is a backup's name, as the console writes it.
var nameRE = regexp.MustCompile(`^221b-backup-(\d{4}-\d{2}-\d{2}-\d{6})\.json\.gz(\.enc)?$`)

// Listed is a backup in the bucket.
type Listed struct {
	s3.Object
	Taken     time.Time `json:"taken"`
	Encrypted bool      `json:"encrypted"`
}

// backups are the console's backups under the prefix, newest first;
// anything else there is left alone.
func backups(ctx context.Context, c *s3.Client, prefix string) ([]Listed, error) {
	objs, err := c.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	var out []Listed
	for _, o := range objs {
		m := nameRE.FindStringSubmatch(strings.TrimPrefix(o.Key, prefix))
		if m == nil {
			continue
		}
		taken, err := time.Parse("2006-01-02-150405", m[1])
		if err != nil {
			continue
		}
		out = append(out, Listed{Object: o, Taken: taken, Encrypted: m[2] != ""})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Taken.After(out[j].Taken) || out[i].Taken.Equal(out[j].Taken) && out[i].Key > out[j].Key
	})
	return out, nil
}

// prune deletes the backups the retention lets go: past the newest Count,
// or taken more than Days ago. The one just written always stays.
func prune(ctx context.Context, c *s3.Client, set S3Settings, keep string, now time.Time) (int, error) {
	if set.Retention.Mode == KeepLifecycle || set.Retention.Mode == "" {
		return 0, nil
	}
	list, err := backups(ctx, c, set.Prefix)
	if err != nil {
		return 0, err
	}
	n := 0
	for i, b := range list {
		old := false
		switch set.Retention.Mode {
		case KeepCount:
			old = i >= set.Retention.Count
		case KeepDays:
			old = now.Sub(b.Taken) > time.Duration(set.Retention.Days)*24*time.Hour
		}
		if !old || b.Key == keep {
			continue
		}
		if err := c.Delete(ctx, b.Key); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// List lists the backups in the bucket, newest first.
func (s *Scheduler) List(ctx context.Context) ([]Listed, error) {
	set, saved, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	if !saved {
		return nil, ErrOff
	}
	keys, _, err := loadKeys(ctx, s.o.Secrets)
	if err != nil {
		return nil, err
	}
	c, err := s.client(set, keys)
	if err != nil {
		return nil, err
	}
	return backups(ctx, c, set.Prefix)
}

// Fetch reads one of the backups in the bucket, opened when encrypted,
// for a restore.
func (s *Scheduler) Fetch(ctx context.Context, key string) (*File, error) {
	set, saved, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	if !saved {
		return nil, ErrOff
	}
	if !strings.HasPrefix(key, set.Prefix) || !nameRE.MatchString(strings.TrimPrefix(key, set.Prefix)) {
		return nil, errors.New("that is not one of the console's backups under the prefix")
	}
	keys, _, err := loadKeys(ctx, s.o.Secrets)
	if err != nil {
		return nil, err
	}
	c, err := s.client(set, keys)
	if err != nil {
		return nil, err
	}
	b, err := c.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return Read(b, func() (string, error) {
		if keys.Passphrase == "" {
			return "", errors.New("this backup is encrypted, and no passphrase is saved: save the one it was made with")
		}
		return keys.Passphrase, nil
	})
}

// Open reads an uploaded backup, opening it with the saved passphrase
// when it is encrypted.
func (s *Scheduler) Open(ctx context.Context, b []byte) (*File, error) {
	return Read(b, func() (string, error) {
		keys, _, err := loadKeys(ctx, s.o.Secrets)
		if err != nil {
			return "", err
		}
		if keys.Passphrase == "" {
			return "", errors.New("this backup is encrypted, and no passphrase is saved here: save the one it was made with on this page")
		}
		return keys.Passphrase, nil
	})
}

// Step is one step of the connection test.
type Step struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Test tries settings not yet saved, with the keys as they would be:
// credentials, then a small object put, read back with HEAD, and deleted,
// then a listing of the prefix. It stops at the first step that fails.
func (s *Scheduler) Test(ctx context.Context, in S3Settings, kc KeysChange) []Step {
	in = in.Normalize()
	if err := in.Validate(); err != nil {
		return []Step{{Name: "Settings", Detail: err.Error()}}
	}
	keys, _, err := loadKeys(ctx, s.o.Secrets)
	if err != nil {
		return []Step{{Name: "Settings", Detail: err.Error()}}
	}
	keys = kc.apply(keys)
	c, err := s.client(in, keys)
	if err != nil {
		return []Step{{Name: "Settings", Detail: err.Error()}}
	}
	var steps []Step
	step := func(name string, f func() (string, error)) bool {
		detail, err := f()
		st := Step{Name: name, OK: err == nil, Detail: detail}
		if err != nil {
			st.Detail = err.Error()
		}
		steps = append(steps, st)
		return err == nil
	}
	tag := make([]byte, 6)
	_, _ = rand.Read(tag)
	key := in.Prefix + ".221b-test-" + hex.EncodeToString(tag)
	body := []byte("221B checks that it may write here; this object is deleted at once.\n")
	_ = step("Credentials", func() (string, error) {
		if in.Credentials == CredentialsIdentity {
			if _, err := s.o.Identity.Credentials(ctx, in.Region); err != nil {
				return "", err
			}
			return "the pod's identity", nil
		}
		if keys.AccessKeyID == "" || keys.SecretAccessKey == "" {
			return "", errors.New("no access key: give its ID and secret")
		}
		return "the access key saved", nil
	}) && step("Put a test object", func() (string, error) {
		return key, c.Put(ctx, key, body, s3.PutOptions{ContentType: "text/plain", SSE: in.SSE, KMSKeyID: in.KMSKeyID})
	}) && step("Read it back", func() (string, error) {
		o, err := c.Head(ctx, key)
		if err == nil && o.Size != int64(len(body)) && o.Size >= 0 {
			err = fmt.Errorf("it reads back as %d bytes, not %d", o.Size, len(body))
		}
		return fmt.Sprintf("%d bytes", o.Size), err
	}) && step("Delete it", func() (string, error) {
		return "", c.Delete(ctx, key)
	}) && step("List the backups", func() (string, error) {
		list, err := backups(ctx, c, in.Prefix)
		return fmt.Sprintf("%d backups under %q", len(list), in.Prefix), err
	})
	return steps
}
