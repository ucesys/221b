package knowledge

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/scrub"
)

// --- A wiki page's own actions (Phase 12, KN-23, KN-24, ADR-139) ---
//
// This is outdated…: an Admin or a SuperAdmin, who may add to the wiki,
// writes what is outdated on an approved page and what is true now. It
// goes in as a note of the source "Added in Mycroft" tied to the page,
// scrubbed like any note, read by a sync started at once, and given to
// Mycroft as a page it must rewrite: the correction wins over older
// sources, and the page names it as a source with its date. The redraft
// waits for review as every change does (KN-08).
//
// Retire page: a reviewer asks, with a reason, and one other reviewer
// approves (the one who asked never approves their own; an agent never
// counts). A retired page leaves every replica's index, so no agent reads
// it; its file stays in the store, and Mycroft does not rewrite it (a
// change to it is held in its run). Make active again is asked and
// approved the same way. The retirements are kept in the manifest, which
// every replica reads with the pages.

// --- This is outdated… (KN-23) ---

// The states of a correction.
const (
	// CorrectionWaiting waits to be read by the sync started for it.
	CorrectionWaiting = "waiting"
	// CorrectionDrafted is in a run of Mycroft's, waiting for review
	// while the run is.
	CorrectionDrafted = "drafted"
	// CorrectionDone is read, with no redraft to wait for: Mycroft has no
	// model, or changed nothing.
	CorrectionDone = "done"
)

// keptCorrections is how many corrections stay listed.
const keptCorrections = 50

// Correction is one This is outdated… sent: the note it became, the page
// it is for, who wrote it, and the run its redraft waits in.
type Correction struct {
	// ID is the note's name in "Added in Mycroft".
	ID    string    `json:"id"`
	Path  string    `json:"path"`
	Title string    `json:"title"`
	By    string    `json:"by"`
	Name  string    `json:"name"`
	At    time.Time `json:"at"`
	State string    `json:"state"`
	Run   string    `json:"run,omitempty"`
	// Note says why no redraft waits, when none does.
	Note string `json:"note,omitempty"`
}

// OutdatedResult is what sending a correction did.
type OutdatedResult struct {
	Correction Correction `json:"correction"`
	// Model says Mycroft has a model to redraft the page with; without
	// one the note is kept and no page is written (MY-08).
	Model bool   `json:"model"`
	Text  string `json:"text"`
}

// Outdated sends a correction of an approved page (KN-23): what is
// outdated there and what is true now, plain text or Markdown up to a
// note's size. It needs the store and the source of additions, as Add
// does.
func (s *Service) Outdated(ctx context.Context, c Caller, pagePath, text string) (OutdatedResult, error) {
	if !c.MayAdd() {
		return OutdatedResult{}, wrapf(ErrDenied, "only an Admin or a SuperAdmin corrects the wiki")
	}
	if err := s.leads(); err != nil {
		return OutdatedResult{}, err
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return OutdatedResult{}, err
	}
	if x, ok := set.source(AddedSourceID); !ok || x.Off {
		return OutdatedResult{}, invalid("the source %q is switched off in Settings › Knowledge: switch it on to correct the wiki", "Added in Mycroft")
	}
	if _, _, err := s.currentStore(ctx); err != nil {
		return OutdatedResult{}, err
	}
	page, ok := s.PageAt(pagePath)
	switch {
	case !ok:
		return OutdatedResult{}, wrapf(ErrNotFound, "no approved page %q", pagePath)
	case page.Retired != nil:
		return OutdatedResult{}, invalid("the page is retired: make it active again before correcting it")
	}
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	switch {
	case text == "":
		return OutdatedResult{}, invalid("say what is outdated and what is true now")
	case len(text) > MaxNote:
		return OutdatedResult{}, invalid("a correction is up to %d KiB", MaxNote>>10)
	case !utf8.ValidString(text):
		return OutdatedResult{}, invalid("the correction is not UTF-8 text")
	}
	now := s.o.Now().UTC()
	title := "Correction to " + page.Title
	if utf8.RuneCountInString(title) > maxNoteTitle {
		title = strings.TrimSpace(string([]rune(title)[:maxNoteTitle-1])) + "…"
	}
	// Each correction is its own note, so a second one does not replace
	// the first before it is read.
	name := itemName("correction ", strings.TrimSuffix(path.Base(pagePath), ".md")+" "+now.Format("2006-01-02 150405")) + ".md"
	body := fmt.Sprintf("Correction to the wiki page %s (“%s”), written by %s on %s. What it says wins over older sources.\n\n%s", pagePath, page.Title, c.label(), now.Format("2006-01-02"), text)
	body = scrub.Known(scrub.Text(body), s.secretValues(ctx))
	corr := Correction{ID: name, Path: pagePath, Title: page.Title, By: c.ID, Name: c.label(), At: now, State: CorrectionWaiting}
	if _, err := s.editDrafts(ctx, func(d *draftsDoc) error {
		d.Corrections = append(d.Corrections, corr)
		return nil
	}); err != nil {
		return OutdatedResult{}, err
	}
	s.audit(audit.Event{Action: ActionOutdated, Who: c.who(), Target: pagePath, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("a correction of %s sent by %s as %q (%d bytes)", pagePath, c.label(), name, len(text)), From: c.From})
	doc := pendingDoc{source: AddedSourceID, name: name, title: title, text: body, hash: hashOf([]byte(pagePath + "\x00" + now.String() + "\x00" + text)), at: now, by: c.label(), corrects: pagePath}
	s.readSoon(ctx, c, []pendingDoc{doc}, "correction of "+pagePath)
	res := OutdatedResult{Correction: corr, Model: s.o.Model != nil}
	if res.Model {
		res.Text = "The correction was sent. Mycroft redrafts the page with it now; the redraft waits for review under Drafts."
	} else {
		res.Text = "The correction was kept as a source the agents can quote, but Mycroft has no model here: no page is written from it."
	}
	return res, nil
}

// noteCorrections records what a sync did with the corrections it read:
// the run their redraft waits in, or why none waits.
func (s *Service) noteCorrections(ctx context.Context, changed []readItem, run *Run) {
	read := map[string]bool{}
	for _, ri := range changed {
		if ri.item.Corrects != "" && ri.source.ID == AddedSourceID {
			read[ri.item.ID] = true
		}
	}
	if len(read) == 0 {
		return
	}
	_, err := s.editDrafts(ctx, func(d *draftsDoc) error {
		for i := range d.Corrections {
			c := &d.Corrections[i]
			if !read[c.ID] || c.State != CorrectionWaiting {
				continue
			}
			switch {
			case s.o.Model == nil:
				c.State, c.Note = CorrectionDone, "Mycroft has no model here: no page is written."
			case run != nil && run.open() && slices.ContainsFunc(run.Pages, func(p DraftPage) bool { return p.Path == c.Path && p.State == StateWaiting }):
				c.State, c.Run = CorrectionDrafted, run.ID
			default:
				c.State, c.Note = CorrectionDone, "Mycroft wrote no change to the page from it."
			}
		}
		return nil
	})
	if err != nil {
		s.o.Logger.Warn("what a sync did with the corrections could not be kept", "err", err)
	}
}

// PendingCorrection is the newest correction of a page still waiting: to
// be read, or redrafted in a run waiting for review; nil when none
// waits.
func (s *Service) PendingCorrection(ctx context.Context, pagePath string) (*Correction, error) {
	var d draftsDoc
	if _, err := load(ctx, s.kv, DraftsKey, &d); err != nil {
		return nil, err
	}
	s.pendingMu.Lock()
	pending := map[string]bool{}
	for _, p := range s.pending {
		if p.source == AddedSourceID {
			pending[p.name] = true
		}
	}
	s.pendingMu.Unlock()
	for i := len(d.Corrections) - 1; i >= 0; i-- {
		c := d.Corrections[i]
		if c.Path != pagePath {
			continue
		}
		switch c.State {
		case CorrectionWaiting:
			// Waits while its text is held to be read; after a restart
			// it is gone, and no longer waits.
			if pending[c.ID] {
				return &c, nil
			}
		case CorrectionDrafted:
			i := slices.IndexFunc(d.Runs, func(r Run) bool { return r.ID == c.Run })
			if i >= 0 && d.Runs[i].open() && slices.ContainsFunc(d.Runs[i].Pages, func(p DraftPage) bool { return p.Path == c.Path && p.State == StateWaiting }) {
				return &c, nil
			}
		}
	}
	return nil, nil
}

// --- Retire page and Make active again (KN-24) ---

// The actions a request asks for.
const (
	RequestRetire   = "retire"
	RequestActivate = "activate"
)

// The states of a request.
const (
	RequestWaiting  = "waiting"
	RequestApproved = "approved"
	RequestRejected = "rejected"
)

// maxReason bounds a request's reason and a rejection's comment.
const maxReason = 500

// PageRequest is a request to retire a page, or make it active again,
// waiting in AIOps › Inbox for one approver other than who asked.
type PageRequest struct {
	ID     string    `json:"id"`
	Action string    `json:"action"`
	Path   string    `json:"path"`
	Title  string    `json:"title"`
	Reason string    `json:"reason"`
	By     string    `json:"by"`
	Name   string    `json:"name"`
	At     time.Time `json:"at"`
	State  string    `json:"state"`
	// DecidedBy and DecidedName are who approved or rejected it, and
	// Comment why a rejection was made.
	DecidedBy   string    `json:"decidedBy,omitempty"`
	DecidedName string    `json:"decidedName,omitempty"`
	Decided     time.Time `json:"decided,omitzero"`
	Comment     string    `json:"comment,omitempty"`
}

// CanApprove says whether a person may approve it: it waits, and they did
// not ask for it.
func (r PageRequest) CanApprove(person string) bool {
	return r.State == RequestWaiting && r.By != person
}

func (r PageRequest) words() string {
	if r.Action == RequestActivate {
		return "make " + r.Path + " active again"
	}
	return "retire " + r.Path
}

// AskPage asks to retire a page (RequestRetire) or make a retired one
// active again (RequestActivate), with a reason; the request waits for
// one other reviewer. Only reviewers ask.
func (s *Service) AskPage(ctx context.Context, c Caller, action, pagePath, reason string) (PageRequest, error) {
	if !c.Reviewer {
		return PageRequest{}, wrapf(ErrDenied, "only an Admin or a SuperAdmin retires a page of the wiki or makes it active again")
	}
	if err := s.leads(); err != nil {
		return PageRequest{}, err
	}
	reason = strings.TrimSpace(reason)
	switch {
	case action != RequestRetire && action != RequestActivate:
		return PageRequest{}, invalid("a request retires a page or makes it active again")
	case reason == "":
		return PageRequest{}, invalid("give a reason")
	case utf8.RuneCountInString(reason) > maxReason:
		return PageRequest{}, invalid("a reason is at most %d characters", maxReason)
	}
	page, ok := s.PageAt(pagePath)
	switch {
	case !ok:
		return PageRequest{}, wrapf(ErrNotFound, "no approved page %q", pagePath)
	case action == RequestRetire && page.Retired != nil:
		return PageRequest{}, invalid("the page is retired already")
	case action == RequestActivate && page.Retired == nil:
		return PageRequest{}, invalid("the page is active")
	}
	now := s.o.Now().UTC()
	req := PageRequest{ID: fmt.Sprintf("p-%s-%d", now.Format("20060102-1504"), s.runSeq.Add(1)), Action: action, Path: pagePath, Title: page.Title, Reason: reason, By: c.ID, Name: c.label(), At: now, State: RequestWaiting}
	_, err := s.editDrafts(ctx, func(d *draftsDoc) error {
		if i := slices.IndexFunc(d.Requests, func(r PageRequest) bool { return r.Path == pagePath && r.State == RequestWaiting }); i >= 0 {
			return conflictf("%s asked to %s already; it waits for approval", d.Requests[i].Name, d.Requests[i].words())
		}
		d.Requests = append(d.Requests, req)
		return nil
	})
	if err != nil {
		return PageRequest{}, err
	}
	s.audit(audit.Event{Action: ActionRetireRequest, Who: c.who(), Target: pagePath, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("%s asked to %s (request %s): %s", c.label(), req.words(), req.ID, reason), From: c.From})
	return req, nil
}

// PageRequests lists the requests, those waiting first, newest first;
// only the waiting ones unless all.
func (s *Service) PageRequests(ctx context.Context, all bool) ([]PageRequest, error) {
	var d draftsDoc
	if _, err := load(ctx, s.kv, DraftsKey, &d); err != nil {
		return nil, err
	}
	var waiting, decided []PageRequest
	for i := len(d.Requests) - 1; i >= 0; i-- {
		switch {
		case d.Requests[i].State == RequestWaiting:
			waiting = append(waiting, d.Requests[i])
		case all:
			decided = append(decided, d.Requests[i])
		}
	}
	return append(append([]PageRequest{}, waiting...), decided...), nil
}

// PageRequestFor is the request waiting for a page, if one does.
func (s *Service) PageRequestFor(ctx context.Context, pagePath string) (*PageRequest, error) {
	list, err := s.PageRequests(ctx, false)
	if err != nil {
		return nil, err
	}
	for _, r := range list {
		if r.Path == pagePath {
			return &r, nil
		}
	}
	return nil, nil
}

// DecidePage approves or rejects a request. Approval is one reviewer's
// other than who asked (403 for their own; an agent never counts), and
// is applied at once: the page leaves the index, or comes back to it.
// Anyone who reviews rejects one, the one who asked too.
func (s *Service) DecidePage(ctx context.Context, c Caller, id string, approve bool, comment string) (PageRequest, error) {
	if !c.Reviewer {
		return PageRequest{}, wrapf(ErrDenied, "only an Admin or a SuperAdmin decides a request to retire a page")
	}
	if err := s.leads(); err != nil {
		return PageRequest{}, err
	}
	comment = strings.TrimSpace(comment)
	if utf8.RuneCountInString(comment) > maxReason {
		return PageRequest{}, invalid("a comment is at most %d characters", maxReason)
	}
	now := s.o.Now().UTC()
	state := RequestRejected
	if approve {
		state = RequestApproved
	}
	// The decision is recorded first, in the write that checks no one
	// decided before (ADR-111); an approval that cannot be applied is
	// put back to wait.
	var req PageRequest
	_, err := s.editDrafts(ctx, func(d *draftsDoc) error {
		i := slices.IndexFunc(d.Requests, func(r PageRequest) bool { return r.ID == id })
		if i < 0 {
			return wrapf(ErrNotFound, "no request %s", id)
		}
		r := &d.Requests[i]
		if r.State != RequestWaiting {
			return conflictf("%s %s it already, %s", r.DecidedName, r.State, r.Decided.Format("2006-01-02 15:04 UTC"))
		}
		if approve && r.By == c.ID {
			return wrapf(ErrDenied, "you asked for this: another person approves it")
		}
		r.State, r.DecidedBy, r.DecidedName, r.Decided, r.Comment = state, c.ID, c.label(), now, comment
		req = *r
		return nil
	})
	if err != nil {
		return PageRequest{}, err
	}
	if !approve {
		s.audit(audit.Event{Action: ActionRetireReject, Who: c.who(), Target: req.Path, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("%s rejected the request %s to %s, asked by %s", c.label(), req.ID, req.words(), req.Name), From: c.From})
		return req, nil
	}
	if err := s.applyPage(ctx, req, c, now); err != nil {
		if _, uerr := s.editDrafts(ctx, func(d *draftsDoc) error {
			if i := slices.IndexFunc(d.Requests, func(r PageRequest) bool { return r.ID == id }); i >= 0 {
				r := &d.Requests[i]
				r.State, r.DecidedBy, r.DecidedName, r.Decided, r.Comment = RequestWaiting, "", "", time.Time{}, ""
			}
			return nil
		}); uerr != nil {
			s.o.Logger.Warn("a request that could not be applied stays decided", "request", id, "err", uerr)
		}
		action := ActionRetire
		if req.Action == RequestActivate {
			action = ActionActivate
		}
		s.audit(audit.Event{Action: action, Who: c.who(), Target: req.Path, Outcome: audit.OutcomeFailed, Detail: fmt.Sprintf("approving the request %s to %s failed: %v", req.ID, req.words(), err), From: c.From})
		return PageRequest{}, fmt.Errorf("the request waits still: %w", err)
	}
	if req.Action == RequestActivate {
		s.audit(audit.Event{Action: ActionActivate, Who: c.who(), Target: req.Path, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("%s made active again, asked by %s and approved by %s (request %s)", req.Path, req.Name, c.label(), req.ID), From: c.From})
	} else {
		s.audit(audit.Event{Action: ActionRetire, Who: c.who(), Target: req.Path, Outcome: audit.OutcomeOK, Detail: fmt.Sprintf("%s retired, asked by %s and approved by %s (request %s): %s", req.Path, req.Name, c.label(), req.ID, req.Reason), From: c.From})
	}
	return req, nil
}

// applyPage writes a decided request into the manifest in the store,
// and takes it into this replica's copy and index at once; the other
// replicas take it at their next refresh (KN-07).
func (s *Service) applyPage(ctx context.Context, req PageRequest, c Caller, now time.Time) error {
	s.manifestMu.Lock()
	defer s.manifestMu.Unlock()
	st, _, err := s.currentStore(ctx)
	if err != nil {
		return err
	}
	files, err := st.Pull(ctx)
	if err != nil {
		return fmt.Errorf("reading the wiki: %w", err)
	}
	files = maps.Clone(files)
	if _, ok := files[req.Path]; !ok {
		return wrapf(ErrNotFound, "the page %s is no longer in the wiki", req.Path)
	}
	m := newManifest()
	if f, ok := files[ManifestPath]; ok {
		m = parseManifest(f.Body)
	}
	msg := ""
	switch req.Action {
	case RequestRetire:
		if m.Retired == nil {
			m.Retired = map[string]Retirement{}
		}
		m.Retired[req.Path] = Retirement{Reason: req.Reason, AskedBy: req.Name, AskedAt: req.At, ApprovedBy: c.label(), At: now, Request: req.ID}
		msg = fmt.Sprintf("%s retired: asked by %s, approved by %s", req.Path, req.Name, c.label())
	case RequestActivate:
		delete(m.Retired, req.Path)
		msg = fmt.Sprintf("%s made active again: asked by %s, approved by %s", req.Path, req.Name, c.label())
	default:
		return errors.New("a request of an unknown kind")
	}
	body := m.encode()
	if err := st.Write(ctx, []Change{{Path: ManifestPath, Body: body}}, msg); err != nil {
		return err
	}
	files[ManifestPath] = File{Path: ManifestPath, Body: body}
	s.setFiles(ctx, files, true)
	return nil
}
