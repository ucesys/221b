package casework

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/jobs"
)

// --- One case for one failure (CF-117 to CF-119) ---
//
// A node that stops reporting makes every pod on it Unknown: one alert a
// pod, and before CF-117 one P0 case a pod from the safety net, beside
// the node's own. Now the safety net works the alert out to its node
// before it opens a case: the open case about that node takes the alert
// as its evidence (CF-117); with none, and the node down, it opens one
// case about the node and attaches every pod's alert on it. The case
// analysis says which open cases share a case's cause (CF-118), and the
// case then suggests merging it, which a person applies (CF-119): only
// the node match attaches by itself.

// signalWhyNode is why the safety net attaches a pod's alert to the case
// about its node.
const signalWhyNode = "same node"

// maxSameCause bounds the cases an analysis names as sharing the cause.
const maxSameCause = 10

// alertNode is the node an alert is about: a node's alert its own, a
// pod's the node it runs on, as the console's live view of the pods says
// (jobs.Alert.Node); "" for anything else. A claim's volume or a zone is
// not followed yet.
func alertNode(a jobs.Alert) string {
	switch a.Kind {
	case "node":
		return a.Name
	case "pod":
		return a.Node
	}
	return ""
}

// nodeDownStatus says whether a node's status is one of a node that
// stopped reporting or is not ready.
func nodeDownStatus(status string) bool {
	for _, s := range []string{"Unknown", "Not ready", "NotReady"} {
		if strings.EqualFold(strings.TrimSpace(status), s) {
			return true
		}
	}
	return false
}

// nodeDown is the alert of a node that is down, NotReady or Unknown, on a
// cluster, among the alerts now; false when there is none.
func nodeDown(all []jobs.Alert, cluster, node string) (jobs.Alert, bool) {
	for _, a := range all {
		if a.Cluster == cluster && a.Kind == "node" && a.Name == node && nodeDownStatus(a.Status) {
			return a, true
		}
	}
	return jobs.Alert{}, false
}

// Node is the open case about a node of the cluster: the one whose
// correlation key is the node's, else, when byObject, the newest about
// the node itself (Node/<name>).
func (c AlertCovers) Node(cluster, node string, byObject bool) (CoverCase, bool) {
	if list := c.Keys[issues.NodeKey(cluster, node)]; len(list) > 0 {
		return list[0], true
	}
	if list := c.Objects[strings.ToLower("node/"+node)]; byObject && len(list) > 0 {
		return list[0], true
	}
	return CoverCase{}, false
}

// correlate is the safety net's look at an alert's node before it opens a
// case for it (CF-117); true when it attached the alert, or opened the
// node's case, so the alert opens nothing of its own. A node's open case
// takes it by its correlation key; by its object too while the node is
// down or the pod Unknown (its node stopped reporting), so a pod failing
// on a healthy node with an unrelated case open keeps its own case.
func (s *Service) correlate(ctx context.Context, a jobs.Alert, cov AlertCovers, all []jobs.Alert, crit map[string]jobs.Alert, after time.Duration) bool {
	node := alertNode(a)
	if node == "" {
		return false
	}
	na, down := nodeDown(all, a.Cluster, node)
	unknown := a.Kind == "pod" && strings.EqualFold(a.Status, "Unknown")
	if t, ok := cov.Node(a.Cluster, node, down || unknown); ok {
		if t.Number == 0 {
			return false
		}
		if err := s.attachAlert(ctx, t.Number, a, signalWhyNode); err != nil {
			s.o.Logger.Warn("the safety net could not attach an alert to its node's case", "cluster", a.Cluster, "alert", a.Key, "case", t.ID, "err", err)
			return true
		}
		s.netOpened(a, t.ID)
		return true
	}
	if !down {
		return false
	}
	// One case about the node, and every pod's alert on it attached.
	id, err := s.openForAlert(ctx, na, after, issues.NodeKey(a.Cluster, node))
	if err != nil {
		s.o.Logger.Warn("the safety net could not open a case for a node", "cluster", a.Cluster, "node", node, "err", err)
		return true
	}
	s.netOpened(na, id)
	n, _ := number(id)
	var pods []jobs.Alert
	for _, p := range crit {
		if p.Cluster == a.Cluster && p.Kind == "pod" && p.Node == node && len(cov.For(p.Key, p.CaseObject())) == 0 {
			pods = append(pods, p)
		}
	}
	slices.SortFunc(pods, func(x, y jobs.Alert) int { return strings.Compare(x.Key, y.Key) })
	for _, p := range pods {
		if err := s.attachAlert(ctx, n, p, signalWhyNode); err != nil {
			s.o.Logger.Warn("the safety net could not attach an alert to its node's case", "cluster", p.Cluster, "alert", p.Key, "case", id, "err", err)
			continue
		}
		s.netOpened(p, id)
	}
	return true
}

// netOpened marks an alert as handled in this occurrence: its case now
// covers it.
func (s *Service) netOpened(a jobs.Alert, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.net.opened != nil {
		s.net.opened[a.Cluster+"/"+a.Key] = id
	}
}

// netDone says whether an alert was handled in this occurrence.
func (s *Service) netDone(a jobs.Alert) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.net.opened[a.Cluster+"/"+a.Key]
	return ok
}

// attachAlert attaches an alert to an open case as its evidence (CF-117):
// a row of the case's signals, its seen count up one, a line of its
// history ("Alert … attached: same node") and the audit log. An alert it
// holds already is not attached again.
func (s *Service) attachAlert(ctx context.Context, n int, a jobs.Alert, why string) error {
	by := issues.ActorOf(netCaller.Person)
	attached := false
	r, err := s.apply(ctx, n, by, func(tx *issues.Tx, r *issues.Case) error {
		if !r.Status.Open() {
			return fmt.Errorf("%w: %s is closed", ErrState, caseID(n))
		}
		attached = issues.AttachSignal(r, issues.Signal{Cluster: a.Cluster, Key: a.Key, Name: a.Named(), Object: a.CaseObject(),
			ByKey: netCaller.ID, By: netCaller.Name, Why: why}, tx.Now())
		return nil
	})
	if err != nil || !attached {
		return err
	}
	name := clip(a.Named(), 200) + bracketed(a.CaseObject())
	s.note(ctx, n, Event{At: s.now(), What: fmt.Sprintf("Alert %s attached: %s", name, why), By: by})
	s.record(audit.Event{Action: ActionAttach, Who: netWho, Cluster: r.Cluster, Target: caseID(n), Outcome: audit.OutcomeOK,
		Detail: fmt.Sprintf("the alert %s attached to case %s: %s", name, caseID(n), why)})
	return nil
}

// ---------- the analysis's "same cause as" (CF-118) ----------

// sameCause are the cases an analysis names as sharing a case's cause,
// kept only when they are open on the case's cluster, and the root it
// names among them or this case; as ids ("C-13").
func (s *Service) sameCause(ctx context.Context, c caseInfo, refs []string, root string) ([]string, string) {
	if len(refs) == 0 && strings.TrimSpace(root) == "" {
		return nil, ""
	}
	lines, err := s.o.Issues.Index(ctx)
	if err != nil {
		return nil, ""
	}
	open := map[int]bool{}
	for _, l := range lines {
		if l.Open() && l.Cluster == c.Cluster && l.Number != c.Number {
			open[l.Number] = true
		}
	}
	var out []string
	add := func(n int) {
		if open[n] && !slices.Contains(out, caseID(n)) && len(out) < maxSameCause {
			out = append(out, caseID(n))
		}
	}
	for _, ref := range refs {
		if n, ok := number(ref); ok {
			add(n)
		}
	}
	rootID := ""
	if n, ok := number(root); ok {
		switch {
		case n == c.Number && len(out) > 0:
			rootID = caseID(n)
		case open[n]:
			add(n)
			if slices.Contains(out, caseID(n)) {
				rootID = caseID(n)
			}
		}
	}
	return out, rootID
}

// mergeSuggestion is the case an open case's analysis suggests merging it
// into (CF-118): the root it named, when that is another case, else the
// first case it said shares the cause; only while that case is open and
// this one is. None when the case is the root itself.
func mergeSuggestion(r issues.Case, a *Analysis, lines []issues.Line) *CaseRef {
	if a == nil || !r.Status.Open() || len(a.SameCauseAs) == 0 {
		return nil
	}
	target := a.RootCase
	if n, ok := number(target); ok && n == r.Number {
		return nil
	}
	if target == "" {
		target = a.SameCauseAs[0]
	}
	n, ok := number(target)
	if !ok {
		return nil
	}
	for _, l := range lines {
		if l.Number == n && l.Open() {
			return &CaseRef{ID: caseID(n), Number: n, Title: l.Title, Status: l.Status, Source: l.Source}
		}
	}
	return nil
}

// ---------- Merge into case… (CF-119) ----------

// MergeInput names the case to merge into, with an optional reason.
type MergeInput struct {
	Into        string `json:"into"`
	Explanation string `json:"explanation,omitempty"`
}

// Merge merges an open case into another open case (CF-119), by hand or
// from the analysis's suggestion: the case closes as a duplicate of it,
// "Merged into C-n", pointing at it; its alerts move there, its own
// alert too when an alert opened it, each counted seen on the target;
// both histories say so, and its conversation with Sherlock stays on it,
// linked from the target's history. Its SLA and its place in the Inbox
// end with it: the target's stand. A data operation of the console,
// audited (case.merge); whoever may close a case may merge it.
func (s *Service) Merge(ctx context.Context, id string, in MergeInput, c Caller) (View, error) {
	n, _, err := s.target(ctx, c, id)
	if err != nil {
		return View{}, err
	}
	t, ok := number(in.Into)
	if !ok {
		return View{}, fmt.Errorf("%w: name the case to merge into, C-<n>", ErrInvalid)
	}
	if t == n {
		return View{}, fmt.Errorf("%w: a case is not merged into itself", ErrInvalid)
	}
	if _, _, err := s.target(ctx, c, caseID(t)); err != nil {
		return View{}, err
	}
	w, _, err := s.loadWork(ctx, n)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return View{}, err
	}
	explanation := clip(strings.TrimSpace(in.Explanation), MaxDetail)
	text := "Merged into " + caseID(t)
	if explanation != "" {
		text += ": " + explanation
	}
	p := c.issue().Person
	var src, dst issues.Case
	moved := 0
	err = s.o.Issues.Batch(ctx, func(tx *issues.Tx) error {
		tx.As(c.actor())
		r, err := tx.Case(n)
		if err != nil {
			return err
		}
		tg, err := tx.Case(t)
		if err != nil {
			return err
		}
		if err := r.MayClose(issues.OutcomeDuplicate); err != nil {
			return err
		}
		var own *issues.Signal
		if w.Source.AlertKey != "" && r.Cluster != "" {
			own = &issues.Signal{Cluster: r.Cluster, Key: w.Source.AlertKey, Name: w.Source.Alert, Object: r.Object}
		}
		before := len(tg.Signals)
		if err := tx.Touch(r, func() error {
			return tx.Touch(tg, func() error { return tx.Merge(r, tg, p, text, own) })
		}); err != nil {
			return err
		}
		moved = len(tg.Signals) - before
		tx.Changed(r, &p)
		tx.Changed(tg, &p)
		src, dst = *r, *tg
		return nil
	})
	if err != nil {
		return View{}, s.fromIssues(err)
	}
	now := s.now()
	alerts := fmt.Sprintf("%d %s", moved, plural(moved, "alert", "alerts"))
	s.note(ctx, n, Event{At: now, What: fmt.Sprintf("%s; its %s moved there", text, alerts), By: c.actor()}, partClose)
	s.note(ctx, t, Event{At: now, What: fmt.Sprintf("%s (%s) was merged into this case: its %s moved here; its history and its conversation with Sherlock stay on %s",
		caseID(n), clip(src.Title, 120), alerts, caseID(n)), By: c.actor()})
	s.tellBy(n, c, "merged it into "+caseID(t))
	s.tellBy(t, c, caseID(n)+" was merged into this case")
	s.record(audit.Event{Action: ActionMerge, Who: who(c.Person), Cluster: src.Cluster, Target: caseID(n), From: c.From, Outcome: audit.OutcomeOK,
		Detail: fmt.Sprintf("case %s merged into %s (%s), %s moved%s", caseID(n), caseID(t), clip(dst.Title, 120), alerts, prefixed(": ", explanation))})
	// Its troubleshooting ends and its waiting changes are settled, as a
	// close's (CF-34, CF-47).
	s.closed(ctx, n, c.card())
	return s.Get(ctx, c, caseID(n))
}

func (h *handler) merge(w http.ResponseWriter, r *http.Request, c Caller) {
	var in MergeInput
	if !decode(w, r, &in) {
		return
	}
	v, err := h.s.Merge(r.Context(), r.PathValue("id"), in, c)
	h.answer(w, http.StatusOK, c, v, err)
}
