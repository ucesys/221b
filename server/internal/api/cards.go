package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/roles"
	"github.com/ucesys/221b/server/internal/watson"
)

// --- Watson cards (Phase 12, ADR-99 to ADR-102) ---
//
// The agents' proposals as durable cards: listed for the people who may
// approve them, in AIOps › Inbox and the cluster's Watson panel; approved
// or rejected with a reason; checked again; and the SuperAdmin's standing
// approvals and notification channels. Approving needs the approval
// permission (the chart's cases.approverRoles), not admin rights; a card
// that changes the console's own records needs a SuperAdmin, as those
// records do today, and kubectl and helm run as the person who approves,
// so Kubernetes still decides.

func (s *Server) cardsRoutes() {
	s.handle("GET /api/v1/cases/proposals", "cases-proposals", s.getProposals)
	s.handle("GET /api/v1/cases/proposals/{id}", "cases-proposal", s.getProposal)
	s.handle("POST /api/v1/cases/proposals/{id}/approve", "cases-proposal-approve", s.postProposalApprove)
	s.handle("POST /api/v1/cases/proposals/{id}/reject", "cases-proposal-reject", s.postProposalReject)
	s.handle("POST /api/v1/cases/proposals/{id}/recheck", "cases-proposal-recheck", s.postProposalRecheck)
	s.handle("GET /api/v1/cases/standing", "cases-standing", s.getStanding)
	s.handle("POST /api/v1/cases/standing", "cases-standing-add", s.postStanding)
	s.handle("PUT /api/v1/cases/standing/{id}", "cases-standing-save", s.putStanding)
	s.handle("DELETE /api/v1/cases/standing/{id}", "cases-standing-delete", s.deleteStanding)
	s.handle("GET /api/v1/cases/notifications", "cases-notifications", s.getNotifications)
	s.handle("PUT /api/v1/cases/notifications", "cases-notifications-save", s.putNotifications)
	s.handle("POST /api/v1/cases/notifications/{id}/test", "cases-notifications-test", s.postNotificationTest)
	s.opts.Cards.SetCondition(s.cardCondition)
	if s.opts.Watson != nil {
		s.opts.Cards.Register(cards.KindKubectl, watsonAction{s: s, tool: "kubectl"})
		s.opts.Cards.Register(cards.KindHelm, watsonAction{s: s, tool: "helm"})
		s.opts.Watson.SetOnEnd(s.watsonEnded)
		// A proposal logs the commands run from it as they end (CF-61).
		s.opts.Watson.AddOnFinish(s.proposalCommandEnded)
	}
	// --- A proposed change's Sherlock (CF-61): its conversation is kept by
	// the cards, and forgotten once it is decided.
	if s.opts.Sherlock != nil && s.opts.Settings != nil {
		s.opts.Sherlock.AddKeeper(s.opts.Cards.Chats())
		s.opts.Cards.SetForget(s.opts.Sherlock.ForgetSession)
		s.proposalTroubleRoutes()
	}
}

// mayApproveCards says whether a person holds the approval permission:
// a SuperAdmin, or a role the chart lists (cases.approverRoles).
func (s *Server) mayApproveCards(r *http.Request) bool {
	if s.opts.Auth == nil || s.isSuperAdmin(r) {
		return true
	}
	sess, ok := auth.SessionFrom(r.Context())
	if !ok {
		return false
	}
	return slices.ContainsFunc(sess.Roles, func(id roles.ID) bool { return slices.Contains(s.opts.CardRoles, id) })
}

// maySeeCards says whether a person sees the proposals: those who may
// approve them, and a SuperViewer, who sees what a SuperAdmin sees.
func (s *Server) maySeeCards(r *http.Request) bool {
	if s.mayApproveCards(r) {
		return true
	}
	sess, _ := auth.SessionFrom(r.Context())
	return slices.Contains(sess.Roles, roles.SuperViewer)
}

func (s *Server) cardsReady(w http.ResponseWriter, r *http.Request) bool {
	if !s.maySeeCards(r) {
		jsonError(w, http.StatusForbidden, "Only the people who may approve proposals see them (cases.approverRoles).")
		return false
	}
	return true
}

// cardPerson is the person a request is from, as a card names them.
func (s *Server) cardPerson(r *http.Request) cards.Person {
	w := s.watsonWho(r)
	p := cards.Person{Key: w.User, Name: w.Name}
	if sess, ok := auth.SessionFrom(r.Context()); ok && s.opts.Auth != nil && sess.Method != "none" {
		p.Key = firstOf(sess.User.Username, sess.User.Subject, sess.User.Email)
	}
	return p
}

// cardActor is who a card's kubectl or helm runs as: the person who
// approved it, with their session.
type cardActor struct {
	who  watson.Who
	sess auth.Session
	has  bool
}

func (s *Server) cardApprover(r *http.Request) cards.Approver {
	sess, has := auth.SessionFrom(r.Context())
	return cards.Approver{Person: s.cardPerson(r), Admin: s.isSuperAdmin(r), As: cardActor{who: s.watsonWho(r), sess: sess, has: has}}
}

func cardStatus(err error) int {
	switch {
	case errors.Is(err, cards.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, cards.ErrInvalid):
		return http.StatusBadRequest
	case errors.Is(err, cards.ErrOwn), errors.Is(err, cards.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, cards.ErrState), errors.Is(err, cards.ErrStale):
		return http.StatusConflict
	case errors.Is(err, cards.ErrFull):
		return http.StatusInsufficientStorage
	}
	return http.StatusInternalServerError
}

// cardError answers a failure in the store's words.
func cardError(w http.ResponseWriter, err error) {
	msg := err.Error()
	for _, e := range []error{cards.ErrInvalid, cards.ErrState, cards.ErrOwn, cards.ErrForbidden, cards.ErrStale, cards.ErrFull} {
		msg = strings.Replace(msg, e.Error()+": ", "", 1)
	}
	if errors.Is(err, cards.ErrStale) {
		msg = "This proposal cannot be approved as it stands: " + msg
	}
	jsonError(w, cardStatus(err), upperFirst(msg))
}

// proposalView is a card as the page shows it to one person.
type proposalView struct {
	cards.Card
	ClusterName string `json:"clusterName"`
	AgentName   string `json:"agentName"`
	// CanApprove says this person may approve it now; Cannot why not.
	CanApprove bool   `json:"canApprove"`
	Cannot     string `json:"cannot,omitempty"`
	// Mine says they asked for it, set its trigger, or decided it.
	Mine bool `json:"mine,omitempty"`
	// --- Who did it, and what it resolves in words (Phase 12, wave 6c,
	// CF-35, CF-39): who decided it, and the title of each case and known
	// issue it resolves.
	DecidedBy      *cards.Actor      `json:"decidedBy,omitempty"`
	ResolvesTitles map[string]string `json:"resolvesTitles,omitempty"`
	// MayAsk says this person asks Sherlock and runs commands from the
	// proposal's drawer while it waits: they may approve proposals, or work
	// the cases (CF-61). Only in the answers about one proposal.
	MayAsk bool `json:"mayAsk,omitempty"`
}

// resolvesTitles are the titles of what the cards resolve, read once for
// them all (CF-39).
func (s *Server) resolvesTitles(ctx context.Context, list ...cards.Card) map[string]string {
	var ids []string
	for _, c := range list {
		ids = append(ids, c.Resolves...)
	}
	if len(ids) == 0 || s.opts.Casework == nil {
		return nil
	}
	return s.opts.Casework.Titles(ctx, ids)
}

func (s *Server) viewProposal(r *http.Request, c cards.Card) proposalView {
	v := s.viewProposalWith(r, c, s.resolvesTitles(r.Context(), c))
	v.MayAsk = s.proposalMayAsk(r)
	return v
}

// viewProposalWith is a card for one person, with the titles of what the
// cards resolve.
func (s *Server) viewProposalWith(r *http.Request, c cards.Card, titles map[string]string) proposalView {
	proposer := c.Proposer()
	c.ProposedBy = &proposer
	v := proposalView{Card: c, ClusterName: c.Cluster, AgentName: s.opts.Cards.AgentName(c.Agent), DecidedBy: c.Decider()}
	if proposer.Kind == cards.ActorAgent {
		proposer.Name = v.AgentName
		v.ProposedBy = &proposer
	}
	for _, id := range c.Resolves {
		if t, ok := titles[id]; ok {
			if v.ResolvesTitles == nil {
				v.ResolvesTitles = map[string]string{}
			}
			v.ResolvesTitles[id] = t
		}
	}
	if s.opts.Clusters != nil {
		if sum, ok := s.opts.Clusters.Summary(c.Cluster); ok {
			v.ClusterName = sum.Name
		}
	}
	me := s.cardPerson(r)
	v.Mine = c.Mine(me)
	if c.Open() {
		switch {
		case !s.mayApproveCards(r):
			v.Cannot = "You may see proposals, not approve them."
		default:
			if err := s.opts.Cards.CanApprove(c, s.cardApprover(r)); err != nil {
				msg := err.Error()
				for _, e := range []error{cards.ErrState, cards.ErrOwn, cards.ErrForbidden} {
					msg = strings.Replace(msg, e.Error()+": ", "", 1)
				}
				v.Cannot = upperFirst(msg) + "."
			} else {
				v.CanApprove = true
			}
		}
	}
	if v.Plans == nil {
		v.Plans = []cards.Plan{}
	}
	return v
}

// getProposals lists the cards, newest first, with how many wait for this
// person: ?state=queued, ?cluster=, ?agent=, ?mine=1.
func (s *Server) getProposals(w http.ResponseWriter, r *http.Request) {
	if !s.cardsReady(w, r) {
		return
	}
	q := r.URL.Query()
	f := cards.Filter{State: q.Get("state"), Cluster: q.Get("cluster"), Agent: q.Get("agent")}
	if q.Get("mine") == "1" || q.Get("mine") == "true" {
		me := s.cardPerson(r)
		f.Mine = &me
	}
	list, err := s.opts.Cards.List(r.Context(), f)
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, "The proposals could not be read: "+err.Error())
		return
	}
	out := make([]proposalView, 0, len(list))
	waiting := 0
	titles := s.resolvesTitles(r.Context(), list...)
	for _, c := range list {
		v := s.viewProposalWith(r, c, titles)
		if v.CanApprove {
			waiting++
		}
		out = append(out, v)
	}
	writeValue(w, r, map[string]any{"proposals": out, "waiting": waiting, "mayApprove": s.mayApproveCards(r)})
}

func (s *Server) getProposal(w http.ResponseWriter, r *http.Request) {
	if !s.cardsReady(w, r) {
		return
	}
	c, err := s.opts.Cards.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		cardError(w, err)
		return
	}
	writeValue(w, r, s.viewProposal(r, c))
}

func (s *Server) postProposalApprove(w http.ResponseWriter, r *http.Request) {
	if !s.mayApproveCards(r) {
		jsonError(w, http.StatusForbidden, "Approving proposals needs the approval permission (cases.approverRoles).")
		return
	}
	var in struct {
		Comment string `json:"comment"`
	}
	if r.ContentLength != 0 && !decodeAutomation(w, r, &in) {
		return
	}
	c, err := s.opts.Cards.Approve(r.Context(), r.PathValue("id"), s.cardApprover(r), in.Comment)
	if err != nil {
		if !s.cardConflict(w, r, c, err) {
			cardError(w, err)
		}
		return
	}
	// A chat's card the person sent to Watson too: approved here, the
	// card there goes, so it cannot run twice.
	if c.WatsonItem != "" && c.State != cards.Queued && s.opts.Watson != nil {
		s.opts.Watson.Supersede(c.WatsonItem, "Approved as proposal "+c.ID+" in AIOps › Inbox.")
	}
	writeValue(w, r, s.viewProposal(r, c))
}

// cardConflict answers a decision that came too late or does not hold
// with 409 and the card as it is now, so the page shows who decided it and
// when without reading it again: a second approval of a card someone else
// already approved is refused, never counted (Phase 12, IB-08). It says
// whether it answered.
func (s *Server) cardConflict(w http.ResponseWriter, r *http.Request, c cards.Card, err error) bool {
	if c.ID == "" || (!errors.Is(err, cards.ErrState) && !errors.Is(err, cards.ErrStale)) {
		return false
	}
	msg := upperFirst(strings.Replace(err.Error(), cards.ErrState.Error()+": ", "", 1))
	if errors.Is(err, cards.ErrStale) {
		msg = "This proposal cannot be approved as it stands: " + strings.TrimPrefix(err.Error(), cards.ErrStale.Error()+": ")
	}
	b, _ := json.Marshal(map[string]any{"error": msg, "proposal": s.viewProposal(r, c)})
	writeJSONStatus(w, r, http.StatusConflict, b)
	return true
}

func (s *Server) postProposalReject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	me := s.cardPerson(r)
	if !s.mayApproveCards(r) {
		// The person who asked in a chat withdraws their own.
		c, err := s.opts.Cards.Get(r.Context(), id)
		if err != nil || c.Requester == nil || me.Key == "" || c.Requester.Key != me.Key {
			jsonError(w, http.StatusForbidden, "Rejecting proposals needs the approval permission (cases.approverRoles).")
			return
		}
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !decodeAutomation(w, r, &in) {
		return
	}
	c, err := s.opts.Cards.Reject(r.Context(), id, me, in.Reason)
	if err != nil {
		if !s.cardConflict(w, r, c, err) {
			cardError(w, err)
		}
		return
	}
	if c.WatsonItem != "" && s.opts.Watson != nil {
		s.opts.Watson.Supersede(c.WatsonItem, "Rejected as proposal "+c.ID+": "+in.Reason)
	}
	writeValue(w, r, s.viewProposal(r, c))
}

func (s *Server) postProposalRecheck(w http.ResponseWriter, r *http.Request) {
	if !s.cardsReady(w, r) {
		return
	}
	c, err := s.opts.Cards.Recheck(r.Context(), r.PathValue("id"), s.cardPerson(r))
	if err != nil {
		cardError(w, err)
		return
	}
	writeValue(w, r, s.viewProposal(r, c))
}

// ---------- standing approvals and notifications: a SuperAdmin's ----------

func (s *Server) cardsAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !s.isSuperAdmin(r) {
		jsonError(w, http.StatusForbidden, "Only a SuperAdmin sets standing approvals and notification channels.")
		return false
	}
	return true
}

func (s *Server) getStanding(w http.ResponseWriter, r *http.Request) {
	if !s.cardsReady(w, r) {
		return
	}
	list, err := s.opts.Cards.Standings(r.Context())
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	for i := range list {
		list[i].RunAs = cards.RunAs{}
	}
	writeValue(w, r, map[string]any{"standing": list})
}

func (s *Server) saveStanding(w http.ResponseWriter, r *http.Request, id string) {
	if !s.cardsAdmin(w, r) {
		return
	}
	var in cards.Standing
	if !decodeAutomation(w, r, &in) {
		return
	}
	in.ID, in.Applied = id, nil
	p := s.person(r)
	st, err := s.opts.Cards.SaveStanding(r.Context(), in, s.cardPerson(r), cards.RunAs{User: p.User, Groups: p.Groups})
	if err != nil {
		cardError(w, err)
		return
	}
	st.RunAs = cards.RunAs{}
	code := http.StatusOK
	if id == "" {
		code = http.StatusCreated
	}
	b, _ := json.Marshal(st)
	writeJSONStatus(w, r, code, b)
}

func (s *Server) postStanding(w http.ResponseWriter, r *http.Request) { s.saveStanding(w, r, "") }

func (s *Server) putStanding(w http.ResponseWriter, r *http.Request) {
	s.saveStanding(w, r, r.PathValue("id"))
}

func (s *Server) deleteStanding(w http.ResponseWriter, r *http.Request) {
	if !s.cardsAdmin(w, r) {
		return
	}
	if err := s.opts.Cards.DeleteStanding(r.Context(), r.PathValue("id"), s.cardPerson(r)); err != nil {
		cardError(w, err)
		return
	}
	writeValue(w, r, map[string]bool{"removed": true})
}

func (s *Server) getNotifications(w http.ResponseWriter, r *http.Request) {
	if !s.cardsAdmin(w, r) {
		return
	}
	v, err := s.opts.Cards.Notifications(r.Context())
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeValue(w, r, v)
}

// putNotifications saves the channels. An address is written, never read
// back: the answer says only whether each channel has one.
func (s *Server) putNotifications(w http.ResponseWriter, r *http.Request) {
	if !s.cardsAdmin(w, r) {
		return
	}
	var in cards.NotifyInput
	if !decodeAutomation(w, r, &in) {
		return
	}
	v, err := s.opts.Cards.SaveNotifications(r.Context(), in, s.cardPerson(r))
	if err != nil {
		cardError(w, err)
		return
	}
	writeValue(w, r, v)
}

func (s *Server) postNotificationTest(w http.ResponseWriter, r *http.Request) {
	if !s.cardsAdmin(w, r) {
		return
	}
	if err := s.opts.Cards.TestChannel(r.Context(), r.PathValue("id"), s.cardPerson(r)); err != nil {
		if errors.Is(err, cards.ErrNotFound) || errors.Is(err, cards.ErrInvalid) {
			cardError(w, err)
			return
		}
		jsonError(w, http.StatusBadGateway, "The test message was not delivered: "+err.Error()+".")
		return
	}
	writeValue(w, r, map[string]bool{"sent": true})
}

// ---------- Watson's side ----------

// cardCluster is a cluster as Watson sees it for a card: as the person
// whose session ctx carries, or with none, as for an agent.
func (s *Server) cardCluster(ctx context.Context, id string) (watson.Cluster, bool) {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	if err != nil {
		return watson.Cluster{}, false
	}
	r.SetPathValue("cluster", id)
	return s.watsonClusterQuiet(r)
}

// watsonAction is a card's kubectl or helm command, run through Watson's
// runner: read at proposal and at approval with the console's account,
// for the fingerprint, and run as the person who approved it last.
type watsonAction struct {
	s    *Server
	tool string
}

func (a watsonAction) Admin() bool { return false }

func (a watsonAction) line(x cards.Action) (string, error) {
	var in struct {
		Command string `json:"command"`
	}
	b, _ := json.Marshal(x.Args)
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return "", fmt.Errorf("%w: a %s action takes {command}: %v", cards.ErrInvalid, a.tool, err)
	}
	line := strings.TrimSpace(in.Command)
	if !strings.HasPrefix(line, a.tool+" ") {
		return "", fmt.Errorf("%w: a %s action's command starts with %s", cards.ErrInvalid, a.tool, a.tool)
	}
	c, err := watson.Parse(line)
	if err != nil {
		return "", fmt.Errorf("%w: %v", cards.ErrInvalid, err)
	}
	if !c.Change || c.DryRun() {
		return "", fmt.Errorf("%w: %s reads and runs without an approval; a card carries changes", cards.ErrInvalid, c.Name())
	}
	return c.Line, nil
}

func (a watsonAction) Check(x cards.Action) error {
	_, err := a.line(x)
	return err
}

func (a watsonAction) Describe(ctx context.Context, cluster string, x cards.Action) (cards.Plan, error) {
	line, err := a.line(x)
	if err != nil {
		return cards.Plan{}, err
	}
	p := cards.Plan{Summary: line}
	cl, ok := a.s.cardCluster(ctx, cluster)
	if !ok {
		return p, fmt.Errorf("the cluster %s is not connected", cluster)
	}
	ins, err := a.s.opts.Watson.Inspect(ctx, cl, line)
	p.Targets, p.Impact, p.Fingerprint = ins.Targets, ins.Impact, ins.Fingerprint
	return p, err
}

// runner is the Watson person a card's change runs as: the approver, or
// for a standing approval the SuperAdmin who set it, as the clusters knew
// them then.
func (a watsonAction) runner(by cards.Approver) (watson.Who, context.Context) {
	ctx := context.Background()
	switch x := by.As.(type) {
	case cardActor:
		if x.has {
			ctx = auth.WithSession(ctx, x.sess)
		}
		return x.who, ctx
	case cards.Standing:
		who := watson.Who{Name: by.Name, User: x.By.Key, Person: kube.Person{User: x.RunAs.User, Groups: x.RunAs.Groups}, Audit: audit.Who{Subject: x.By.Key, Name: by.Name}}
		return who, ctx
	}
	return watson.Who{Name: by.Name, User: by.Key}, ctx
}

func (a watsonAction) Apply(ctx context.Context, c cards.Card, i int, by cards.Approver) (cards.Result, error) {
	line, err := a.line(c.Actions[i])
	if err != nil {
		return cards.Result{}, err
	}
	who, sctx := a.runner(by)
	cl, ok := a.s.cardCluster(sctx, c.Cluster)
	if !ok {
		return cards.Result{}, fmt.Errorf("the cluster %s is not connected", c.Cluster)
	}
	var plan *watson.Inspection
	if i < len(c.Plans) {
		plan = &watson.Inspection{Line: line, Targets: c.Plans[i].Targets, Impact: c.Plans[i].Impact}
	}
	// Run from the proposal (CF-62): its output shows in the drawer's
	// Watson, and the proposal logs it.
	it, err := a.s.opts.Watson.RunApproved(cl, who, line, cards.ChatSession(c.ID), c.Title, plan)
	if err != nil {
		r := cards.Result{Summary: err.Error()}
		if it != nil {
			r.Ref = it.ID
		}
		return r, err
	}
	done, finished := a.s.opts.Watson.WaitDone(ctx, it.ID)
	if !finished {
		return cards.Result{Summary: "it had not finished when the card stopped waiting; see " + who.Name + "'s Watson panel", Ref: it.ID}, errors.New("not finished")
	}
	r := cards.Result{OK: done.Status == watson.StatusDone, Summary: fmt.Sprintf("%s, exit %d: run as %s, output in their Watson panel", done.Status, done.Exit, who.Name), Ref: it.ID}
	if done.Reason != "" {
		r.Summary += ". " + done.Reason
	}
	if !r.OK {
		return r, errors.New(r.Summary)
	}
	return r, nil
}

// watsonEnded tells the cards how a chat's command ended in the Watson
// panel.
func (s *Server) watsonEnded(it watson.Item) {
	if s.opts.Cards == nil {
		return
	}
	var by cards.Person
	if it.Approver != nil {
		by = cards.Person{Key: it.Approver.User, Name: it.Approver.Name}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.opts.Cards.WatsonEnded(ctx, it.ID, it.Status, by, it.Exit)
}

// proposeFromChat keeps the changes a person sent to Watson from an
// agent's answer as durable cards too: the Watson card lapses in minutes
// and lives in memory, the proposal waits in the Inbox for 24 hours and
// outlives a restart. Either approval runs it, once.
func (s *Server) proposeFromChat(r *http.Request, c watson.Cluster, who watson.Who, items []*watson.Item, why string) {
	if s.opts.Cards == nil {
		return
	}
	for _, it := range items {
		if !it.Change || it.Status != watson.StatusPending || it.Session != nil {
			continue
		}
		tool, _, _ := strings.Cut(it.Line, " ")
		if tool != cards.KindKubectl && tool != cards.KindHelm {
			continue
		}
		p := cards.Proposal{
			Agent:     "sherlock",
			Source:    cards.Source{Kind: cards.SourceChat, Label: "Asked by " + who.Name + " in a chat on " + c.Name, SetBy: who.Name},
			Cluster:   c.ID,
			Title:     it.Line,
			Reasoning: why,
			Actions:   []cards.Action{{Kind: tool, Args: map[string]any{"command": it.Line}}},
			DedupKey:  "chat:" + c.ID + ":" + it.Line,
		}
		if _, err := s.opts.Cards.ProposeFor(r.Context(), p, s.cardPerson(r), it.ID); err != nil {
			s.opts.Logger.Warn("a chat's change was not kept as a proposal", "cluster", c.ID, "err", err)
		}
	}
}

// cardCondition checks a card's condition: an alert still on the Alerts
// tab, not recovered; an object still at the version read. It reads with
// the console's account.
func (s *Server) cardCondition(ctx context.Context, cluster string, c cards.Condition) (bool, string, error) {
	str := func(k string) string { v, _ := c.Args[k].(string); return v }
	switch c.Kind {
	case cards.ConditionAlert:
		var st *live.Store
		if s.opts.Clusters != nil {
			st, _, _ = s.opts.Clusters.Lookup(cluster)
		} else {
			st, _ = s.opts.Store.(*live.Store)
		}
		if st == nil {
			return false, "", fmt.Errorf("the cluster %s is not connected", cluster)
		}
		var snap struct {
			Cluster *struct {
				Synced bool `json:"synced"`
			} `json:"cluster"`
			Views map[string]struct {
				Rows []map[string]any `json:"rows"`
			} `json:"views"`
		}
		if err := json.Unmarshal(st.Snapshot([]string{live.ViewAlerts}, nil), &snap); err != nil {
			return false, "", err
		}
		if snap.Cluster == nil || !snap.Cluster.Synced {
			return false, "", errors.New("the console has not read the cluster yet")
		}
		key := str("key")
		for _, row := range snap.Views[live.ViewAlerts].Rows {
			if row["key"] != key {
				continue
			}
			if sev, _ := row["severity"].(string); sev == "recovered" {
				return false, "the alert on " + key + " recovered", nil
			}
			return true, "", nil
		}
		return false, "there is no alert on " + key + " any more", nil
	case cards.ConditionVersion:
		if s.opts.Watson == nil {
			return false, "", errors.New("Watson is not part of this console")
		}
		cl, ok := s.cardCluster(ctx, cluster)
		if !ok {
			return false, "", fmt.Errorf("the cluster %s is not connected", cluster)
		}
		line := fmt.Sprintf("kubectl get %s %s -o jsonpath={.metadata.resourceVersion}", str("resource"), str("name"))
		if ns := str("namespace"); ns != "" {
			line += " -n " + ns
		}
		var out bytes.Buffer
		code, err := s.opts.Watson.RunLine(ctx, cl, kube.Person{}, line, false, &out)
		got := strings.TrimSpace(out.String())
		switch {
		case err != nil:
			return false, "", err
		case code != 0 && strings.Contains(got, "NotFound"), code != 0 && strings.Contains(got, "not found"):
			return false, str("resource") + " " + str("name") + " no longer exists", nil
		case code != 0:
			return false, "", fmt.Errorf("reading %s %s failed", str("resource"), str("name"))
		case got != str("resourceVersion"):
			return false, fmt.Sprintf("%s %s changed: it is at version %s now, not %s", str("resource"), str("name"), got, str("resourceVersion")), nil
		}
		return true, "", nil
	}
	return true, "", nil
}

// --- end of Watson cards.
