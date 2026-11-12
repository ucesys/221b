package backup

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/awsv4"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/s3"
	"github.com/ucesys/221b/server/internal/s3/s3test"
	"github.com/ucesys/221b/server/internal/schedule"
)

var s3Key = awsv4.Credentials{AccessKeyID: "AKIABACKUP", SecretAccessKey: "backup-secret"}

// clock is a time tests move.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Set(s string) {
	t, _ := time.Parse(time.RFC3339, s)
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

// rig is a scheduler over a fake bucket, with one part to back up.
type rig struct {
	fake    *s3test.Fake
	store   *kv.Memory
	secrets *MemorySecret
	clock   *clock
	log     *audit.Log
	events  *auditBuffer
}

type auditBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (a *auditBuffer) Write(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.b.Write(p)
}

func (a *auditBuffer) String() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.b.String()
}

func newRig(t *testing.T) *rig {
	t.Helper()
	f := s3test.New("backups", "eu-central-1", s3Key)
	t.Cleanup(f.Close)
	r := &rig{fake: f, store: &kv.Memory{}, secrets: &MemorySecret{}, clock: &clock{}, events: &auditBuffer{}}
	r.clock.Set("2026-11-05T10:00:00Z")
	f.Now = r.clock.Now
	r.log = audit.New(audit.Options{Out: r.events})
	return r
}

func (r *rig) scheduler(leads func() bool) *Scheduler {
	reg := NewRegistry()
	reg.Register(Part{Name: "notes", Label: "Notes", Export: func(context.Context, *Env) (any, error) { return []string{"hello"}, nil }})
	return NewScheduler(Options{Registry: reg, Store: r.store, Secrets: r.secrets, Leads: leads, Audit: r.log, Version: "0.12.0", Now: r.clock.Now, HTTP: r.fake.Client()})
}

func (r *rig) settings() S3Settings {
	return S3Settings{Enabled: true, Bucket: "backups", Prefix: "prod", Region: "eu-central-1", Endpoint: r.fake.URL, PathStyle: true, Credentials: CredentialsKeys,
		Schedule: schedule.Spec{Kind: schedule.Daily, At: "03:00", TimeZone: "Europe/Berlin"}, Retention: Retention{Mode: KeepCount, Count: 2}, SSE: s3.SSEKMS, KMSKeyID: "alias/221b"}
}

func ptr(s string) *string { return &s }

func (r *rig) save(t *testing.T, s *Scheduler, set S3Settings, kc KeysChange) {
	t.Helper()
	if _, _, err := s.Save(context.Background(), set, kc, "ops@corp.example"); err != nil {
		t.Fatal(err)
	}
}

// The keys are kept in the Secret, never with the settings; a setting that
// needs a key is refused without one.
func TestTheKeysStayInTheSecret(t *testing.T) {
	r := newRig(t)
	s := r.scheduler(nil)
	ctx := context.Background()
	if _, _, err := s.Save(ctx, r.settings(), KeysChange{}, "ops"); err == nil || !strings.Contains(err.Error(), "access key is needed") {
		t.Fatalf("enabled without a key: %v", err)
	}
	enc := r.settings()
	enc.Encrypt = true
	if _, _, err := s.Save(ctx, enc, KeysChange{AccessKeyID: ptr("AKIABACKUP"), SecretAccessKey: ptr("backup-secret"), Passphrase: ptr("short")}, "ops"); err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("a short passphrase: %v", err)
	}
	saved, change, err := s.Save(ctx, r.settings(), KeysChange{AccessKeyID: ptr("AKIABACKUP"), SecretAccessKey: ptr("backup-secret")}, "ops")
	if err != nil || saved.Prefix != "prod/" || !strings.Contains(change, "secret access key replaced") || strings.Contains(change, "backup-secret") || !strings.Contains(change, "schedule Every day at 03:00, Europe/Berlin") {
		t.Fatalf("%+v %q %v", saved, change, err)
	}
	b, _, _ := r.store.LoadKey(ctx, SettingsKey)
	if strings.Contains(string(b), "backup-secret") || strings.Contains(string(b), "AKIABACKUP") {
		t.Fatalf("the settings hold a key: %s", b)
	}
	if st, _ := s.Keys(ctx); !st.AccessKey || st.Passphrase {
		t.Fatalf("keys %+v", st)
	}
	// Saving again without keys keeps them.
	if _, change, err := s.Save(ctx, r.settings(), KeysChange{}, "ops"); err != nil || change != "no change" {
		t.Fatalf("%q %v", change, err)
	}
}

// Only the leader runs the schedule; it runs once when its time comes,
// not again on the next look or on another replica that becomes leader.
func TestTheScheduleRunsOnTheLeaderOnly(t *testing.T) {
	r := newRig(t)
	leads := false
	follower := r.scheduler(func() bool { return false })
	leader := r.scheduler(func() bool { return leads })
	r.save(t, leader, r.settings(), KeysChange{AccessKeyID: ptr("AKIABACKUP"), SecretAccessKey: ptr("backup-secret")})
	ctx := context.Background()
	r.clock.Set("2026-11-06T02:00:30Z") // 03:00:30 in Berlin
	follower.Tick(ctx)
	leader.Tick(ctx)
	leader.Wait()
	if n := len(r.fake.Objects()); n != 0 {
		t.Fatalf("a replica not leading ran: %d objects", n)
	}
	leads = true
	leader.Tick(ctx)
	leader.Wait()
	leader.Tick(ctx)
	leader.Wait()
	objs := r.fake.Objects()
	o, ok := objs["prod/221b-backup-2026-11-06-020030.json.gz"]
	if len(objs) != 1 || !ok || o.SSE != s3.SSEKMS || o.KMSKeyID != "alias/221b" {
		t.Fatalf("objects %v", objs)
	}
	f, err := Read(o.Body, nil)
	if err != nil || f.By != ScheduledBy || compact(f.Parts["notes"]) != `["hello"]` {
		t.Fatalf("the backup: %+v %v", f, err)
	}
	// Another replica takes over: the time is handled already.
	other := r.scheduler(nil)
	other.Tick(ctx)
	other.Wait()
	st, _ := other.Status(ctx)
	if len(r.fake.Objects()) != 1 || len(st.Recent) != 1 || !st.Recent[0].OK || st.LastOK == nil || st.Recent[0].Slot.IsZero() {
		t.Fatalf("status %+v", st)
	}
	if ev := r.events.String(); !strings.Contains(ev, `"action":"backup.s3.run"`) || !strings.Contains(ev, `"name":"scheduled backup"`) || !strings.Contains(ev, "put s3://backups/prod/221b-backup-2026-11-06-020030.json.gz") {
		t.Fatalf("the audit log: %s", ev)
	}
	// Two replicas that both think they lead, for a moment: one runs.
	r.clock.Set("2026-11-07T02:00:10Z")
	a, b := r.scheduler(nil), r.scheduler(nil)
	var wg sync.WaitGroup
	for _, sc := range []*Scheduler{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sc.Tick(ctx)
			sc.Wait()
		}()
	}
	wg.Wait()
	if n := len(r.fake.Objects()); n != 2 {
		t.Fatalf("two leaders made %d backups of one time", n-1)
	}
}

// A time missed while the console was down runs once within the hour,
// and is let go, noted and audited, past it.
func TestAMissedTime(t *testing.T) {
	r := newRig(t)
	s := r.scheduler(nil)
	ctx := context.Background()
	r.save(t, s, r.settings(), KeysChange{AccessKeyID: ptr("AKIABACKUP"), SecretAccessKey: ptr("backup-secret")})
	r.clock.Set("2026-11-06T02:50:00Z") // 50 minutes late
	s.Tick(ctx)
	s.Wait()
	if len(r.fake.Objects()) != 1 {
		t.Fatal("a time 50 minutes late was not run")
	}
	r.clock.Set("2026-11-07T04:30:00Z") // two and a half hours late
	s.Tick(ctx)
	s.Wait()
	st, _ := s.Status(ctx)
	if len(r.fake.Objects()) != 1 || len(st.Recent) != 2 || !st.Recent[0].Missed || !strings.Contains(st.Recent[0].Error, "not running at 7 Nov 03:00 CET") {
		t.Fatalf("status %+v", st.Recent)
	}
	if ev := r.events.String(); !strings.Contains(ev, `"outcome":"failed"`) || !strings.Contains(ev, "missed: the console was not running") {
		t.Fatalf("the audit log: %s", ev)
	}
}

// Retention: the newest N stay, or those younger than N days; what is not
// a backup of the console is left alone; the one just written always stays.
func TestRetention(t *testing.T) {
	r := newRig(t)
	s := r.scheduler(nil)
	ctx := context.Background()
	set := r.settings()
	r.save(t, s, set, KeysChange{AccessKeyID: ptr("AKIABACKUP"), SecretAccessKey: ptr("backup-secret")})
	day := time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		taken := day.AddDate(0, 0, i)
		r.fake.Set("prod/221b-backup-"+taken.Format("2006-01-02-150405")+".json.gz", []byte("x"), taken)
	}
	r.fake.Set("prod/notes.txt", []byte("mine"), day)
	r.fake.Set("other/221b-backup-2026-01-01-000000.json.gz", []byte("x"), day)
	run, err := s.RunNow(ctx, audit.Who{Name: "Ops", Email: "ops@corp.example"}, "10.0.0.1")
	if err != nil || !run.OK || run.Pruned != 3 {
		t.Fatalf("%+v %v", run, err)
	}
	var keys []string
	for k := range r.fake.Objects() {
		keys = append(keys, k)
	}
	if want := 4; len(keys) != want || r.fake.Objects()["prod/221b-backup-2026-11-04-020000.json.gz"].Body == nil || r.fake.Objects()["prod/notes.txt"].Body == nil {
		t.Fatalf("left: %v", keys)
	}
	// By age: keep three days, at 5 November 10:00.
	set.Retention = Retention{Mode: KeepDays, Days: 3}
	r.save(t, s, set, KeysChange{})
	for i := 0; i < 4; i++ {
		taken := day.AddDate(0, 0, i)
		r.fake.Set("prod/221b-backup-"+taken.Format("2006-01-02-150405")+".json.gz", []byte("x"), taken)
	}
	r.clock.Set("2026-11-05T10:00:01Z")
	run, err = s.RunNow(ctx, audit.Who{Name: "Ops"}, "")
	if err != nil || !run.OK || run.Pruned != 2 {
		t.Fatalf("by age: %+v %v", run, err)
	}
	set.Retention = Retention{Mode: KeepLifecycle}
	r.save(t, s, set, KeysChange{})
	if run, _ = s.RunNow(ctx, audit.Who{Name: "Ops"}, ""); run.Pruned != 0 {
		t.Fatalf("the lifecycle's: %+v", run)
	}
}

// With encryption on, the file is sealed on the console with the Secret's
// passphrase, named .enc, and a restore from the bucket opens it.
func TestEncryptedBackupsRestoreFromTheBucket(t *testing.T) {
	r := newRig(t)
	s := r.scheduler(nil)
	ctx := context.Background()
	set := r.settings()
	set.Encrypt, set.SSE = true, s3.SSES3
	r.save(t, s, set, KeysChange{AccessKeyID: ptr("AKIABACKUP"), SecretAccessKey: ptr("backup-secret"), Passphrase: ptr("a long passphrase indeed")})
	run, err := s.RunNow(ctx, audit.Who{Name: "Ops"}, "")
	if err != nil || !run.OK || !strings.HasSuffix(run.Key, ".json.gz.enc") {
		t.Fatalf("%+v %v", run, err)
	}
	o := r.fake.Objects()[run.Key]
	if !Sealed(o.Body) || strings.Contains(string(o.Body), "hello") || o.SSE != s3.SSES3 {
		t.Fatalf("the object: %q", o.Body[:40])
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 1 || !list[0].Encrypted || list[0].Key != run.Key {
		t.Fatalf("list %+v %v", list, err)
	}
	f, err := s.Fetch(ctx, run.Key)
	if err != nil || compact(f.Parts["notes"]) != `["hello"]` {
		t.Fatalf("fetch %v %v", f, err)
	}
	if _, err := s.Fetch(ctx, "prod/../secrets.txt"); err == nil {
		t.Fatal("fetched what is not a backup")
	}
	// An uploaded copy opens too; with another passphrase it does not.
	if f, err := s.Open(ctx, o.Body); err != nil || f.Format != Format {
		t.Fatalf("open %v", err)
	}
	r.save(t, s, set, KeysChange{Passphrase: ptr("another passphrase here")})
	if _, err := s.Fetch(ctx, run.Key); err == nil || !strings.Contains(err.Error(), "passphrase does not open") {
		t.Fatalf("another passphrase: %v", err)
	}
}

// Test connection: each step, and the first failure stops it; Run now is
// refused while a backup runs.
func TestTestConnectionAndRunNow(t *testing.T) {
	r := newRig(t)
	s := r.scheduler(nil)
	ctx := context.Background()
	set := r.settings()
	steps := s.Test(ctx, set, KeysChange{AccessKeyID: ptr("AKIABACKUP"), SecretAccessKey: ptr("backup-secret")})
	var names []string
	for _, st := range steps {
		if !st.OK {
			t.Fatalf("step %+v", st)
		}
		names = append(names, st.Name)
	}
	if strings.Join(names, ",") != "Credentials,Put a test object,Read it back,Delete it,List the backups" || len(r.fake.Objects()) != 0 || r.fake.Requests["PUT"] != 1 || r.fake.Requests["DELETE"] != 1 {
		t.Fatalf("%v %v %v", names, r.fake.Objects(), r.fake.Requests)
	}
	steps = s.Test(ctx, set, KeysChange{AccessKeyID: ptr("AKIABACKUP"), SecretAccessKey: ptr("wrong")})
	if len(steps) != 2 || steps[1].OK || !strings.Contains(steps[1].Detail, "SignatureDoesNotMatch") {
		t.Fatalf("a wrong secret: %+v", steps)
	}
	if steps := s.Test(ctx, S3Settings{Bucket: "B"}, KeysChange{}); len(steps) != 1 || steps[0].OK {
		t.Fatalf("bad settings: %+v", steps)
	}
	if _, err := s.RunNow(ctx, audit.Who{}, ""); err != ErrOff {
		t.Fatalf("nothing saved: %v", err)
	}
	r.save(t, s, set, KeysChange{AccessKeyID: ptr("AKIABACKUP"), SecretAccessKey: ptr("backup-secret")})
	s.running.Store(true)
	if _, err := s.RunNow(ctx, audit.Who{}, ""); err != ErrRunning {
		t.Fatalf("while running: %v", err)
	}
	// A scheduled time that comes while one runs is let go.
	r.clock.Set("2026-11-06T02:00:30Z")
	s.Tick(ctx)
	st, _ := s.Status(ctx)
	if len(st.Recent) != 1 || !st.Recent[0].Missed || !strings.Contains(st.Recent[0].Error, "still running") {
		t.Fatalf("%+v", st)
	}
	s.running.Store(false)
	// A failure is shown and audited.
	r.fake.Fail = func(req *http.Request) (int, string) {
		if req.Method == http.MethodPut {
			return http.StatusForbidden, "AccessDenied"
		}
		return 0, ""
	}
	run, err := s.RunNow(ctx, audit.Who{Name: "Ops"}, "")
	if err != nil || run.OK || !strings.Contains(run.Error, "403 AccessDenied") {
		t.Fatalf("%+v %v", run, err)
	}
	if ev := r.events.String(); !strings.Contains(ev, `"outcome":"failed","detail":"failed: putting`) && !strings.Contains(ev, "failed: putting") {
		t.Fatalf("the audit log: %s", ev)
	}
}

// The pod's identity signs when chosen.
func TestThePodsIdentity(t *testing.T) {
	r := newRig(t)
	s := r.scheduler(nil)
	env := map[string]string{"AWS_ACCESS_KEY_ID": "AKIABACKUP", "AWS_SECRET_ACCESS_KEY": "backup-secret"}
	s.o.Identity = &awsv4.Identity{Ambient: &awsv4.Ambient{Getenv: func(k string) string { return env[k] }}}
	set := r.settings()
	set.Credentials = CredentialsIdentity
	r.save(t, s, set, KeysChange{})
	if run, err := s.RunNow(context.Background(), audit.Who{Name: "Ops"}, ""); err != nil || !run.OK {
		t.Fatalf("%+v %v", run, err)
	}
	if !s.IdentityAvailable() {
		t.Fatal("the identity is not seen")
	}
	b, _ := json.Marshal(DefaultSettings())
	if err := DefaultSettings().Validate(); err == nil {
		t.Fatalf("the defaults have no bucket, yet validate: %s", b)
	}

}
