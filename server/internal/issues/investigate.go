package issues

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/scrub"
)

// Investigator looks into a report no known issue explains (IS-16). The
// AI Troubleshooter is the first; until it is plugged in, NoInvestigator
// finds nothing and the report waits for an operator.
type Investigator interface {
	// Investigate works out what a report is about and why.
	Investigate(ctx context.Context, r Report) (Finding, error)
	// Verify checks whether a report marked fixed is.
	Verify(ctx context.Context, r Report) (Verdict, error)
}

// Finding is what an investigation found. Every field may be empty.
type Finding struct {
	// Service is the service's ID, with how sure the investigator is.
	Service    string   `json:"service,omitempty"`
	Confidence int      `json:"confidence,omitempty"`
	Cause      string   `json:"cause,omitempty"`
	Evidence   []string `json:"evidence,omitempty"`
	// Technical is for the operators; Plain for the reporter, in their
	// words, naming nothing outside what they use (IS-22).
	Technical string `json:"technical,omitempty"`
	Plain     string `json:"plain,omitempty"`
	// Reply is a reply drafted for the reporter: an operator sends it, or
	// not (IS-14).
	Reply string `json:"reply,omitempty"`
	// Known proposes a known issue, which waits for an operator.
	Known *KnownInput `json:"known,omitempty"`
}

// Empty reports whether nothing was found.
func (f Finding) Empty() bool {
	return f.Service == "" && f.Cause == "" && len(f.Evidence) == 0 && f.Technical == "" && f.Plain == "" && f.Reply == "" && f.Known == nil
}

// Verdict is whether a fix holds.
type Verdict struct {
	Fixed    bool   `json:"fixed"`
	Evidence string `json:"evidence,omitempty"`
}

// ErrSkipped is an investigator that did not look (Phase 12: its job is
// paused, or its budget spent): nothing is noted, the report waits for an
// operator.
var ErrSkipped = errors.New("the investigator did not look")

// NoInvestigator finds nothing.
type NoInvestigator struct{}

func (NoInvestigator) Investigate(context.Context, Report) (Finding, error) { return Finding{}, nil }

func (NoInvestigator) Verify(context.Context, Report) (Verdict, error) { return Verdict{}, nil }

// investigateTime bounds one investigation.
const investigateTime = 10 * time.Minute

// investigate runs the investigator on a report in the background, at
// most a few at a time, and keeps what it finds.
func (s *Service) investigate(n int) {
	if _, none := s.inv.(NoInvestigator); none {
		return
	}
	go func() {
		s.investigating <- struct{}{}
		defer func() { <-s.investigating }()
		ctx, cancel := context.WithTimeout(context.Background(), investigateTime)
		defer cancel()
		st, err := s.load(ctx, true)
		if err != nil {
			s.log.Warn("a report could not be read for its investigation", "report", n, "err", err)
			return
		}
		r := st.report(n)
		if r == nil || !r.Status.Open() {
			return
		}
		f, err := s.inv.Investigate(ctx, *r)
		if errors.Is(err, ErrSkipped) {
			return
		}
		if err != nil {
			s.log.Warn("an investigation failed", "report", n, "err", err)
			return
		}
		if err := s.ApplyFinding(ctx, n, f); err != nil {
			s.log.Warn("an investigation's finding could not be kept", "report", n, "err", err)
		}
	}()
}

// ApplyFinding keeps what an investigation found on a report: the
// summaries, the cause and evidence, the service when none is known, a
// drafted reply, and a proposed known issue.
func (s *Service) ApplyFinding(ctx context.Context, n int, f Finding) error {
	if f.Empty() {
		return nil
	}
	agent := Person{ID: "system:investigator", Name: "Investigator", System: true}
	if f.Known != nil {
		k, err := s.Propose(ctx, SourceAgent, *f.Known)
		if err != nil {
			return err
		}
		f.Technical = strings.TrimSpace(f.Technical + "\n\nProposed known issue " + k.ID + ": " + k.Title)
	}
	return s.update(ctx, func(st *state, ch *change) (int, error) {
		r := st.report(n)
		if r == nil {
			return 0, nil
		}
		now := s.now()
		r.Technical, r.Plain, r.Cause = clip(f.Technical, MaxText), clip(f.Plain, MaxText), clip(f.Cause, MaxShort)
		r.Evidence = nil
		for _, e := range f.Evidence {
			if len(r.Evidence) < 20 {
				r.Evidence = append(r.Evidence, clip(e, MaxShort))
			}
		}
		if f.Service != "" && st.service(f.Service) != nil && (r.Service == "" || r.ServiceBy == "match" && f.Confidence > r.ServiceConfidence) {
			r.Service, r.ServiceConfidence, r.ServiceBy = f.Service, f.Confidence, "investigator"
		}
		// The investigator's findings leave the status as it was: a case is
		// in progress only once a person works on it (CF-45).
		if f.Reply != "" {
			_ = addMessage(r, Message{At: now, Kind: FromOperator, From: agent, Text: clip(f.Reply, MaxText), Draft: true, DraftBy: "agent"})
		}
		r.Updated = now
		ch.event(Event{Kind: EventReportTriaged, At: now, Report: copyReport(r), By: &agent})
		return saveReports, nil
	})
}

// verify asks the investigator whether a fix holds, and notes its answer
// for the operators.
func (s *Service) verify(n int) {
	if _, none := s.inv.(NoInvestigator); none {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), investigateTime)
		defer cancel()
		st, err := s.load(ctx, true)
		if err != nil {
			return
		}
		r := st.report(n)
		if r == nil {
			return
		}
		v, err := s.inv.Verify(ctx, *r)
		if errors.Is(err, ErrSkipped) {
			return
		}
		if err != nil {
			s.log.Warn("checking a fix failed", "report", n, "err", err)
			return
		}
		note := "The investigator could not confirm the fix."
		if v.Fixed {
			note = "The investigator confirms the fix."
		}
		if v.Evidence != "" {
			note += " " + clip(v.Evidence, MaxShort)
		}
		err = s.update(ctx, func(st *state, ch *change) (int, error) {
			r := st.report(n)
			if r == nil {
				return 0, nil
			}
			_ = addMessage(r, Message{At: s.now(), Kind: FromSystem, Internal: true, Text: note})
			return saveReports, nil
		})
		if err != nil {
			s.log.Warn("a fix's check could not be kept", "report", n, "err", err)
		}
	}()
}

// clip cuts what an investigator wrote to at most max characters, with
// anything that looks like a credential taken out.
func clip(s string, max int) string {
	s = scrub.Text(strings.TrimSpace(s))
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
