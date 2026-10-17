package auth

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// Errors a directory gives.
var (
	// ErrBadCredentials is a refused sign-in: no such person, or a wrong
	// password. Nothing more is said to the person (RB-21).
	ErrBadCredentials = errors.New("the directory refused the sign-in")
	// ErrDirectoryDown says the directory cannot be reached (RB-22).
	ErrDirectoryDown = errors.New("the directory cannot be reached")
)

// Person is someone a directory knows.
type Person struct {
	// Ref names them in the directory (the DN), to read their groups again.
	Ref      string
	Username string
	Name     string
	Email    string
	// Groups are the names of the groups they are in, nested ones included,
	// as the directory has them (RB-22).
	Groups []string
}

// Directory is what LDAP sign-in needs from a directory (RB-19 to RB-25).
type Directory interface {
	// Authenticate binds as the person, with their own password, and says
	// who they are. A refusal is ErrBadCredentials; a directory that does
	// not answer is ErrDirectoryDown.
	Authenticate(ctx context.Context, login, password string) (Person, error)
	// Groups reads a person's groups again (RB-23).
	Groups(ctx context.Context, ref string) ([]string, error)
	// Members reads the people in the listed groups, for the Users page
	// (RB-24).
	Members(ctx context.Context, groups []string) ([]Person, error)
	// URL is the server, for the Users page.
	URL() string
}

// DirectoryState is what the Users page says about the directory (RB-24).
type DirectoryState struct {
	URL     string    `json:"url"`
	State   string    `json:"state"`
	Message string    `json:"message,omitempty"`
	Last    time.Time `json:"-"`
	Next    time.Time `json:"-"`
	People  int       `json:"people"`
	Groups  int       `json:"groups"`
	Errors  []string  `json:"errors"`
	Running bool      `json:"running"`
}

// Directory person as the Users page lists them.
type directoryPerson struct {
	Person
	Disabled bool
	Note     string
	LastSeen time.Time
	SignedIn time.Time
}

// people is the list a sync keeps in memory, with the result of the last
// sync (RB-24, RB-25).
type people struct {
	mu      sync.Mutex
	byName  map[string]*directoryPerson
	state   DirectoryState
	running bool
	now     func() time.Time
}

func newPeople(url string, now func() time.Time) *people {
	return &people{byName: map[string]*directoryPerson{}, state: DirectoryState{URL: url, State: "connected", Errors: []string{}}, now: now}
}

// seen records a person at sign-in: they are listed from their first
// sign-in, before any sync (RB-24).
func (p *people) seen(who Person) {
	p.mu.Lock()
	defer p.mu.Unlock()
	d := p.byName[who.Username]
	if d == nil {
		d = &directoryPerson{}
		p.byName[who.Username] = d
	}
	d.Person, d.Disabled, d.Note, d.SignedIn = who, false, "", p.now()
}

// apply merges a successful sync: people no longer in a listed group are
// shown as disabled, not deleted.
func (p *people) apply(members []Person, listedGroups int, next time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	at := p.now()
	in := map[string]bool{}
	for _, m := range members {
		in[m.Username] = true
		d := p.byName[m.Username]
		if d == nil {
			d = &directoryPerson{}
			p.byName[m.Username] = d
		}
		d.Person, d.Disabled, d.Note, d.LastSeen = m, false, "", at
	}
	for name, d := range p.byName {
		if !in[name] && !d.Disabled {
			d.Disabled, d.Note = true, "Not in a listed group since the "+at.Format("15:04")+" sync"
		}
	}
	p.state = DirectoryState{URL: p.state.URL, State: "connected", Last: at, Next: next, People: len(members), Groups: listedGroups, Errors: []string{}}
}

// failed keeps the old list and says why (RB-24).
func (p *people) failed(err error, next time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state.State, p.state.Message, p.state.Next = "error", "The directory could not be read: "+err.Error(), next
	p.state.Errors = []string{err.Error()}
}

// begin marks a sync as running unless one is; it says whether it may.
func (p *people) begin() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running {
		return false
	}
	p.running = true
	return true
}

func (p *people) end() {
	p.mu.Lock()
	p.running = false
	p.mu.Unlock()
}

func (p *people) snapshot() (DirectoryState, []directoryPerson) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.state
	st.Running = p.running
	st.Errors = append([]string{}, p.state.Errors...)
	out := make([]directoryPerson, 0, len(p.byName))
	for _, d := range p.byName {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return st, out
}

// cleanErr keeps an error message to one line, without anything that
// looks like a credential.
func cleanErr(err error) string {
	m := strings.Join(strings.Fields(err.Error()), " ")
	if len(m) > 300 {
		m = m[:300] + "…"
	}
	return m
}
