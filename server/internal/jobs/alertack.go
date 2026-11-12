package jobs

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/ucesys/221b/server/internal/audit"
)

// --- Alerts and cases (Phase 12, wave 6, CF-77 to CF-79) ---
//
// An alert diagnosis run that ended well and whose findings went to a
// case acknowledges its alert in the job's name, with the run and the
// case (CF-79): the alert leaves the banners, and the Alerts tab links
// the run and the case. The cases read which cases the alert runs went
// to, to show the case on an alert (CF-77), and the leader gives them the
// alerts it reads every look, for the safety net (CF-78).
//
// Since CF-82: a run that concluded on its alert acknowledges it, whether
// its findings reached a case, an exception covers them, or the agent
// found them expected (or below the job's lowest priority); and the
// safety net asks whether a run concluded on an alert as it is
// (AlertConcluded) before it opens a case. Since CF-84 a case opened from
// an alert names it by its reason (Named).

// CaseObject is the alert's object as the cases name objects (CF-05):
// "shop/Pod/web-7f9", "Node/worker-3".
func (a Alert) CaseObject() string {
	kind := map[string]string{"pod": "Pod", "node": "Node", "pvc": "PersistentVolumeClaim"}[a.Kind]
	if kind == "" {
		return a.Object()
	}
	if a.Namespace != "" && a.Kind != "node" {
		return a.Namespace + "/" + kind + "/" + a.Name
	}
	return kind + "/" + a.Name
}

// maxNamed is the most an alert's name by its reason keeps (CF-84).
const maxNamed = 120

// backOff is the kubelet's back-off message, without its times and names.
var backOff = regexp.MustCompile(`(?i)^back-off (?:\S+ )?(restarting failed container|pulling image)\b.*`)

// Named is the alert's name by its reason, short (CF-84): "Pod status
// Unknown: node r620-worker stopped reporting", "CrashLoopBackOff:
// back-off restarting failed container", "Node not ready: kubelet stopped
// reporting"; the status, or the kind, when nothing says why. The web's
// alertReason says it the same way.
func (a Alert) Named() string {
	status := strings.TrimSpace(a.Status)
	msg := reasonText(a.Message)
	var out string
	switch {
	case a.Kind == "pod" && strings.EqualFold(status, "Unknown"):
		out = "Pod status Unknown: its node stopped reporting"
		if a.Node != "" {
			out = "Pod status Unknown: node " + a.Node + " stopped reporting"
		}
	case a.Kind == "node" && (strings.EqualFold(status, "Unknown") || strings.EqualFold(status, "Not ready") || strings.EqualFold(status, "NotReady")):
		out = "Node not ready"
		if why := reasonText(a.Reason); why != "" && !strings.EqualFold(why, "not ready") {
			out += ": " + why
		} else if msg != "" {
			out += ": " + msg
		}
	default:
		head := firstNonEmpty(status, a.Reason, a.Kind)
		if a.Kind == "node" && a.Reason != "" {
			// A node's problem says more than its status (Ready, Cordoned).
			head = strings.TrimSpace(a.Reason)
		}
		switch {
		case msg != "" && !strings.EqualFold(msg, head):
			out = head + ": " + msg
		case a.Reason != "" && !strings.EqualFold(strings.TrimSpace(a.Reason), head):
			out = head + ": " + strings.TrimSpace(a.Reason)
		default:
			out = head
		}
	}
	return clip(out, maxNamed)
}

// reasonText is a message as an alert's name says it: its first sentence,
// without the kubelet's times and names, lower case first unless it
// starts with an acronym or a name, without its full stop.
func reasonText(msg string) string {
	t := strings.TrimSpace(msg)
	if i := strings.IndexAny(t, "\n"); i >= 0 {
		t = t[:i]
	}
	if i := strings.Index(t, ". "); i >= 0 {
		t = t[:i]
	}
	t = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), "."))
	if m := backOff.FindStringSubmatch(t); m != nil {
		t = "back-off " + strings.ToLower(m[1])
	}
	r := []rune(t)
	if len(r) > 1 && unicode.IsUpper(r[0]) && unicode.IsLower(r[1]) {
		r[0] = unicode.ToLower(r[0])
	}
	return string(r)
}

// AlertAck is a job's acknowledgement of the alert its run handled.
type AlertAck struct {
	Cluster string
	Key     string
	// Fingerprint is the alert as the run saw it.
	Fingerprint string
	// By is the job's name; Run its run, Case the case its findings went
	// to. Since CF-82: Exception is the exception that covers what it
	// found, and Expected says it diagnosed the alert as expected, Why
	// with the agent's reason (none for a finding below the job's lowest
	// priority).
	By        string
	Run       string
	Case      string
	Exception string
	Expected  bool
	Why       string
	At        time.Time
}

// Acker keeps the jobs' acknowledgements of alerts with the people's
// (package acks, in the settings). Ack says whether it was kept: a
// person's acknowledgement of the alert as it is stands.
type Acker interface {
	Ack(ctx context.Context, a AlertAck) (bool, error)
}

// SetAcker gives the service where the jobs' acknowledgements go.
func (s *Service) SetAcker(a Acker) { s.o.Acker = a }

// AlertWatcher is given, on the leader at each look (JB-11), the alerts
// there are; it calls alerts only when it wants them, and they are read
// once per look whoever asks.
type AlertWatcher func(ctx context.Context, alerts func() []Alert)

// SetAlertWatcher gives the service what else watches the alerts: the
// cases' safety net (CF-78).
func (s *Service) SetAlertWatcher(w AlertWatcher) { s.o.AlertWatcher = w }

// routedToCase says a finding went to a case: opened it, joined it,
// reopened it, or opened a regression.
func routedToCase(f Finding) bool {
	if f.Case == "" {
		return false
	}
	switch f.Routing {
	case RoutedOpened, RoutedSeen, RoutedReopened, RoutedRegression:
		return true
	}
	return false
}

// conclusion ranks what a finding concluded about its alert, best first
// (CF-82): it reached a case (opened, joined, reopened, a regression, or
// recorded on a known issue), an exception covers it, the agent found it
// expected, or it is below the job's lowest priority; -1 for a finding
// that concluded nothing (capped, invalid).
func conclusion(f Finding) int {
	switch f.Routing {
	case RoutedOpened, RoutedSeen, RoutedReopened, RoutedRegression, RoutedKnown:
		if f.Case != "" {
			return 0
		}
	case RoutedExcepted:
		if f.Exception != "" {
			return 1
		}
	case RoutedExpected:
		return 2
	case RoutedIgnored:
		return 3
	}
	return -1
}

// concluded is what an alert run concluded on its alert (CF-82): the
// finding that says it best, on the alert's object first; ok is false
// when the run did not end well, or its findings were only capped or
// invalid, or it had none.
func concluded(r Run, object string) (Finding, bool) {
	if r.Status != StatusOK {
		return Finding{}, false
	}
	best, rank, on := Finding{}, -1, false
	object = strings.ToLower(object)
	for _, f := range r.Findings {
		c := conclusion(f)
		if c < 0 {
			continue
		}
		here := f.Key != nil && strings.ToLower(f.Key.Object) == object
		if rank < 0 || c < rank || c == rank && here && !on {
			best, rank, on = f, c, here
		}
	}
	return best, rank >= 0
}

// ackAlert acknowledges the alert that started an alert run, in the job's
// name (CF-79), when the run ended well and concluded on it (CF-82): its
// findings went to a case (the case of the finding on the alert's object,
// or the first), an exception covers them, or they are expected or below
// the job's lowest priority. A run that failed, was stopped by its budget,
// or whose findings were only capped or invalid, or none, acknowledges
// nothing.
func (s *Service) ackAlert(ctx context.Context, j Job, r Run, ev *Event) {
	if s.o.Acker == nil || ev == nil || ev.Kind != EventAlert || r.Status != StatusOK {
		return
	}
	a, ok := ev.Data.(Alert)
	if !ok || a.Fingerprint == "" || a.Key == "" {
		return
	}
	f, ok := concluded(r, a.CaseObject())
	if !ok {
		return
	}
	in := AlertAck{Cluster: a.Cluster, Key: a.Key, Fingerprint: a.Fingerprint, By: r.Label, Run: r.ID, At: s.o.Now().UTC()}
	var what string
	switch f.Routing {
	case RoutedExcepted:
		in.Exception = f.Exception
		what = "what it found is covered by exception " + f.Exception
	case RoutedExpected:
		in.Expected, in.Why = true, clip(f.ExpectedWhy, 300)
		what = "diagnosed as expected: " + in.Why
	case RoutedIgnored:
		in.Expected = true
		what = "diagnosed as expected: what it found is below the job's lowest priority"
	default:
		in.Case = f.Case
		what = "its findings went to case " + f.Case
	}
	kept, err := s.o.Acker.Ack(ctx, in)
	if err != nil {
		s.o.Logger.Warn("a job could not acknowledge the alert it handled", "job", j.ID, "run", r.ID, "alert", a.Key, "err", err)
		s.record(audit.Event{Action: audit.ActionAlertAck, Who: audit.Who{Subject: "221b", Name: "job " + j.ID}, Cluster: a.Cluster, Target: a.Key, Outcome: audit.OutcomeFailed,
			Detail: fmt.Sprintf("%s: not acknowledged by %s (%s; %s): %v", a.Status, r.Label, r.ID, what, err)})
		return
	}
	if !kept {
		return
	}
	// Audited as a person's acknowledgement is, in the job's name.
	s.record(audit.Event{Action: audit.ActionAlertAck, Who: audit.Who{Subject: "221b", Name: "job " + j.ID}, Cluster: a.Cluster, Target: a.Key, Outcome: audit.OutcomeOK,
		Detail: fmt.Sprintf("%s: acknowledged by %s, run %s; %s", a.Status, r.Label, r.ID, what)})
}

// AlertConcluded says whether an alert diagnosis run concluded on an
// alert as it is now (CF-78, CF-82): a run on its key that saw it with
// this fingerprint, ended well and concluded (its findings reached a
// case, an exception covers them, or they are expected or below the job's
// lowest priority), within the day the job counts a repeat as the same
// alert (DedupWindow). It names the run.
func (s *Service) AlertConcluded(ctx context.Context, cluster, key, fingerprint string) (string, bool) {
	if fingerprint == "" {
		return "", false
	}
	rec, err := s.loadRuns(ctx)
	if err != nil {
		return "", false
	}
	now := s.o.Now()
	subject := "alert:" + cluster + "/" + key
	for _, r := range rec.Runs {
		if r.Kind != KindAlert || r.Subject != subject || r.Fingerprint != fingerprint {
			continue
		}
		seen := r.LastSeen
		if seen.IsZero() {
			seen = r.Started
		}
		if now.Sub(seen) >= DedupWindow {
			continue
		}
		if _, ok := concluded(r, ""); ok {
			return r.ID, true
		}
	}
	return "", false
}

// alertCasesFor is how long AlertCases keeps what it read.
const alertCasesFor = 10 * time.Second

// AlertCases are the cases the alert runs' findings went to, by the
// alert's key ("pod:shop/web-7f9"), on one cluster (CF-77): the newest
// run's first. Read from the runs kept, at most every few seconds.
func (s *Service) AlertCases(ctx context.Context, cluster string) map[string][]string {
	now := s.o.Now()
	s.mu.Lock()
	all, at := s.alertCases, s.alertCasesAt
	s.mu.Unlock()
	if all == nil || now.Sub(at) >= alertCasesFor || now.Before(at) {
		rec, err := s.loadRuns(ctx)
		if err != nil {
			return nil
		}
		all = map[string][]string{}
		for _, r := range rec.Runs {
			if r.Kind != KindAlert || !strings.HasPrefix(r.Subject, "alert:") {
				continue
			}
			sub := strings.TrimPrefix(r.Subject, "alert:")
			for _, f := range r.Findings {
				if routedToCase(f) && !slices.Contains(all[sub], f.Case) {
					all[sub] = append(all[sub], f.Case)
				}
			}
		}
		s.mu.Lock()
		s.alertCases, s.alertCasesAt = all, now
		s.mu.Unlock()
	}
	out := map[string][]string{}
	prefix := cluster + "/"
	for sub, ids := range all {
		if key, ok := strings.CutPrefix(sub, prefix); ok {
			out[key] = ids
		}
	}
	return out
}
