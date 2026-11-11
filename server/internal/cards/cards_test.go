package cards

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/kv"
)

// syncBuffer collects the audit log, written from the store's goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// clock is a time the test moves.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeExec is a kubectl that reads a target whose state the test sets, and
// records what it applied, as whom.
type fakeExec struct {
	mu      sync.Mutex
	print   string
	unread  error
	applied []string
	as      []string
	fail    bool
}

func (f *fakeExec) Admin() bool { return false }

func (f *fakeExec) Check(a Action) error {
	if line, _ := a.Args["command"].(string); !strings.HasPrefix(line, "kubectl ") {
		return ErrInvalid
	}
	return nil
}

func (f *fakeExec) Describe(_ context.Context, _ string, a Action) (Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	line, _ := a.Args["command"].(string)
	if f.unread != nil {
		return Plan{Summary: line}, f.unread
	}
	return Plan{Summary: line, Targets: []string{"deployment shop/web"}, Impact: []string{"2 pods replaced"}, Fingerprint: f.print}, nil
}

func (f *fakeExec) Apply(_ context.Context, c Card, i int, by Approver) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	line, _ := c.Actions[i].Args["command"].(string)
	f.applied = append(f.applied, line)
	f.as = append(f.as, by.Name)
	if f.fail {
		return Result{Summary: "exit 1"}, errors.New("exit 1")
	}
	return Result{OK: true, Summary: "done, exit 0", Ref: "w1"}, nil
}

func (f *fakeExec) set(print string) {
	f.mu.Lock()
	f.print = print
	f.mu.Unlock()
}

func (f *fakeExec) runs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.applied...)
}

type world struct {
	st      *Store
	exec    *fakeExec
	clock   *clock
	audit   *syncBuffer
	backend kv.Backend
	// firing are the alerts still on, by key.
	mu     sync.Mutex
	firing map[string]bool
	prod   map[string]bool
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{exec: &fakeExec{print: "v1"}, clock: &clock{t: time.Date(2026, 11, 5, 3, 12, 0, 0, time.UTC)}, audit: &syncBuffer{}, backend: &kv.Memory{}, firing: map[string]bool{}, prod: map[string]bool{"prod-1": true}}
	w.st = w.open()
	return w
}

func (w *world) open() *Store {
	st := New(Options{
		Backend: w.backend,
		Now:     w.clock.Now,
		Audit:   audit.New(audit.Options{Out: w.audit}),
		Cluster: func(id string) (ClusterInfo, bool) {
			w.mu.Lock()
			defer w.mu.Unlock()
			switch id {
			case "prod-1", "dev-1":
				return ClusterInfo{Name: id, Production: w.prod[id], TwoPerson: w.prod[id]}, true
			}
			return ClusterInfo{}, false
		},
		Condition: func(_ context.Context, _ string, c Condition) (bool, string, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			key, _ := c.Args["key"].(string)
			if !w.firing[key] {
				return false, "the alert on " + key + " recovered", nil
			}
			return true, "", nil
		},
		ModelOf: func(string) string { return "claude-opus-5-5" },
		AgentName: func(id string) string {
			return map[string]string{"troubleshooter": "Troubleshooter", "mycroft": "Mycroft"}[id]
		},
	})
	st.Register(KindKubectl, w.exec)
	return st
}

func (w *world) fire(key string, on bool) {
	w.mu.Lock()
	w.firing[key] = on
	w.mu.Unlock()
}

func restart(cluster, dedup string) Proposal {
	return Proposal{
		Agent:     "troubleshooter",
		Source:    Source{Kind: SourceAlert, ID: "a1", Label: "Alert KubePodCrashLooping on shop/web-7f9 at 03:12", SetBy: "Sam"},
		Cluster:   cluster,
		Title:     "Restart shop/web",
		Reasoning: "web cannot sign in to its database since the Secret shop-db changed.",
		Evidence:  []Evidence{{Kind: "log", Text: "FATAL: password authentication failed", At: time.Date(2026, 11, 5, 3, 11, 0, 0, time.UTC)}},
		Actions:   []Action{{Kind: KindKubectl, Args: map[string]any{"command": "kubectl rollout restart deployment web -n shop"}, Impact: "2 pods replaced", Risk: "low"}},
		Condition: Condition{Kind: ConditionAlert, Args: map[string]any{"key": "pod:shop/web-7f9", "alert": "KubePodCrashLooping"}},
		DedupKey:  dedup,
	}
}

func person(key string) Approver {
	return Approver{Person: Person{Key: key, Name: strings.ToUpper(key[:1]) + key[1:]}}
}

func waitState(t *testing.T, st *Store, id, want string) Card {
	t.Helper()
	st.Wait()
	c, err := st.Get(context.Background(), id)
	if err != nil || c.State != want {
		t.Fatalf("proposal %s: %s (%v), want %s; %+v", id, c.State, err, want, c.Results)
	}
	return c
}

// A proposal is stored with what Watson read, survives a restart, and a
// newer one for the same thing replaces it.
func TestAProposalIsDurableAndANewerOneReplacesIt(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	id, err := w.st.Propose(ctx, restart("dev-1", "restart:dev-1/shop/web"))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := w.st.Get(ctx, id)
	if c.State != Queued || c.Needed != 1 || c.Model != "claude-opus-5-5" || len(c.Plans) != 1 || c.Plans[0].Fingerprint != "v1" || !c.Expires.Equal(w.clock.Now().Add(24*time.Hour)) {
		t.Fatalf("stored %+v", c)
	}
	// A new store on the same records: the console restarted.
	again := w.open()
	if c2, err := again.Get(ctx, id); err != nil || c2.Title != "Restart shop/web" {
		t.Fatalf("after a restart: %+v %v", c2, err)
	}
	newer, err := again.Propose(ctx, restart("dev-1", "restart:dev-1/shop/web"))
	if err != nil {
		t.Fatal(err)
	}
	old, _ := again.Get(ctx, id)
	if old.State != Replaced || old.ReplacedBy != newer {
		t.Fatalf("the older: %+v", old)
	}
	if n, _ := again.Get(ctx, newer); n.Replaces != id || n.State != Queued {
		t.Fatalf("the newer: %+v", n)
	}
	open, _ := again.List(ctx, Filter{State: Queued})
	if len(open) != 1 || open[0].ID != newer {
		t.Fatalf("open: %+v", open)
	}
	if _, err := again.Approve(ctx, id, person("pat"), ""); !errors.Is(err, ErrState) {
		t.Fatalf("approving the replaced one: %v", err)
	}
	if log := w.audit.String(); !strings.Contains(log, `"cards.replace"`) || !strings.Contains(log, `"cards.propose"`) || !strings.Contains(log, "Troubleshooter (claude-opus-5-5)") || !strings.Contains(log, "trigger set by Sam") {
		t.Fatalf("audit: %s", log)
	}
}

// A card expires after 24 hours, or as soon as its alert resolves; the
// expiry is in the audit log.
func TestACardExpiresAfter24HoursOrWhenItsAlertResolves(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.fire("pod:shop/web-7f9", true)
	first, _ := w.st.Propose(ctx, restart("dev-1", "a"))
	second, _ := w.st.Propose(ctx, restart("dev-1", "b"))
	w.st.Sweep(ctx)
	if c, _ := w.st.Get(ctx, first); c.State != Queued {
		t.Fatalf("swept early: %+v", c)
	}
	// The alert resolves: both expire.
	w.fire("pod:shop/web-7f9", false)
	w.st.Sweep(ctx)
	for _, id := range []string{first, second} {
		if c, _ := w.st.Get(ctx, id); c.State != Expired || !strings.Contains(c.Reason, "its alert resolved") {
			t.Fatalf("%s: %+v", id, c)
		}
	}
	// One with no condition expires after its 24 hours.
	p := restart("dev-1", "c")
	p.Condition = Condition{}
	third, _ := w.st.Propose(ctx, p)
	w.clock.add(23 * time.Hour)
	w.st.Sweep(ctx)
	if c, _ := w.st.Get(ctx, third); c.State != Queued {
		t.Fatalf("expired early: %+v", c)
	}
	w.clock.add(time.Hour)
	w.st.Sweep(ctx)
	if c, _ := w.st.Get(ctx, third); c.State != Expired {
		t.Fatalf("not expired: %+v", c)
	}
	if n := strings.Count(w.audit.String(), `"cards.expire"`); n != 3 {
		t.Fatalf("%d expiries audited: %s", n, w.audit.String())
	}
	// A proposal asking for more than 24 hours gets 24.
	p.Expires = w.clock.Now().Add(72 * time.Hour)
	id, _ := w.st.Propose(ctx, p)
	if c, _ := w.st.Get(ctx, id); !c.Expires.Equal(w.clock.Now().Add(24 * time.Hour)) {
		t.Fatalf("expires %v", c.Expires)
	}
}

// Approving checks the card again: a target changed since the proposal,
// or a condition no longer holding, refuses the approval and says why.
func TestApprovalRechecksTheConditionAndTheTarget(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.fire("pod:shop/web-7f9", true)
	id, _ := w.st.Propose(ctx, restart("dev-1", ""))
	w.exec.set("v2")
	c, err := w.st.Approve(ctx, id, person("pat"), "")
	if !errors.Is(err, ErrStale) || c.State != Queued || c.Recheck == nil || c.Recheck.OK || !strings.Contains(c.Recheck.Why, "changed since it was proposed") {
		t.Fatalf("a changed target: %v %+v", err, c.Recheck)
	}
	if len(w.exec.runs()) != 0 {
		t.Fatal("it ran")
	}
	w.exec.set("v1")
	w.fire("pod:shop/web-7f9", false)
	c, err = w.st.Approve(ctx, id, person("pat"), "")
	if !errors.Is(err, ErrStale) || !strings.Contains(c.Recheck.Why, "recovered") {
		t.Fatalf("a resolved alert: %v %+v", err, c.Recheck)
	}
	w.fire("pod:shop/web-7f9", true)
	if c, err = w.st.Recheck(ctx, id, Person{Key: "pat", Name: "Pat"}); err != nil || !c.Recheck.OK || len(c.Recheck.Checked) != 2 {
		t.Fatalf("recheck: %v %+v", err, c.Recheck)
	}
	if _, err := w.st.Approve(ctx, id, person("pat"), "looks right"); err != nil {
		t.Fatal(err)
	}
	c = waitState(t, w.st, id, Applied)
	if got := w.exec.runs(); len(got) != 1 || got[0] != "kubectl rollout restart deployment web -n shop" || c.Results[0].Ref != "w1" || c.Approvals[0].Comment != "looks right" {
		t.Fatalf("ran %v; %+v", got, c)
	}
	// What could not be read when proposed is never approved.
	w.exec.mu.Lock()
	w.exec.unread = errors.New("the cluster cannot be reached")
	w.exec.mu.Unlock()
	p := restart("dev-1", "")
	p.Condition = Condition{}
	id2, err := w.st.Propose(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	w.exec.mu.Lock()
	w.exec.unread = nil
	w.exec.mu.Unlock()
	if _, err := w.st.Approve(ctx, id2, person("pat"), ""); !errors.Is(err, ErrStale) {
		t.Fatalf("an unread target: %v", err)
	}
}

// On production, where changes need a second person, an agent's own
// proposal needs two people: an agent never counts as one. A person who
// asked for it in a chat counts, and may not approve it themselves.
func TestTwoPeopleApproveAnAgentsChangeOnProduction(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.fire("pod:shop/web-7f9", true)
	id, _ := w.st.Propose(ctx, restart("prod-1", ""))
	c, err := w.st.Approve(ctx, id, person("pat"), "")
	if err != nil || c.State != Queued || c.Needed != 2 || len(c.Approvals) != 1 {
		t.Fatalf("first approval: %v %+v", err, c)
	}
	if _, err := w.st.Approve(ctx, id, person("pat"), ""); !errors.Is(err, ErrState) {
		t.Fatalf("the same person again: %v", err)
	}
	if len(w.exec.runs()) != 0 {
		t.Fatal("ran on one approval")
	}
	if _, err := w.st.Approve(ctx, id, Approver{Person: Person{Name: "Anonymous"}}, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("sign-in off: %v", err)
	}
	if _, err := w.st.Approve(ctx, id, person("sam"), ""); err != nil {
		t.Fatal(err)
	}
	c = waitState(t, w.st, id, Applied)
	if w.exec.as[0] != "Sam" {
		t.Fatalf("ran as %v", w.exec.as)
	}
	if log := w.audit.String(); !strings.Contains(log, "approval 1 of 2") || !strings.Contains(log, "approval 2 of 2") || !strings.Contains(log, "approved by Pat, Sam") {
		t.Fatalf("audit: %s", log)
	}
	// Asked for in a chat by Pat: Pat is one of the two.
	p := restart("prod-1", "")
	p.Source = Source{Kind: SourceChat, Label: "Asked by Pat in a chat", SetBy: "Pat"}
	chat, err := w.st.ProposeFor(ctx, p, Person{Key: "pat", Name: "Pat"}, "")
	if err != nil || chat.Needed != 1 {
		t.Fatalf("a chat's: %v %+v", err, chat)
	}
	if _, err := w.st.Approve(ctx, chat.ID, person("pat"), ""); !errors.Is(err, ErrOwn) {
		t.Fatalf("approving one's own on production: %v", err)
	}
	if _, err := w.st.Approve(ctx, chat.ID, person("sam"), ""); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.st, chat.ID, Applied)
	// Off production one approval does, the asker's own included.
	p.Cluster = "dev-1"
	dev, _ := w.st.ProposeFor(ctx, p, Person{Key: "pat", Name: "Pat"}, "")
	if _, err := w.st.Approve(ctx, dev.ID, person("pat"), ""); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.st, dev.ID, Applied)
}

// A rejection says why, and a failed action fails the card with it.
func TestRejectionAndFailureAreRecorded(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.fire("pod:shop/web-7f9", true)
	id, _ := w.st.Propose(ctx, restart("dev-1", ""))
	if _, err := w.st.Reject(ctx, id, Person{Key: "pat", Name: "Pat"}, " "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no reason: %v", err)
	}
	c, err := w.st.Reject(ctx, id, Person{Key: "pat", Name: "Pat"}, "the database is migrating")
	if err != nil || c.State != Rejected || c.Rejection.Comment != "the database is migrating" {
		t.Fatalf("%v %+v", err, c)
	}
	w.exec.fail = true
	id, _ = w.st.Propose(ctx, restart("dev-1", ""))
	if _, err := w.st.Approve(ctx, id, person("pat"), ""); err != nil {
		t.Fatal(err)
	}
	c = waitState(t, w.st, id, Failed)
	if !strings.Contains(c.Reason, "action 1 of 1 failed") {
		t.Fatalf("%+v", c)
	}
	if log := w.audit.String(); !strings.Contains(log, `"cards.reject"`) || !strings.Contains(log, `"outcome":"failed"`) {
		t.Fatalf("audit: %s", log)
	}
}

// A card never carries a credential or a host key; free text is scrubbed.
func TestACardCarriesNoSecretAndNoHostKey(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.st.RegisterConsole(Console{Hosts: &fakeHosts{}})
	for name, args := range map[string]map[string]any{
		"a password":  {"name": "db-1", "address": "10.0.0.7", "password": "hunter2-long-secret"},
		"a host key":  {"name": "db-1", "address": "10.0.0.7", "hostKey": "x"},
		"a key value": {"name": "db-1", "address": "10.0.0.7", "labels": map[string]any{"k": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"}},
	} {
		p := restart("dev-1", "")
		p.Actions = []Action{{Kind: KindHostAdd, Args: args}}
		if _, err := w.st.Propose(ctx, p); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	p := restart("dev-1", "")
	p.Reasoning = "The log says password=Sup3rSecretValue and the key ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"
	p.Evidence = []Evidence{{Kind: "log", Text: "token: ghp_abcdefghijklmnopqrstuvwxyz0123456789"}}
	id, err := w.st.Propose(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := w.st.Get(ctx, id)
	for _, s := range []string{"Sup3rSecretValue", "AAAAC3NzaC1lZDI1NTE5", "ghp_abcdefghijklmnopqrstuvwxyz0123456789"} {
		if strings.Contains(c.Reasoning+c.Evidence[0].Text, s) || strings.Contains(w.audit.String(), s) {
			t.Errorf("%s kept: %q %q", s, c.Reasoning, c.Evidence[0].Text)
		}
	}
	// An unknown action, or a cluster not connected, is refused.
	p = restart("dev-1", "")
	p.Actions = []Action{{Kind: "shell", Args: map[string]any{"command": "rm -rf /"}}}
	if _, err := w.st.Propose(ctx, p); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown action: %v", err)
	}
	if _, err := w.st.Propose(ctx, restart("nowhere", "")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown cluster: %v", err)
	}
}

// A chat's card follows its Watson card: run there, it is applied;
// discarded there, withdrawn; lapsed there, it waits on.
func TestAChatsCardFollowsItsWatsonCard(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	p := restart("dev-1", "")
	p.Condition = Condition{}
	p.Source = Source{Kind: SourceChat, Label: "Asked by Pat in a chat", SetBy: "Pat"}
	pat := Person{Key: "pat", Name: "Pat"}
	ran, _ := w.st.ProposeFor(ctx, p, pat, "item-1")
	w.st.WatsonEnded(ctx, "item-1", "done", pat, 0)
	if c, _ := w.st.Get(ctx, ran.ID); c.State != Applied || c.Results[0].Ref != "item-1" {
		t.Fatalf("run in Watson: %+v", c)
	}
	gone, _ := w.st.ProposeFor(ctx, p, pat, "item-2")
	w.st.WatsonEnded(ctx, "item-2", "discarded", pat, -1)
	if c, _ := w.st.Get(ctx, gone.ID); c.State != Rejected || c.Rejection.By.Key != "pat" {
		t.Fatalf("discarded in Watson: %+v", c)
	}
	waits, _ := w.st.ProposeFor(ctx, p, pat, "item-3")
	w.st.WatsonEnded(ctx, "item-3", "lapsed", pat, -1)
	if c, _ := w.st.Get(ctx, waits.ID); c.State != Queued || c.WatsonItem != "" {
		t.Fatalf("lapsed in Watson: %+v", c)
	}
}

// Two people approve the same card at once: one approval counts and the
// change runs once; the other is refused with who approved it and when
// (Phase 12, IB-08). A late rejection or approval says the same.
func TestASecondApprovalIsRefusedWithWhoAndWhen(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.fire("pod:shop/web-7f9", true)
	id, _ := w.st.Propose(ctx, restart("dev-1", ""))
	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := make(chan struct{})
	for i, who := range []string{"pat", "sam"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = w.st.Approve(ctx, id, person(who), "")
		}()
	}
	close(start)
	wg.Wait()
	c := waitState(t, w.st, id, Applied)
	ok, refused := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrState) && strings.Contains(err.Error(), "already approved by "+c.Approvals[0].By.Name+" at 2026-11-05 03:12:00 UTC"):
			refused++
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	if ok != 1 || refused != 1 || len(c.Approvals) != 1 || len(w.exec.runs()) != 1 {
		t.Fatalf("ok %d refused %d approvals %+v runs %v", ok, refused, c.Approvals, w.exec.runs())
	}
	if _, err := w.st.Reject(ctx, id, Person{Key: "ann", Name: "Ann"}, "too late"); !errors.Is(err, ErrState) || !strings.Contains(err.Error(), "already approved by") || !strings.Contains(err.Error(), "it was applied") {
		t.Fatalf("a late rejection: %v", err)
	}
	id2, _ := w.st.Propose(ctx, restart("dev-1", ""))
	if _, err := w.st.Reject(ctx, id2, Person{Key: "ann", Name: "Ann"}, "not now"); err != nil {
		t.Fatal(err)
	}
	got, err := w.st.Approve(ctx, id2, person("pat"), "")
	if !errors.Is(err, ErrState) || err.Error() != ErrState.Error()+": already rejected by Ann at 2026-11-05 03:12:00 UTC" || got.ID != id2 || got.State != Rejected {
		t.Fatalf("approving a rejected card: %v %+v", err, got)
	}
}

func TestDecidedTextNamesEachEnding(t *testing.T) {
	at := time.Date(2026, 11, 6, 14, 3, 0, 0, time.UTC)
	for _, tc := range []struct {
		c    Card
		want string
	}{
		{Card{State: Approved, Approvals: []Decision{{By: Person{Name: "Ann"}, At: at}}}, "already approved by Ann at 2026-11-06 14:03:00 UTC"},
		{Card{State: Applied, Approvals: []Decision{{By: Person{Name: "Ann"}, At: at.Add(-time.Hour)}, {By: Person{Name: "Bo"}, At: at}}}, "already approved by Ann and Bo at 2026-11-06 14:03:00 UTC; it was applied"},
		{Card{State: Failed, Approvals: []Decision{{By: Person{Name: "rule"}, At: at, Standing: "s1"}}}, "already approved by a standing approval at 2026-11-06 14:03:00 UTC; it failed"},
		{Card{State: Rejected, Rejection: &Decision{By: Person{Name: "Cy"}, At: at}}, "already rejected by Cy at 2026-11-06 14:03:00 UTC"},
		{Card{State: Expired, Updated: at}, "it expired at 2026-11-06 14:03:00 UTC"},
		{Card{State: Replaced, ReplacedBy: "PC-9", Updated: at}, "a newer proposed change, PC-9, replaced it at 2026-11-06 14:03:00 UTC"},
	} {
		if got := DecidedText(tc.c); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.c.State, got, tc.want)
		}
	}
}

// A job's proposal keeps the job's name, trimmed, so the drawer says
// "Nightly audit, run-12" (CF-92).
func TestAProposalKeepsTheJobThatMadeIt(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	p := restart("dev-1", "restart:dev-1/shop/web")
	p.Source = Source{Kind: SourceJob, ID: "run-12", Label: "Every day at 02:00, UTC", SetBy: "Sam", Job: "  Nightly audit "}
	id, err := w.st.Propose(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := w.st.Get(ctx, id)
	if c.Source.Job != "Nightly audit" || c.Source.ID != "run-12" {
		t.Fatalf("source: %+v", c.Source)
	}
	b, _ := json.Marshal(c.Source)
	if !strings.Contains(string(b), `"job":"Nightly audit"`) {
		t.Fatalf("JSON: %s", b)
	}
}
