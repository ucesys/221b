package credentials

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The upgrade to 0.12.1 (CR-12) makes a credential of every secret the
// console kept with what used it (an application's repository token, a
// Helm repository's password or token, a Mycroft source's token or access
// key), one per distinct secret and endpoint, and switches each to name
// it. It runs on the leader at start and again after a restore; a
// consumer it switched is marked so, and is never taken again, so it can
// run any number of times. The old secrets stay where they were, no longer
// read: they can be deleted once the new credentials are checked.

// Found is a secret a consumer keeps itself, as the upgrade finds it.
type Found struct {
	// Key names the consumer for Switch; Label names it for people:
	// "app 221b/k8s-console".
	Key, Label string
	// The credential it becomes.
	Endpoint string
	Kind     string
	Username string
	SendAs   SendAs
	Region   string
	CAPEM    string
	Secret   Secret
}

// Consumer is a part of the console whose own secrets the upgrade moves.
type Consumer interface {
	// Find lists the consumers not switched yet that keep a secret, and
	// notes on those it leaves as they are.
	Find(ctx context.Context) ([]Found, []string, error)
	// Switch has a consumer name the credential, and marks it switched.
	Switch(ctx context.Context, key, credential string) error
}

// identity tells one credential from another: the same secret, sent the
// same way, to the same endpoint, is one.
func identity(endpoint, kind, username, region string, sa SendAs, sec Secret) string {
	b, _ := json.Marshal([]any{endpoint, kind, username, region, sa, sec})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Migrate moves the consumers' own secrets to credentials (CR-12) and
// says what it made.
func (s *Service) Migrate(ctx context.Context, consumers []Consumer, by string) ([]Created, error) {
	var found []Found
	var notes []string
	// Each Found is switched by the consumer that found it.
	var owner []func(context.Context, string, string) error
	for _, c := range consumers {
		f, n, err := c.Find(ctx)
		if err != nil {
			return nil, err
		}
		found, notes = append(found, f...), append(notes, n...)
		for range f {
			owner = append(owner, c.Switch)
		}
	}
	if len(found) == 0 && len(notes) == 0 {
		return nil, nil
	}
	list, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	have := map[string]string{}
	names := map[string]bool{}
	for _, c := range list {
		names[strings.ToLower(c.Name)] = true
		sec, err := s.secret(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		have[identity(c.Endpoint, c.Kind, c.Username, c.Region, c.SendAs, sec)] = c.ID
	}
	type group struct {
		f      Found
		labels []string
		keys   []int
	}
	groups := map[string]*group{}
	var order []string
	for i, f := range found {
		cand := Credential{Name: "x", Kind: f.Kind, Endpoint: f.Endpoint, Username: f.Username, SendAs: f.SendAs, Region: f.Region, CAPEM: f.CAPEM}
		sec := keep(f.Kind, f.Secret.trimmed())
		if err := cand.Check(sec); err != nil {
			notes = append(notes, fmt.Sprintf("%s keeps its own secret: it cannot become a credential (%v)", f.Label, strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": ")))
			continue
		}
		f.Endpoint, f.SendAs, f.Username, f.Region, f.CAPEM, f.Secret = cand.Endpoint, cand.SendAs, cand.Username, cand.Region, cand.CAPEM, sec
		id := identity(f.Endpoint, f.Kind, f.Username, f.Region, f.SendAs, f.Secret)
		g, ok := groups[id]
		if !ok {
			g = &group{f: f}
			groups[id] = g
			order = append(order, id)
		}
		g.labels = append(g.labels, f.Label)
		g.keys = append(g.keys, i)
	}
	var created []Created
	for _, id := range order {
		g := groups[id]
		credID, ok := have[id]
		if !ok {
			name := migratedName(g.f.Endpoint, g.labels, names)
			names[strings.ToLower(name)] = true
			now := s.o.Now().UTC()
			c := Credential{ID: newID(), Name: name, Kind: g.f.Kind, Endpoint: g.f.Endpoint, Username: g.f.Username, SendAs: g.f.SendAs, Region: g.f.Region, CAPEM: g.f.CAPEM,
				Description: migratedDescription(g.labels),
				Set:         g.f.Secret.parts(), Created: now, CreatedBy: by, Changed: now, ChangedBy: by, From: g.labels}
			if err := s.add(ctx, c, g.f.Secret); err != nil {
				return created, err
			}
			credID = c.ID
			have[id] = c.ID
			created = append(created, Created{ID: c.ID, Name: c.Name, From: g.labels})
		}
		for n, k := range g.keys {
			if err := owner[k](ctx, found[k].Key, credID); err != nil {
				notes = append(notes, fmt.Sprintf("%s could not be switched to its credential, and keeps its own secret for now: %v", g.labels[n], err))
			}
		}
	}
	// Nothing made and nothing new to say: the record stays as it is (the
	// run repeats every quarter of an hour).
	if len(created) == 0 {
		m, err := s.Migration(ctx)
		if err != nil {
			return nil, err
		}
		fresh := false
		for _, n := range notes {
			fresh = fresh || !containsString(m.Notes, n)
		}
		if !fresh {
			return nil, nil
		}
	}
	err = s.o.Repo.UpdateMigration(ctx, func(m *Migration) error {
		m.At = s.o.Now().UTC()
		m.Created = append(m.Created, created...)
		for _, n := range notes {
			if !containsString(m.Notes, n) {
				m.Notes = append(m.Notes, n)
			}
		}
		if len(created) > 0 {
			m.Dismissed = false
		}
		return nil
	})
	return created, err
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// migratedName names a credential the upgrade made: its endpoint without
// the scheme, and what it was made from: "github.com/ucesys (from app
// 221b/k8s-console)".
func migratedName(endpoint string, labels []string, taken map[string]bool) string {
	ep := endpoint
	if i := strings.Index(ep, "://"); i >= 0 {
		ep = ep[i+3:]
	}
	sorted := append([]string(nil), labels...)
	sort.Strings(sorted)
	from := strings.Join(sorted, ", ")
	if len(sorted) > 2 {
		from = sorted[0] + " and " + fmt.Sprint(len(sorted)-1) + " more"
	}
	base := clipRunes(ep+" (from "+from+")", 90)
	name := base
	for n := 2; taken[strings.ToLower(name)]; n++ {
		name = fmt.Sprintf("%s %d", base, n)
	}
	return name
}

// migratedDescription says what a credential the upgrade made was made
// from, within the 500 characters a description may have: every label,
// or the first and how many more.
func migratedDescription(labels []string) string {
	const head, most = "Made at the upgrade to 0.12.1 from ", 500
	d := head + strings.Join(labels, ", ") + "."
	if len(d) <= most || len(labels) == 0 {
		return d
	}
	more := fmt.Sprintf(" and %d more.", len(labels)-1)
	return clipRunes(head+labels[0], most-len(more)) + more
}

// clipRunes cuts s to at most n bytes on a character's boundary, ending
// it with … when cut.
func clipRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	for len(string(r))+len("…") > n {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}
