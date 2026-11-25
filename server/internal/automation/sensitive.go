package automation

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/ucesys/221b/server/internal/kv"
)

// A sensitive variable's value is never in the database (DB-71, decided
// by the owner on November 20, 2026). A name a fleet marks sensitive is
// sensitive on the whole cluster: every fleet's value of it, every node's
// own and a run's own value for it. Such a value is kept in a Kubernetes
// Secret, one a cluster, under a keyed digest of it; everywhere else (the
// variables, a change's items, a run's values, every answer of the API,
// the audit log, the log) the digest stands where the value was:
// "hidden:" and 24 hex digits. Two values of one name are the same when
// their digests are, so a change is still compared with what it found,
// and nobody who reads a row, a page or a log line learns the value. The
// digest's key is in the Secret too: without it a digest cannot be tried
// against guesses.
//
// What is kept under a digest is added before the row that names it is
// committed and removed only after the row that named it last is gone,
// so a value is never in neither place: a save that fails leaves a value
// nothing names, which costs nothing.

// hiddenPrefix starts what stands for a sensitive value.
const hiddenPrefix = "hidden:"

// Unmoved stands for a sensitive value that is still in a row: the start
// that moves the values into the Secret could not move this one. Nothing
// reads the value until it is moved; a run that needs it is refused.
const Unmoved = hiddenPrefix + "000000000000000000000000"

// MaskText is what a sensitive value reads as where a text quoted it.
const MaskText = maskText

var hiddenRE = regexp.MustCompile(`^hidden:[0-9a-f]{24}$`)

// IsHidden says whether v stands for a sensitive value.
func IsHidden(v string) bool { return hiddenRE.MatchString(v) }

// digestOf is what stands for a value of a name, under a cluster's key.
func digestOf(key []byte, name, value string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(name))
	m.Write([]byte{0})
	m.Write([]byte(value))
	return hiddenPrefix + hex.EncodeToString(m.Sum(nil)[:12])
}

// SensitiveStore keeps the sensitive values of each cluster, each under
// its digest, and the key the digests are made with. Nothing it is given
// is logged or put in an error.
type SensitiveStore interface {
	// Key is a cluster's key, made the first time it is asked for.
	Key(ctx context.Context, cluster string) ([]byte, error)
	// Put keeps values under their digests; one kept already stays.
	Put(ctx context.Context, cluster string, values map[string]string) error
	// Get is the values kept under the digests given; one not kept is
	// left out.
	Get(ctx context.Context, cluster string, digests []string) (map[string]string, error)
	// Drop removes what is kept under the digests given.
	Drop(ctx context.Context, cluster string, digests []string) error
	// Held are the digests kept, by cluster.
	Held(ctx context.Context) (map[string][]string, error)
}

// MaxSensitiveBytes is how much one cluster's sensitive values may take
// together: a Secret holds 1 MiB.
const MaxSensitiveBytes = 768 << 10

// MemorySensitive keeps the sensitive values in memory: a console whose
// connections are in memory (the simulator, a test) keeps these there
// too.
type MemorySensitive struct {
	mu       sync.Mutex
	clusters map[string]*memoryCluster
}

type memoryCluster struct {
	key    []byte
	values map[string]string
}

// NewMemorySensitive is an empty store in memory.
func NewMemorySensitive() *MemorySensitive {
	return &MemorySensitive{clusters: map[string]*memoryCluster{}}
}

func (m *MemorySensitive) of(cluster string) *memoryCluster {
	c := m.clusters[cluster]
	if c == nil {
		c = &memoryCluster{key: newDigestKey(), values: map[string]string{}}
		m.clusters[cluster] = c
	}
	return c
}

func newDigestKey() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	return key
}

func (m *MemorySensitive) Key(_ context.Context, cluster string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.of(cluster).key, nil
}

func (m *MemorySensitive) Put(_ context.Context, cluster string, values map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.of(cluster)
	for d, v := range values {
		if _, kept := c.values[d]; !kept {
			c.values[d] = v
		}
	}
	return nil
}

func (m *MemorySensitive) Get(_ context.Context, cluster string, digests []string) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	if c := m.clusters[cluster]; c != nil {
		for _, d := range digests {
			if v, ok := c.values[d]; ok {
				out[d] = v
			}
		}
	}
	return out, nil
}

func (m *MemorySensitive) Drop(_ context.Context, cluster string, digests []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c := m.clusters[cluster]; c != nil {
		for _, d := range digests {
			delete(c.values, d)
		}
	}
	return nil
}

func (m *MemorySensitive) Held(context.Context) (map[string][]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string][]string{}
	for id, c := range m.clusters {
		for d := range c.values {
			out[id] = append(out[id], d)
		}
		sort.Strings(out[id])
	}
	return out, nil
}

// ring gives one piece of work the clusters' keys, each read once.
type ring struct {
	ctx  context.Context
	s    SensitiveStore
	keys map[string][]byte
}

func newRing(ctx context.Context, s SensitiveStore) *ring {
	return &ring{ctx: ctx, s: s, keys: map[string][]byte{}}
}

// digest is what stands for a value of a name on a cluster.
func (r *ring) digest(cluster, name, value string) (string, error) {
	key, ok := r.keys[cluster]
	if !ok {
		if r.s == nil {
			return "", errNoSensitive
		}
		var err error
		if key, err = r.s.Key(r.ctx, cluster); err != nil {
			return "", fmt.Errorf("the Secret that keeps cluster %s's sensitive values could not be read: %w", cluster, err)
		}
		r.keys[cluster] = key
	}
	return digestOf(key, name, value), nil
}

func (r *ring) get(cluster string, digests []string) (map[string]string, error) {
	if len(digests) == 0 {
		return map[string]string{}, nil
	}
	if r.s == nil {
		return nil, errNoSensitive
	}
	out, err := r.s.Get(r.ctx, cluster, digests)
	if err != nil {
		return nil, fmt.Errorf("the Secret that keeps cluster %s's sensitive values could not be read: %w", cluster, err)
	}
	return out, nil
}

var errNoSensitive = fmt.Errorf("%w: this console has nowhere to keep a sensitive value", ErrInvalid)

// sealing is what hiding a document's sensitive values asks of the
// secret half, and what it found.
type sealing struct {
	// add are the values to keep, by cluster and digest, before the
	// document is saved.
	add map[string]map[string]string
	// became are the names that are sensitive on a cluster from this save
	// on, and plain the values hidden with it, by cluster: what the
	// records of the past are looked through for.
	became map[string][]string
	plain  map[string]map[string]bool
}

func newSealing() *sealing {
	return &sealing{add: map[string]map[string]string{}, became: map[string][]string{}, plain: map[string]map[string]bool{}}
}

func (s *sealing) keep(cluster, digest, value string) {
	if s.add[cluster] == nil {
		s.add[cluster] = map[string]string{}
	}
	s.add[cluster][digest] = value
}

func (s *sealing) saw(cluster, value string) {
	if value == "" {
		return
	}
	if s.plain[cluster] == nil {
		s.plain[cluster] = map[string]bool{}
	}
	s.plain[cluster][value] = true
}

// put keeps what the sealing found to keep.
func (s *sealing) put(ctx context.Context, store SensitiveStore) error {
	for _, cluster := range sortedClusters(s.add) {
		if store == nil {
			return errNoSensitive
		}
		if err := store.Put(ctx, cluster, s.add[cluster]); err != nil {
			return fmt.Errorf("the sensitive values of cluster %s could not be kept in their Secret: %w", cluster, err)
		}
	}
	return nil
}

func sortedClusters[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sensitiveNames are the names any fleet of a cluster marks sensitive.
func sensitiveNames(fleets map[string]FleetVariables) map[string]bool {
	out := map[string]bool{}
	for _, fp := range fleets {
		for _, n := range fp.Sensitive {
			out[n] = true
		}
	}
	return out
}

// SensitiveNames are the names any fleet of a cluster marks sensitive, in
// order: a value of one of them is hidden wherever it is on the cluster.
func SensitiveNames(fleets map[string]FleetVariables) []string {
	return slices.Sorted(mapKeys(sensitiveNames(fleets)))
}

// place names where a value of the variables is.
type place struct {
	fleet, node, name string
}

// eachValue calls f for every value of a cluster's variables: the fleets'
// own, and the nodes' own under All nodes. f returns the value to leave
// there.
func eachValue(fleets map[string]FleetVariables, f func(p place, v string) (string, error)) error {
	for _, id := range sortedClusters(fleets) {
		fp := fleets[id]
		for _, name := range sortedKeys(fp.Values) {
			v, err := f(place{fleet: id, name: name}, fp.Values[name])
			if err != nil {
				return err
			}
			fp.Values[name] = v
		}
		for _, node := range sortedClusters(fp.Nodes) {
			for _, name := range sortedKeys(fp.Nodes[node]) {
				v, err := f(place{fleet: id, node: node, name: name}, fp.Nodes[node][name])
				if err != nil {
					return err
				}
				fp.Nodes[node][name] = v
			}
		}
	}
	return nil
}

func (p place) of(fleets map[string]FleetVariables) (string, bool) {
	fp := fleets[p.fleet]
	if p.node != "" {
		v, ok := fp.Nodes[p.node][p.name]
		return v, ok
	}
	v, ok := fp.Values[p.name]
	return v, ok
}

func (p place) String() string {
	if p.node != "" {
		return p.name + " of node " + p.node
	}
	return p.name
}

// sealVariables hides the sensitive values of the variables as they are
// about to be saved, next, over what is kept, old. A value given for a
// sensitive name becomes its digest, and is to be kept in the Secret. A
// digest that comes to a place it was not at must stand for a value the
// Secret keeps. A name no fleet of its cluster marks any more gets its
// values back from the Secret, where they stood before the save: taking
// the mark off is what shows them, and only an approved change does it. A
// digest under a name that is not sensitive anywhere else is refused, so
// renaming a variable never shows what was hidden.
func sealVariables(r *ring, old, next *variablesRecord) (*sealing, error) {
	out := newSealing()
	for _, cluster := range sortedClusters(next.Clusters) {
		fleets, was := next.Clusters[cluster], old.Clusters[cluster]
		names, before := sensitiveNames(fleets), sensitiveNames(was)
		for n := range names {
			if !before[n] {
				out.became[cluster] = append(out.became[cluster], n)
			}
		}
		sort.Strings(out.became[cluster])
		known := map[string]bool{}
		_ = eachValue(was, func(_ place, v string) (string, error) {
			known[v] = true
			return v, nil
		})
		var check, back []string
		err := eachValue(fleets, func(p place, v string) (string, error) {
			switch {
			case v == Unmoved:
				if o, had := p.of(was); !had || o != Unmoved {
					return "", fmt.Errorf("%w: %s is sensitive: type its value", ErrInvalid, p)
				}
			case names[p.name] && !IsHidden(v):
				d, err := r.digest(cluster, p.name, v)
				if err != nil {
					return "", err
				}
				out.keep(cluster, d, v)
				out.saw(cluster, v)
				return d, nil
			case names[p.name]:
				if !known[v] {
					check = append(check, v)
				}
			case IsHidden(v):
				if o, had := p.of(was); !had || o != v {
					return "", fmt.Errorf("%w: %s holds a hidden value, which stays under a sensitive name: type a value, or mark %s sensitive", ErrInvalid, p, p.name)
				}
				back = append(back, v)
			}
			return v, nil
		})
		if err != nil {
			return nil, err
		}
		if len(check) > 0 {
			kept, err := r.get(cluster, check)
			if err != nil {
				return nil, err
			}
			for _, d := range check {
				if _, added := out.add[cluster][d]; !added && !hasKey(kept, d) {
					return nil, fmt.Errorf("%w: a sensitive value given by its hidden form is not one the console keeps: type the value", ErrInvalid)
				}
			}
		}
		if len(back) > 0 {
			kept, err := r.get(cluster, back)
			if err != nil {
				return nil, err
			}
			_ = eachValue(fleets, func(p place, v string) (string, error) {
				if plain, ok := kept[v]; ok && !names[p.name] && IsHidden(v) {
					return plain, nil
				}
				return v, nil
			})
		}
	}
	return out, nil
}

// changeMarks are the names whose sensitive mark a change puts on or
// takes off.
func changeMarks(c VariableChange) map[string]bool {
	out := map[string]bool{}
	for _, n := range c.Before.Sensitive {
		if !slices.Contains(c.After.Sensitive, n) {
			out[n] = true
		}
	}
	for _, n := range c.After.Sensitive {
		if !slices.Contains(c.Before.Sensitive, n) {
			out[n] = true
		}
	}
	return out
}

// hideValues hides the values of a fleet's variables whose names hide
// says to, and calls kept for each value it hid.
func hideValues(r *ring, cluster string, fp *FleetVariables, hide func(name string) bool, kept func(digest, value string)) error {
	one := func(name, v string) (string, error) {
		if !hide(name) || IsHidden(v) {
			return v, nil
		}
		d, err := r.digest(cluster, name, v)
		if err != nil {
			return "", err
		}
		kept(d, v)
		return d, nil
	}
	for name, v := range fp.Values {
		d, err := one(name, v)
		if err != nil {
			return err
		}
		fp.Values[name] = d
	}
	for _, m := range fp.Nodes {
		for name, v := range m {
			d, err := one(name, v)
			if err != nil {
				return err
			}
			m[name] = d
		}
	}
	return nil
}

// sealChanges hides the sensitive values of the variable changes and of
// the runs that wait for approval. names gives the names sensitive on a
// cluster now. A change hides the values of those names and of the names
// it marks itself; while it waits, what it found under a name it would
// mark stays as it is, since the variable still reads so and the two are
// compared when it is approved. The values a waiting change or a waiting
// run would use are to be kept in the Secret until it is decided; of a
// decided one only the digest is kept. plain are values hidden on a
// cluster with this save: one of them is hidden wherever it stands,
// whatever its name.
func sealChanges(r *ring, next *changesRecord, names func(cluster string) map[string]bool, plain map[string]map[string]bool) (*sealing, error) {
	out := newSealing()
	for i := range next.Changes {
		c := &next.Changes[i]
		sens, marks, waiting := names(c.Cluster), changeMarks(*c), c.State == ChangeWaiting
		err := hideValues(r, c.Cluster, &c.Before, func(name string) bool { return sens[name] || (marks[name] && !waiting) }, func(string, string) {})
		if err == nil {
			err = hideValues(r, c.Cluster, &c.After, func(name string) bool { return sens[name] || marks[name] }, func(d, v string) {
				if waiting {
					out.keep(c.Cluster, d, v)
				}
			})
		}
		if err == nil && !waiting {
			err = hidePlain(r, c.Cluster, plain[c.Cluster], c.Before.Values, c.After.Values)
			for _, m := range c.Before.Nodes {
				if err == nil {
					err = hidePlain(r, c.Cluster, plain[c.Cluster], m)
				}
			}
			for _, m := range c.After.Nodes {
				if err == nil {
					err = hidePlain(r, c.Cluster, plain[c.Cluster], m)
				}
			}
		}
		if err != nil {
			return nil, err
		}
	}
	for i := range next.Runs {
		w := &next.Runs[i]
		sens := names(w.Cluster)
		for name, v := range w.Params {
			if _, bound := Bound(v); bound || IsHidden(v) || !(sens[name] || plain[w.Cluster][Constant(v)]) {
				continue
			}
			d, err := r.digest(w.Cluster, name, Constant(v))
			if err != nil {
				return nil, err
			}
			if w.State == ChangeWaiting {
				out.keep(w.Cluster, d, Constant(v))
			}
			w.Params[name] = d
		}
	}
	return out, nil
}

// hidePlain hides, in the maps given, the values that are among plain.
func hidePlain(r *ring, cluster string, plain map[string]bool, maps ...map[string]string) error {
	if len(plain) == 0 {
		return nil
	}
	for _, m := range maps {
		for name, v := range m {
			if !plain[v] || IsHidden(v) {
				continue
			}
			d, err := r.digest(cluster, name, v)
			if err != nil {
				return err
			}
			m[name] = d
		}
	}
	return nil
}

// sealRuns hides the sensitive values the runs recorded: a run's own
// values, what each node got and what each fleet had, for the names
// sensitive on the run's cluster now, and any value among plain. Only the
// digest is kept: a run's values are a record of what it used.
func sealRuns(r *ring, next *runsRecord, names func(cluster string) map[string]bool, plain map[string]map[string]bool) error {
	for i := range next.Runs {
		run := &next.Runs[i]
		sens, seen := names(run.Cluster), plain[run.Cluster]
		if len(sens) == 0 && len(seen) == 0 {
			continue
		}
		hide := func(m map[string]string, constant bool) error {
			for name, v := range m {
				if constant {
					if _, bound := Bound(v); bound {
						continue
					}
					v = Constant(v)
				}
				if IsHidden(v) || !(sens[name] || seen[v]) {
					continue
				}
				d, err := r.digest(run.Cluster, name, v)
				if err != nil {
					return err
				}
				m[name] = d
			}
			return nil
		}
		if err := hide(run.Overrides, true); err != nil {
			return err
		}
		for j := range run.Nodes {
			if err := hide(run.Nodes[j].Values, false); err != nil {
				return err
			}
			if len(seen) > 0 {
				n := &run.Nodes[j]
				n.Tail, n.Msg, n.Error = maskPlain(n.Tail, seen), maskPlain(n.Msg, seen), maskPlain(n.Error, seen)
			}
		}
		for j := range run.Fleets {
			if err := hide(run.Fleets[j].Values, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// minMasked is the shortest value looked for in a text of the past: a
// shorter one is in too many words.
const minMasked = 4

// maskPlain blanks the values among plain that a text quotes.
func maskPlain(text string, plain map[string]bool) string {
	if text == "" {
		return text
	}
	var found []string
	for v := range plain {
		if len(v) >= minMasked && strings.Contains(text, v) {
			found = append(found, v)
		}
	}
	// The longest first: a value inside another is blanked with it.
	sort.Slice(found, func(i, j int) bool { return len(found[i]) > len(found[j]) })
	for _, v := range found {
		text = strings.ReplaceAll(text, v, maskText)
	}
	return text
}

// liveDigests are the digests whose values are still to be read, by
// cluster: every value of the variables, what a waiting change would set
// and what a waiting run would use.
func liveDigests(vars *variablesRecord, changes *changesRecord) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	note := func(cluster, v string) {
		if !IsHidden(v) {
			return
		}
		if out[cluster] == nil {
			out[cluster] = map[string]bool{}
		}
		out[cluster][v] = true
	}
	if vars != nil {
		for cluster, fleets := range vars.Clusters {
			_ = eachValue(fleets, func(_ place, v string) (string, error) {
				note(cluster, v)
				return v, nil
			})
		}
	}
	if changes != nil {
		for _, c := range changes.Changes {
			if c.State != ChangeWaiting {
				continue
			}
			for _, v := range c.After.Values {
				note(c.Cluster, v)
			}
			for _, m := range c.After.Nodes {
				for _, v := range m {
					note(c.Cluster, v)
				}
			}
		}
		for _, w := range changes.Runs {
			if w.State != ChangeWaiting {
				continue
			}
			for _, v := range w.Params {
				note(w.Cluster, v)
			}
		}
	}
	return out
}

// gone are the digests live in old that are not live in next, by cluster.
func gone(old, next map[string]map[string]bool) map[string][]string {
	out := map[string][]string{}
	for cluster, ds := range old {
		for d := range ds {
			if !next[cluster][d] && d != Unmoved {
				out[cluster] = append(out[cluster], d)
			}
		}
		sort.Strings(out[cluster])
	}
	return out
}

func hasKey(m map[string]string, k string) bool {
	_, ok := m[k]
	return ok
}

func clone[T any](v T) T {
	var out T
	b, _ := json.Marshal(v)
	_ = json.Unmarshal(b, &out)
	return out
}

// --- The store's side ---

// SetSensitive has the store keep the sensitive values in s. With tables
// true the documents the store saves are rows of a database that hide the
// values themselves, in the transaction that writes the rows (NewTables);
// otherwise the store hides them before it saves a document.
func (st *Store) SetSensitive(s SensitiveStore, tables bool, log *slog.Logger) {
	st.sens, st.inTables = s, tables
	if log != nil {
		st.log = log
	}
}

// Sensitive is where the store keeps the sensitive values.
func (st *Store) Sensitive() SensitiveStore { return st.sens }

// sealDoc hides the sensitive values of a document the store is about to
// save, when the store does that itself, and says what has to be kept.
func (st *Store) sealDoc(ctx context.Context, key string, old, next any) (*sealing, error) {
	r := newRing(ctx, st.sens)
	switch key {
	case VariablesKey:
		o, n := old.(*variablesRecord), next.(*variablesRecord)
		o.nodesOwn()
		n.nodesOwn()
		return sealVariables(r, o, n)
	case ChangesKey:
		names, err := st.namesNow(ctx)
		if err != nil {
			return nil, err
		}
		return sealChanges(r, next.(*changesRecord), names, nil)
	case RunsKey:
		names, err := st.namesNow(ctx)
		if err != nil {
			return nil, err
		}
		return newSealing(), sealRuns(r, next.(*runsRecord), names, nil)
	}
	return newSealing(), nil
}

// namesNow gives the names sensitive on each cluster as the variables
// say now.
func (st *Store) namesNow(ctx context.Context) (func(cluster string) map[string]bool, error) {
	var rec variablesRecord
	if _, err := st.load(ctx, VariablesKey, &rec); err != nil {
		return nil, err
	}
	rec.nodesOwn()
	return func(cluster string) map[string]bool { return sensitiveNames(rec.Clusters[cluster]) }, nil
}

// sealPast hides, in the changes and the runs kept, the values of the
// names that became sensitive with a save of the variables, when the
// store hides the values itself. Called with the store's lock held.
func (st *Store) sealPast(ctx context.Context, s *sealing) error {
	if len(s.became) == 0 {
		return nil
	}
	names, err := st.namesNow(ctx)
	if err != nil {
		return err
	}
	r := newRing(ctx, st.sens)
	if err := resave(ctx, st, ChangesKey, func(rec *changesRecord) (*sealing, error) { return sealChanges(r, rec, names, s.plain) }); err != nil {
		return err
	}
	return resave(ctx, st, RunsKey, func(rec *runsRecord) (*sealing, error) { return newSealing(), sealRuns(r, rec, names, s.plain) })
}

// resave reads a document, has f hide what it must and saves it when it
// changed. Called with the store's lock held.
func resave[T any](ctx context.Context, st *Store, key string, f func(*T) (*sealing, error)) error {
	for try := 0; try < changeTries; try++ {
		var v T
		version, err := st.load(ctx, key, &v)
		if err != nil {
			return err
		}
		was, _ := json.Marshal(v)
		s, err := f(&v)
		if err != nil {
			return err
		}
		b, _ := json.Marshal(v)
		if string(b) == string(was) {
			return nil
		}
		if err := s.put(ctx, st.sens); err != nil {
			return err
		}
		if _, err = st.backend.SaveKey(ctx, key, b, version); err == nil {
			return nil
		} else if !errors.Is(err, kv.ErrConflict) {
			return err
		}
	}
	return fmt.Errorf("%s kept changing while its sensitive values were hidden", key)
}

// collect removes from the Secret the values no row reads any more: of
// the digests given, those that neither the variables nor a waiting
// change or run name. It is done after the save that stopped naming them,
// and never fails it: a value left behind is said in the log, by how many
// and of which cluster.
func (st *Store) collect(ctx context.Context, was map[string][]string) {
	if len(was) == 0 || st.sens == nil {
		return
	}
	drop := func(live map[string]map[string]bool) error {
		for _, cluster := range sortedClusters(was) {
			var out []string
			for _, d := range was[cluster] {
				if !live[cluster][d] {
					out = append(out, d)
				}
			}
			if len(out) == 0 {
				continue
			}
			if err := st.sens.Drop(ctx, cluster, out); err != nil {
				return fmt.Errorf("cluster %s: %w", cluster, err)
			}
		}
		return nil
	}
	var err error
	if st.collectIn != nil {
		err = st.collectIn(ctx, drop)
	} else {
		var vars variablesRecord
		var changes changesRecord
		if _, err = st.load(ctx, VariablesKey, &vars); err == nil {
			if _, err = st.load(ctx, ChangesKey, &changes); err == nil {
				vars.nodesOwn()
				err = drop(liveDigests(&vars, &changes))
			}
		}
	}
	if err != nil {
		st.logger().Warn("sensitive values that nothing reads any more were not removed from their Secret; they stay there, unread", "err", err)
	}
}

func (st *Store) logger() *slog.Logger {
	if st.log != nil {
		return st.log
	}
	return slog.Default()
}

// HideRunValues gives a run's own values as they are recorded: a value
// given for a name that is sensitive on the cluster is its digest. typed
// are the values so hidden, by digest, for the run to use; nothing keeps
// them (KeepValues does, for a run that waits).
func (st *Store) HideRunValues(ctx context.Context, cluster string, params map[string]string) (hidden, typed map[string]string, err error) {
	if len(params) == 0 {
		return params, nil, nil
	}
	names, err := st.namesNow(ctx)
	if err != nil {
		return nil, nil, err
	}
	sens := names(cluster)
	r := newRing(ctx, st.sens)
	hidden, typed = map[string]string{}, map[string]string{}
	for name, v := range params {
		hidden[name] = v
		if IsHidden(v) && !sens[name] {
			// A digest stands for a sensitive value, and stays under a
			// sensitive name: given under another, the run would hand the
			// value to a parameter nobody weighed as sensitive (FU-05).
			return nil, nil, fmt.Errorf("%w: %s is given a hidden value, which stays under a sensitive name: type a value, or bind %s to the variable (var:NAME)", ErrInvalid, name, name)
		}
		if _, bound := Bound(v); bound || IsHidden(v) || !sens[name] {
			continue
		}
		d, err := r.digest(cluster, name, Constant(v))
		if err != nil {
			return nil, nil, err
		}
		hidden[name], typed[d] = d, Constant(v)
	}
	return hidden, typed, nil
}

// KeepValues keeps values under their digests until nothing waits for
// them: the values a run that waits for approval would use.
func (st *Store) KeepValues(ctx context.Context, cluster string, typed map[string]string) error {
	if len(typed) == 0 {
		return nil
	}
	if st.sens == nil {
		return errNoSensitive
	}
	if err := st.sens.Put(ctx, cluster, typed); err != nil {
		return fmt.Errorf("the run's sensitive values could not be kept in the Secret: %w", err)
	}
	return nil
}

// ForgetValues removes values kept for a run that did not come to wait,
// unless something else names them.
func (st *Store) ForgetValues(ctx context.Context, cluster string, typed map[string]string) {
	if len(typed) == 0 {
		return
	}
	digests := make([]string, 0, len(typed))
	for d := range typed {
		digests = append(digests, d)
	}
	sort.Strings(digests)
	st.mu.Lock()
	defer st.mu.Unlock()
	st.collect(ctx, map[string][]string{cluster: digests})
}

// Reveal is the values kept under the digests given, for a run to use or
// a backup to seal: never for a page. A digest the Secret does not keep
// is left out.
func (st *Store) Reveal(ctx context.Context, cluster string, digests []string) (map[string]string, error) {
	return newRing(ctx, st.sens).get(cluster, digests)
}
