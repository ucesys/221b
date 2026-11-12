// Package backup is what a backup of the console is made of, and where it
// goes (ADR-80 to ADR-82).
//
// A backup is one gzipped JSON file: a header (format, version, when, by
// whom, what is never in it) and one key per part. Each part belongs to
// the package that keeps its records and is registered here with a name,
// how to export it and how to restore it:
//
//	backup.Register(backup.Part{
//		Name:  "jobs",
//		Label: "Jobs",
//		How:   "are added or replaced, by name; their runs are not restored",
//		Order: 300,
//		Export: func(ctx context.Context, env *backup.Env) (any, error) {
//			return store.All(ctx)
//		},
//		Restore: func(ctx context.Context, raw json.RawMessage, env *backup.Env) (int, error) {
//			var list []jobs.Job
//			if err := json.Unmarshal(raw, &list); err != nil {
//				return 0, err
//			}
//			…
//		},
//	})
//
// The rules every part keeps: an export writes each secret sealed with
// the install's backup key (Env.Seal, ADR-137), never in clear, and the
// placeholder when the install keeps no key; a restore takes a secret
// back only where the console has none (Env.Open), lists every secret it
// could not open to set again by hand, with where it is set, refuses a
// secret in clear where a sealed value or a placeholder belongs, and
// holds what it brings back to the checks the console's own pages make. A restore applies only the parts a
// SuperAdmin picks, in Order, and reports what each applied and what is
// left to do by hand.
//
// The file's format stays 221b-backup/2 as parts are added: a console
// reading a file with a part it does not know says so and leaves it, and
// a part missing from a file is simply not restored.
package backup

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"
)

// Format marks a backup file; a restore reads it and FormatV1, which left
// the secrets out rather than holding placeholders.
const (
	Format   = "221b-backup/2"
	FormatV1 = "221b-backup/1"
)

// Placeholder stands where a secret was, in a backup taken without a key
// and in every backup before ADR-137 (clusters.Placeholder).
const Placeholder = "<secret: not in the backup>"

// ErrSecretInFile is a restore's refusal of a secret where a backup holds
// a placeholder: a file is no way around the pages that take secrets.
var ErrSecretInFile = errors.New("the file holds a secret where a backup holds a placeholder")

// ErrNotApplied is what a part's Restore returns when it applied nothing
// and has said why with Env.Note: the report then counts nothing for it.
var ErrNotApplied = errors.New("not applied")

// Secret is the placeholder when there is a secret, nothing when there is
// none: what a part that never carries its secret writes. A part that
// carries its secrets writes Env.Seal.
func Secret(value string) string {
	if value == "" {
		return ""
	}
	return Placeholder
}

// Keep is the secret a restore keeps for a field: the console's own (have)
// when the file holds the placeholder, a sealed value or nothing, so a
// file never replaces a secret here. A file holding a value in clear
// there is ErrSecretInFile.
func Keep(have, inFile string) (string, error) {
	return have, CheckSecret(inFile)
}

// Part is one package's share of a backup.
type Part struct {
	// Name is the part's key in the file and in a restore's choice:
	// lowerCamelCase, unique, and kept once released, since files carry it.
	Name string
	// Label names the part on the page and in the report's notes.
	Label string
	// How says, after the label, what restoring it does: "are added or
	// replaced, by name".
	How string
	// Order places the part in the file and in a restore: lower first.
	// The console's own parts take 10 to 120; another package takes 200
	// or more, or less to be restored before a part it depends on.
	Order int
	// Optional parts are exported only when asked (Env.Optional), such as
	// chat conversations.
	Optional bool
	// LeftOut are lines for the file's list of what it never holds.
	LeftOut []string
	// Export returns the part as a value encoded to JSON, each secret
	// in it env.Seal's; nil, or an empty list or map, leaves the part
	// out of the file. An error fails the whole backup.
	Export func(ctx context.Context, env *Env) (any, error)
	// Restore applies the part from the file, when picked, and returns how
	// many records it applied. It notes with env.Note what it could not
	// apply and what is left to do by hand. An error means nothing was
	// applied: the report says "<Label>: <error>", or nothing more for
	// ErrNotApplied. nil: the part is kept in the file but not restored.
	Restore func(ctx context.Context, raw json.RawMessage, env *Env) (int, error)
	// Check runs on every restore of a file that holds the part, picked or
	// not, after the parts restored: it notes what the file holds that
	// stays undone, such as connections not brought back. Optional.
	Check func(ctx context.Context, raw json.RawMessage, env *Env)
}

// Info is a part as the page lists it.
type Info struct {
	Name       string `json:"name"`
	Label      string `json:"label"`
	How        string `json:"how,omitempty"`
	Optional   bool   `json:"optional,omitempty"`
	Restorable bool   `json:"restorable"`
}

// Env is what an export or a restore is given.
type Env struct {
	// By names who takes or restores the backup; a scheduled backup is
	// "scheduled backup".
	By string
	// Taken is when the backup is (or, restoring, was) taken.
	Taken time.Time
	// Version is the console's version that writes (or wrote) the file.
	Version string
	// Optional asks an export for the optional parts too.
	Optional bool
	mu       sync.Mutex
	picked   map[string]bool
	notes    []string
	// key seals an export's secrets, or opens a restore's: nil writes
	// placeholders, and opens nothing.
	key *SecretsKey
	// part is the label of the part being restored, for SetAgain.
	part    string
	secrets []SecretToSet
}

// Note adds a line to the restore's report.
func (e *Env) Note(format string, a ...any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.notes = append(e.notes, fmt.Sprintf(format, a...))
}

// Picked says whether a restore applies the part named.
func (e *Env) Picked(name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.picked[name]
}

// Registry holds the parts. The console has one, Default; tests make their
// own.
type Registry struct {
	mu        sync.Mutex
	parts     []Part
	leftOut   []string
	keys      KeySource
	keySecret string
}

// NewRegistry makes an empty registry.
func NewRegistry() *Registry { return &Registry{} }

// Default is the console's registry: packages register their parts in it
// when they are set up, and the backup page and the scheduled backups
// read it.
var Default = NewRegistry()

// Register adds a part to Default.
func Register(p Part) { Default.Register(p) }

// header names the file's own keys, which no part may take.
var header = []string{"format", "version", "taken", "by", "leftOut", "secrets"}

// SetKey gives the registry the install's backup key (ADR-137), kept in
// the Secret named secret: every export seals its secrets with it, and a
// restore opens them with it. Without one, exports write placeholders.
func (r *Registry) SetKey(secret string, keys KeySource) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keySecret, r.keys = secret, keys
}

// Key is the install's backup key, nil when it keeps none, and the name of
// its Secret.
func (r *Registry) Key(ctx context.Context) (*SecretsKey, string, error) {
	r.mu.Lock()
	keys, secret := r.keys, r.keySecret
	r.mu.Unlock()
	if keys == nil {
		return nil, "", nil
	}
	k, err := keys(ctx)
	if err != nil {
		return nil, secret, fmt.Errorf("the backup key (Secret %s): %w", secret, err)
	}
	return k, secret, nil
}

// CheckKey says whether this install's key opens a file's secrets. A key
// that cannot be read opens nothing, as no key.
func (r *Registry) CheckKey(ctx context.Context, f *File) KeyCheck {
	var c KeyCheck
	if f.Secrets != nil && f.Secrets.Key != "" {
		c.Sealed, c.File = true, f.Secrets.Key
	}
	if k, _, err := r.Key(ctx); err == nil && k != nil {
		c.Here = k.Fingerprint()
	}
	c.Same = c.Sealed && c.Here == c.File
	return c
}

// Register adds a part. A part without a name, label or export, or with a
// name taken, is a mistake in the code, and panics.
func (r *Registry) Register(p Part) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p.Name == "" || p.Label == "" || p.Export == nil {
		panic("backup: a part needs a name, a label and an export")
	}
	if slices.Contains(header, p.Name) || slices.ContainsFunc(r.parts, func(x Part) bool { return x.Name == p.Name }) {
		panic("backup: the part " + strconv.Quote(p.Name) + " is registered twice, or names the file's header")
	}
	r.parts = append(r.parts, p)
	slices.SortStableFunc(r.parts, func(a, b Part) int { return cmp.Compare(a.Order, b.Order) })
}

// LeftOut adds lines to the list of what a backup never holds that belong
// to no one part.
func (r *Registry) LeftOut(lines ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.leftOut = append(r.leftOut, lines...)
}

// Parts are the parts, in order.
func (r *Registry) Parts() []Part {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.parts)
}

// Infos are the parts as the page lists them.
func (r *Registry) Infos() []Info {
	var out []Info
	for _, p := range r.Parts() {
		out = append(out, Info{Name: p.Name, Label: p.Label, How: p.How, Optional: p.Optional, Restorable: p.Restore != nil})
	}
	return out
}

// leftOutLines is the file's list of what it never holds: the rule for
// secrets first, then the registry's lines, then each part's.
func (r *Registry) leftOutLines(key *SecretsKey) []string {
	out := []string{"any secret: each is the placeholder " + strconv.Quote(Placeholder) + ", and a restore never puts one over a secret the console has"}
	if key != nil {
		out[0] = "any secret in clear: each is sealed with this install's backup key (" + Cipher + ", key " + key.Fingerprint() + "); a restore with the same key brings it back where the console has none, and with another key lists it to set again"
	}
	r.mu.Lock()
	out = append(out, r.leftOut...)
	r.mu.Unlock()
	for _, p := range r.Parts() {
		out = append(out, p.LeftOut...)
	}
	return out
}

// encode is JSON as a backup writes it: a placeholder as written, not
// <, for people reading the file.
func encode(v any) (json.RawMessage, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(b.Bytes()), nil
}

// empty says whether a part's JSON holds nothing.
func empty(raw json.RawMessage) bool {
	switch string(bytes.TrimSpace(raw)) {
	case "", "null", "[]", "{}", `""`:
		return true
	}
	return false
}

// Count is how many records a part's JSON holds: a list's or a map's
// entries, or one.
func Count(raw json.RawMessage) int {
	if empty(raw) {
		return 0
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		return len(list)
	}
	var m map[string]json.RawMessage
	if raw = bytes.TrimSpace(raw); len(raw) > 0 && raw[0] == '{' && json.Unmarshal(raw, &m) == nil && !looksLikeRecord(m) {
		return len(m)
	}
	return 1
}

// looksLikeRecord tells an object that is one record (settings) from a map
// of records (pairings by node): a map's values are all objects.
func looksLikeRecord(m map[string]json.RawMessage) bool {
	for _, v := range m {
		if v = bytes.TrimSpace(v); len(v) == 0 || v[0] != '{' {
			return true
		}
	}
	return false
}

// Export makes a backup of every part (the optional ones when env asks).
func (r *Registry) Export(ctx context.Context, env *Env) (*File, error) {
	key, _, err := r.Key(ctx)
	if err != nil {
		return nil, err
	}
	env.key = key
	f := &File{Format: Format, Version: env.Version, Taken: env.Taken.UTC(), By: env.By, LeftOut: r.leftOutLines(key), Parts: map[string]json.RawMessage{}}
	if key != nil {
		f.Secrets = &Sealing{Cipher: Cipher, Key: key.Fingerprint()}
	}
	for _, p := range r.Parts() {
		if p.Optional && !env.Optional {
			continue
		}
		v, err := p.Export(ctx, env)
		if err != nil {
			return nil, err
		}
		raw, err := encode(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.Label, err)
		}
		if empty(raw) {
			continue
		}
		f.Parts[p.Name] = raw
		f.order = append(f.order, p.Name)
	}
	return f, nil
}

// Report says what a restore did.
type Report struct {
	// Restored counts what each part applied.
	Restored map[string]int `json:"restored"`
	// Notes say what could not be applied, and what is left to do by hand.
	Notes []string `json:"notes"`
	// Secrets are the secrets not brought back, each to set again by hand
	// where it says (ADR-137).
	Secrets []SecretToSet `json:"secrets"`
}

// Restore applies the parts picked that the file holds, in order, then
// runs every part's Check, and reports. A part picked that this console
// does not know is named in the notes and left. The file's secrets are
// opened only with the key they were sealed with: with another, or none,
// each is listed to set again (the caller asks first, CheckKey).
func (r *Registry) Restore(ctx context.Context, f *File, picked map[string]bool, env *Env) Report {
	rep := Report{Restored: map[string]int{}, Notes: []string{}, Secrets: []SecretToSet{}}
	var key *SecretsKey
	var why string
	if f.Secrets != nil {
		k, _, err := r.Key(ctx)
		switch {
		case err != nil:
			why = fmt.Sprintf("the backup's secrets were not opened: %v", err)
		case k == nil:
			why = "the backup's secrets are sealed with a backup key, and this install keeps none: each is listed to set again"
		case k.Fingerprint() != f.Secrets.Key:
			why = fmt.Sprintf("the backup's secrets are sealed with another backup key (%s; this install's is %s): each is listed to set again", f.Secrets.Key, k.Fingerprint())
		default:
			key = k
		}
	}
	env.mu.Lock()
	env.key, env.secrets = key, nil
	env.picked, env.notes = map[string]bool{}, nil
	if why != "" {
		env.notes = append(env.notes, why)
	}
	for k, v := range picked {
		if v {
			env.picked[k] = true
		}
	}
	env.mu.Unlock()
	parts := r.Parts()
	known := map[string]bool{}
	for _, p := range parts {
		known[p.Name] = true
	}
	for _, name := range f.Names() {
		if picked[name] && !known[name] {
			env.Note("%s: this console does not know this part of the backup (a newer version wrote it) and leaves it", name)
		}
	}
	for _, p := range parts {
		raw, ok := f.Parts[p.Name]
		if !picked[p.Name] || !ok || empty(raw) || p.Restore == nil {
			continue
		}
		env.mu.Lock()
		env.part = p.Label
		env.mu.Unlock()
		n, err := p.Restore(ctx, raw, env)
		switch {
		case errors.Is(err, ErrNotApplied):
		case err != nil:
			env.Note("%s: %v", p.Label, err)
		default:
			rep.Restored[p.Name] = n
		}
	}
	for _, p := range parts {
		if raw, ok := f.Parts[p.Name]; ok && !empty(raw) && p.Check != nil {
			env.mu.Lock()
			env.part = p.Label
			env.mu.Unlock()
			p.Check(ctx, raw, env)
		}
	}
	env.mu.Lock()
	rep.Notes = append(rep.Notes, env.notes...)
	rep.Secrets = append(rep.Secrets, env.secrets...)
	env.mu.Unlock()
	return rep
}
