package casework

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
)

// --- Alerts and cases (Phase 12, wave 6, CF-77, CF-78) ---
//
// An open case covers an alert when it is on the alert's cluster about
// the alert's object, or when the alert's own diagnosis run sent its
// findings to it. The Alerts tab shows the case in place of New Case
// (CF-77); and a critical alert still going after Settings › Cases'
// minutes that no case covers opens one by itself, once per occurrence
// (CF-78): the safety net for when the alert diagnosis job is off, failed
// or found nothing. Since CF-82 it leaves an alert a diagnosis run
// concluded on, as it is now: its findings reached a case, an exception
// covers them, or they are expected or below the job's lowest priority.

// AlertsPath serves the open cases that cover a cluster's alerts (CF-77).
const AlertsPath = "/api/v1/cases/alert-cases"

// CoverCase is an open case that covers an alert.
type CoverCase struct {
	ID       string `json:"id"`
	Number   int    `json:"number"`
	Title    string `json:"title"`
	Priority int    `json:"priority"`
}

// AlertCovers are the open cases that may cover a cluster's alerts, newest
// first: by the object they are about, lower case as the cases name
// objects ("shop/pod/web-1"), and by the key of the alert whose diagnosis
// run sent its findings to them ("pod:shop/web-1").
type AlertCovers struct {
	Objects map[string][]CoverCase `json:"objects"`
	Alerts  map[string][]CoverCase `json:"alerts"`
}

// For are the open cases that cover one alert, newest first.
func (c AlertCovers) For(key, object string) []CoverCase {
	var out []CoverCase
	for _, x := range append(append([]CoverCase(nil), c.Alerts[key]...), c.Objects[strings.ToLower(normObject(object))]...) {
		if !slices.ContainsFunc(out, func(y CoverCase) bool { return y.Number == x.Number }) {
			out = append(out, x)
		}
	}
	slices.SortFunc(out, func(a, b CoverCase) int { return b.Number - a.Number })
	return out
}

// alertRuns says which cases the alert runs' findings went to: the jobs'
// service (jobs.Service.AlertCases).
type alertRuns interface {
	AlertCases(ctx context.Context, cluster string) map[string][]string
}

// alertConclusions says whether an alert diagnosis run concluded on an
// alert as it is (CF-82): the jobs' service (jobs.Service.AlertConcluded).
type alertConclusions interface {
	AlertConcluded(ctx context.Context, cluster, key, fingerprint string) (string, bool)
}

// alertObjects are the kinds an alert is about: a pod, a node, a claim.
var alertObjects = map[string]bool{"Pod": true, "Node": true, "PersistentVolumeClaim": true}

// AlertCovers are the open cases that may cover a cluster's alerts
// (CF-77), from the cases' index and the alert runs kept.
func (s *Service) AlertCovers(ctx context.Context, cluster string) (AlertCovers, error) {
	lines, err := s.o.Issues.Index(ctx)
	if err != nil {
		return AlertCovers{}, err
	}
	out := AlertCovers{Objects: map[string][]CoverCase{}, Alerts: map[string][]CoverCase{}}
	open := map[int]CoverCase{}
	for _, l := range lines {
		if !l.Open() || l.Cluster != cluster {
			continue
		}
		c := CoverCase{ID: caseID(l.Number), Number: l.Number, Title: l.Title, Priority: l.Priority}
		open[l.Number] = c
		if o, err := jobs.NormalizeObject(l.Object); err == nil && alertObjects[o.Kind] {
			k := strings.ToLower(o.String())
			out.Objects[k] = append(out.Objects[k], c)
		}
	}
	if ar, ok := s.o.Jobs.(alertRuns); ok {
		for key, ids := range ar.AlertCases(ctx, cluster) {
			for _, id := range ids {
				if n, ok := number(id); ok {
					if c, ok := open[n]; ok && !slices.ContainsFunc(out.Alerts[key], func(y CoverCase) bool { return y.Number == n }) {
						out.Alerts[key] = append(out.Alerts[key], c)
					}
				}
			}
		}
	}
	for _, m := range []map[string][]CoverCase{out.Objects, out.Alerts} {
		for _, list := range m {
			slices.SortFunc(list, func(a, b CoverCase) int { return b.Number - a.Number })
		}
	}
	return out, nil
}

func (h *handler) alertCovers(w http.ResponseWriter, r *http.Request, c Caller) {
	cluster := strings.TrimSpace(r.URL.Query().Get("cluster"))
	if cluster == "" {
		fail(w, http.StatusBadRequest, "name the cluster")
		return
	}
	out, err := h.s.AlertCovers(r.Context(), cluster)
	if err != nil {
		failErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------- the safety net (CF-78) ----------

// alertNet is the leader's memory of the critical alerts, by cluster and
// key: since when each has been critical, and the case it opened for one.
// A new leader starts afresh; the covering check keeps it from opening a
// second case for an alert a case covers.
type alertNet struct {
	since  map[string]time.Time
	opened map[string]string
	looked time.Time
}

// netForget is how long without a look the memory is let go: this
// replica stopped leading in between.
const netForget = 5 * time.Minute

// netCaller and netWho are the console opening a case on its own
// (CF-78), as the cases and the audit log name it.
var (
	netCaller = issues.Caller{Person: issues.Person{ID: "system:alert-net", Name: "The console (alert safety net)", System: true}, Operator: true}
	netWho    = audit.Who{Subject: "221b", Name: "alert safety net"}
)

// WatchAlerts is the safety net (CF-78), given the alerts at each of the
// leader's looks (jobs.AlertWatcher): a critical alert still going, and
// not acknowledged by a person, after Settings › Cases' minutes opens a
// case like an alert's New Case, unless an open case covers it. One case
// per occurrence: not again while it goes on. Since CF-82 only when no
// diagnosis concluded on it as it is: a job acknowledged it (it does for
// what its run concluded on), or a run concluded on it; so a case a
// person closed while its alert goes on does not make it open another.
func (s *Service) WatchAlerts(ctx context.Context, read func() []jobs.Alert) {
	after := s.settings(ctx).AlertAfter()
	now := s.now()
	s.mu.Lock()
	if after == 0 || s.net.since == nil || now.Sub(s.net.looked) > netForget || now.Before(s.net.looked) {
		s.net = alertNet{since: map[string]time.Time{}, opened: map[string]string{}}
	}
	s.net.looked = now
	s.mu.Unlock()
	if after == 0 {
		return
	}
	crit := map[string]jobs.Alert{}
	for _, a := range read() {
		if a.Severity == jobs.SeverityError {
			crit[a.Cluster+"/"+a.Key] = a
		}
	}
	var due []string
	s.mu.Lock()
	for k := range s.net.since {
		if _, ok := crit[k]; !ok {
			delete(s.net.since, k)
			delete(s.net.opened, k)
		}
	}
	for k := range crit {
		since, ok := s.net.since[k]
		if !ok {
			s.net.since[k] = now
			continue
		}
		if _, done := s.net.opened[k]; !done && now.Sub(since) >= after {
			due = append(due, k)
		}
	}
	s.mu.Unlock()
	slices.Sort(due)
	covers := map[string]AlertCovers{}
	for _, k := range due {
		a := crit[k]
		c, ok := covers[a.Cluster]
		if !ok {
			var err error
			if c, err = s.AlertCovers(ctx, a.Cluster); err != nil {
				s.o.Logger.Warn("the cases that cover the alerts cannot be read", "cluster", a.Cluster, "err", err)
				continue
			}
			covers[a.Cluster] = c
		}
		if len(c.For(a.Key, a.CaseObject())) > 0 || s.concluded(ctx, a) {
			continue
		}
		id, err := s.openForAlert(ctx, a, after)
		if err != nil {
			s.o.Logger.Warn("the safety net could not open a case for a critical alert", "cluster", a.Cluster, "alert", a.Key, "err", err)
			continue
		}
		s.mu.Lock()
		s.net.opened[k] = id
		s.mu.Unlock()
		// The case covers it from now on, for the rest of this look too.
		delete(covers, a.Cluster)
	}
}

// concluded says whether a diagnosis concluded on an alert as it is now
// (CF-82): a job acknowledged it, which it does only for what its run
// concluded on, or the alert runs kept say one did.
func (s *Service) concluded(ctx context.Context, a jobs.Alert) bool {
	if a.JobAck {
		return true
	}
	if ac, ok := s.o.Jobs.(alertConclusions); ok {
		_, ok := ac.AlertConcluded(ctx, a.Cluster, a.Key, a.Fingerprint)
		return ok
	}
	return false
}

// openForAlert opens the safety net's case for an alert: as its New Case
// would, P0, an outage from the alert, opened by the console; named by
// the alert's reason (CF-84).
func (s *Service) openForAlert(ctx context.Context, a jobs.Alert, after time.Duration) (string, error) {
	name := a.Named()
	mins := int(after / time.Minute)
	in := NewCase{FromAlert: &AlertInput{Cluster: a.Cluster, Key: a.Key, Name: name, Severity: "critical", Object: a.CaseObject(), Summary: a.Message}}
	op := opener{issue: netCaller, actor: issues.ActorOf(netCaller.Person), who: netWho,
		opened: fmt.Sprintf("Opened by the console: the critical alert %s was still going after %d minutes and no case covered it", clip(name, 200), mins),
		what:   fmt.Sprintf("by the console from the alert %s, critical for %d minutes with no case covering it", clip(name, 200), mins)}
	info, analyse, err := s.open(ctx, in, op)
	if err != nil {
		return "", err
	}
	if analyse {
		s.startAnalysis(ctx, info.Number, op.actor)
		s.launch(info)
	}
	return info.ID, nil
}
