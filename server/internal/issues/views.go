package issues

import (
	"fmt"
	"time"

	"github.com/ucesys/221b/server/internal/scrub"
)

// What a caller is shown (IS-21, IS-22). An operator sees a report as it
// is kept. Anyone else (its reporter, a follower, a contact of its
// service) sees what the reporter may: the conversation without drafts or
// internal notes, the plain summary and never the technical one, the
// cause or the evidence, which name clusters, namespaces and workloads
// outside their scope. What they see is scrubbed again on the way out.

// SLA is how a report stands against its priority's targets (IS-05).
type SLA struct {
	// Target is the priority's targets, which a late case names (CU-06).
	Target       Target     `json:"target"`
	ResponseDue  *time.Time `json:"responseDue,omitempty"`
	ResolveDue   *time.Time `json:"resolveDue,omitempty"`
	ResponseLate bool       `json:"responseLate,omitempty"`
	ResolveLate  bool       `json:"resolveLate,omitempty"`
}

// slaOf works a case's targets out from when its clock started (CF-02):
// nil for a case without one, which shows "no SLA", never late.
func (set Settings) slaOf(r *Case, now time.Time) *SLA {
	if r.SLAFrom == nil {
		return nil
	}
	t := set.target(r.Priority)
	out := &SLA{Target: t}
	from := *r.SLAFrom
	if t.ResponseMinutes > 0 {
		due := from.Add(time.Duration(t.ResponseMinutes) * time.Minute)
		out.ResponseDue = &due
		out.ResponseLate = (r.FirstResponse == nil && now.After(due)) || (r.FirstResponse != nil && r.FirstResponse.After(due))
	}
	if t.ResolveMinutes > 0 {
		due := from.Add(time.Duration(t.ResolveMinutes) * time.Minute)
		out.ResolveDue = &due
		out.ResolveLate = (r.Resolved == nil && now.After(due)) || (r.Resolved != nil && r.Resolved.After(due))
	}
	return out
}

// KnownView is a known issue as a caller sees it.
type KnownView struct {
	KnownIssue
	// LastUpdate is the newest entry of the log.
	LastUpdate *Update `json:"lastUpdate,omitempty"`
	Following  bool    `json:"following"`
	// Linked are the cases closed as it, for operators; Cases the same
	// as ids ("C-12", CF-06).
	Linked []int    `json:"linked,omitempty"`
	Cases  []string `json:"cases,omitempty"`
}

// ReportView is a report as a caller sees it.
type ReportView struct {
	Case
	ServiceName string     `json:"serviceName,omitempty"`
	Known       *KnownView `json:"known,omitempty"`
	// Mine says the caller reported or filed it; Watching that they
	// follow it.
	Mine     bool `json:"mine"`
	Watching bool `json:"watching"`
	// CanReopen says "Still broken" would reopen it now.
	CanReopen bool `json:"canReopen"`
	SLA       *SLA `json:"sla,omitempty"`
}

// redactedPerson keeps a person's name, not their address.
func redactedPerson(p *Person) *Person {
	if p == nil {
		return nil
	}
	return &Person{ID: p.ID, Name: p.Name, System: p.System}
}

// forReporter strips a report to what its reporter may see.
func forReporter(r Case) Case {
	out := r
	out.Owner = redactedPerson(r.Owner)
	out.ServiceConfidence, out.ServiceBy, out.MatchConfidence, out.PriorityBy = 0, "", 0, ""
	out.Rejected, out.Similar, out.Watchers = nil, nil, nil
	out.Technical, out.Cause, out.Evidence = "", "", nil
	// What a case is about names clusters and objects outside their scope
	// (CU-01).
	out.Cluster, out.Object, out.Key, out.Job, out.Run, out.Finding = "", "", nil, "", "", ""
	out.Seen, out.Previous, out.ClosedBy, out.SLAFrom = nil, nil, redactedPerson(r.ClosedBy), nil
	out.OpenedBy = redactedPerson(r.OpenedBy)
	// The change that fixed it is the console's own (CF-29).
	out.AppliedBy, out.Verification = nil, ""
	// What the run that verified it read is the console's too (I-1).
	out.VerifiedBy, out.Unchecked, out.UncheckedWhy = nil, 0, ""
	// The incident number is support's, and the queue the console's
	// (CF-32, CF-36).
	out.Incident, out.AnalysisQueued = "", nil
	out.Plain = scrub.Text(r.Plain)
	out.Acknowledged, out.FirstResponse = nil, nil
	out.Messages = nil
	for _, m := range r.Messages {
		if m.Draft || m.Internal {
			continue
		}
		m.From = *redactedPerson(&m.From)
		m.SentBy = redactedPerson(m.SentBy)
		out.Messages = append(out.Messages, m)
	}
	return out
}

// knownView is a known issue as a caller sees it.
func (st *state) knownView(c Caller, k *KnownIssue) *KnownView {
	v := &KnownView{KnownIssue: *copyKnown(k), Following: k.followedBy(c.ID)}
	if u := k.LastUpdate(); u != nil {
		last := *u
		v.LastUpdate = &last
	}
	if c.Operator {
		for _, l := range st.index.Lines {
			if l.KnownIssue == k.ID {
				v.Linked = append(v.Linked, l.Number)
				v.Cases = append(v.Cases, fmt.Sprintf("C-%d", l.Number))
			}
		}
		v.Pending = k.Pending && k.Owner != nil && k.Owner.ID == c.ID
		return v
	}
	v.Owner = redactedPerson(k.Owner)
	v.Symptoms, v.Followers, v.Updates, v.Source = nil, nil, nil, ""
	v.Keys, v.VerifyJobs, v.Pending = nil, nil, false
	if v.LastUpdate != nil {
		v.LastUpdate.By = *redactedPerson(&v.LastUpdate.By)
	}
	return v
}

// reportView is a report as a caller sees it, with its service's name and
// its known issue as they stand.
func (st *state) reportView(c Caller, r *Case, now time.Time) ReportView {
	set := st.settings()
	v := ReportView{Case: *copyReport(r), Mine: isReporter(c, r), Watching: r.watchedBy(c.ID)}
	// A report's title is its first line until someone gives it one: kept
	// only then, so it goes with the text after the retention.
	if v.Title == "" {
		v.Title = titleOf(r.Text)
	}
	if svc := st.service(r.Service); svc != nil {
		v.ServiceName = svc.Name
	}
	if k := st.knownIssue(r.KnownIssue); k != nil && (!k.Draft || c.Operator) {
		v.Known = st.knownView(c, k)
	}
	if r.Status == StatusClosed {
		v.CanReopen = !r.Pruned && r.Closed != nil && (c.Operator || now.Sub(*r.Closed) <= time.Duration(set.ReopenDays)*24*time.Hour)
	}
	if c.Operator {
		v.SLA = set.slaOf(r, now)
		return v
	}
	v.Case = forReporter(v.Case)
	return v
}
