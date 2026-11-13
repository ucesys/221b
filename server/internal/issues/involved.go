package issues

import "strings"

// Who is involved in a case (EM-05): the people the case's emails go to,
// and those whose "Cases I'm involved in" lists it. It is read from what
// the case itself records, nothing else:
//
//   - its reporter, and the person who filed it for them (support);
//   - who opened it in the console;
//   - its assignee (owner);
//   - who follows it (joined it with "Add to C-n", or the reporter of a
//     case closed as its duplicate);
//   - who wrote in its conversation: a reply, a question, an internal
//     note, a comment, or sent a reply someone else drafted;
//   - who closed it, while it is closed;
//   - who approved a change that fixed it (Approvers, recorded since
//     EM-05 when the change is applied).
//
// A system account (an API token, the console itself, an agent) is never
// involved, nor a person without an id. Works marks the people who work
// the case (its assignee, who opened, closed or approved, who wrote as an
// operator): only they hear of an internal note.

// Why a person is involved.
const (
	InvolvedReported = "reported"
	InvolvedFiled    = "filed"
	InvolvedOpened   = "opened"
	InvolvedAssigned = "assigned"
	InvolvedFollows  = "follows"
	InvolvedWrote    = "wrote"
	InvolvedClosed   = "closed"
	InvolvedApproved = "approved"
)

// Involvement is one person involved in a case, and why.
type Involvement struct {
	Person
	Why []string `json:"why"`
	// Works says they work the case, rather than only report or follow it.
	Works bool `json:"works,omitempty"`
}

// Involved are the people involved in a case, in the order first met: the
// reporter's side first, then the people who work it. A person met twice
// keeps the first address known for them.
func Involved(r *Case) []Involvement {
	var out []Involvement
	at := map[string]int{}
	add := func(p *Person, why string, works bool) {
		if p == nil || p.System || strings.TrimSpace(p.ID) == "" || strings.HasPrefix(p.ID, "system:") || p.ID == systemPerson.ID {
			return
		}
		i, ok := at[p.ID]
		if !ok {
			at[p.ID] = len(out)
			out = append(out, Involvement{Person: Person{ID: p.ID, Name: p.Name, Email: p.Email}})
			i = len(out) - 1
		}
		x := &out[i]
		if x.Name == "" {
			x.Name = p.Name
		}
		if x.Email == "" {
			x.Email = p.Email
		}
		if !contains(x.Why, why) {
			x.Why = append(x.Why, why)
		}
		x.Works = x.Works || works
	}
	add(&r.Reporter, InvolvedReported, false)
	add(r.FiledBy, InvolvedFiled, false)
	add(r.OpenedBy, InvolvedOpened, true)
	add(r.Owner, InvolvedAssigned, true)
	for i := range r.Watchers {
		add(&r.Watchers[i], InvolvedFollows, false)
	}
	for i := range r.Messages {
		m := &r.Messages[i]
		if m.Draft || m.Kind == FromSystem {
			continue
		}
		add(&m.From, InvolvedWrote, m.Kind == FromOperator)
		add(m.SentBy, InvolvedWrote, true)
	}
	if !r.Status.Open() {
		add(r.ClosedBy, InvolvedClosed, true)
	}
	for i := range r.Approvers {
		add(&r.Approvers[i], InvolvedApproved, true)
	}
	return out
}

// involvedIDs are the ids of the people involved in a case that its line
// does not name otherwise (reporter, filer, assignee, followers), for
// "Cases I'm involved in" (EM-08).
func involvedIDs(r *Case, l *Line) []string {
	var out []string
	for _, p := range Involved(r) {
		if p.ID == l.Reporter || p.ID == l.FiledBy || (l.Assignee != nil && p.ID == l.Assignee.ID) || l.watchedBy(p.ID) {
			continue
		}
		out = append(out, p.ID)
	}
	return out
}

// Involves says whether a person, by their id, is involved in the case of
// the line (EM-05). A line kept before EM-08 names only the reporter, the
// filer, the assignee and the followers, until the case changes again.
func (l Line) Involves(id string) bool {
	if id == "" {
		return false
	}
	return l.Reporter == id || l.FiledBy == id || (l.Assignee != nil && l.Assignee.ID == id) || l.watchedBy(id) || contains(l.Involved, id)
}

// AddApprovers records the people who approved a change that fixed the
// case (EM-05), each once.
func AddApprovers(r *Case, people ...Person) {
	for _, p := range people {
		if p.ID == "" || p.System {
			continue
		}
		seen := false
		for _, x := range r.Approvers {
			if x.ID == p.ID {
				seen = true
				break
			}
		}
		if !seen && len(r.Approvers) < 20 {
			r.Approvers = append(r.Approvers, Person{ID: p.ID, Name: p.Name})
		}
	}
}
