package issues

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
)

// asKept is a record as a ConfigMap gives it back: through its own JSON.
func asKept[T any](t *testing.T, v T) T {
	t.Helper()
	var out T
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func sameAs[T any](t *testing.T, what string, got []byte, want T) {
	t.Helper()
	var g T
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, want) {
		w, _ := json.Marshal(want)
		t.Fatalf("%s: the database reads back\n%s\nand a ConfigMap\n%s", what, got, w)
	}
}

// Every field of a case is a column and comes back as a ConfigMap gives
// it, with its messages, watchers and approvers; the object has one
// version, which every save moves on and a stale save does not pass; a
// deleted object is gone with what hangs from it.
func TestCaseObjectsInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	s := NewCasesPG(d, nil)
	ctx := context.Background()
	var full Case
	dbtest.Fill(&full)
	full.Number = 12
	want := asKept(t, full)
	raw, _ := json.Marshal(full)
	if _, _, err := s.Load(ctx, "12"); !errors.Is(err, kv.ErrNotFound) {
		t.Fatalf("an object that is not there: %v", err)
	}
	if _, err := s.Save(ctx, "12", CaseKey, raw, "7"); !errors.Is(err, kv.ErrConflict) {
		t.Fatalf("a save over a version of an object that is not there: %v", err)
	}
	v1, err := s.Save(ctx, "12", CaseKey, raw, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(ctx, "12", CaseKey, raw, ""); !errors.Is(err, kv.ErrConflict) {
		t.Fatalf("making an object that is there: %v", err)
	}
	keys, v, err := s.Load(ctx, "12")
	if err != nil || v != v1 || len(keys) != 1 {
		t.Fatalf("load: %d keys at %q, want %q: %v", len(keys), v, v1, err)
	}
	sameAs(t, "the case", keys[CaseKey], want)
	// A plain case, as one just opened: nothing optional set.
	plain := Case{Number: 13, Reporter: Person{ID: "ann"}, Status: "new", Created: full.Created, Updated: full.Created, StatusAt: full.Created, Priority: 3}
	rawPlain, _ := json.Marshal(plain)
	if _, err := s.Save(ctx, "13", CaseKey, rawPlain, ""); err != nil {
		t.Fatal(err)
	}
	keys, _, _ = s.Load(ctx, "13")
	sameAs(t, "a plain case", keys[CaseKey], asKept(t, plain))
	// It changes: fewer messages, no watcher.
	full.Messages, full.Watchers, full.Title = full.Messages[:1], nil, "Web is down"
	raw, _ = json.Marshal(full)
	v2, err := s.Save(ctx, "12", CaseKey, raw, v1)
	if err != nil || v2 == v1 {
		t.Fatalf("second save: %q, %v", v2, err)
	}
	if _, err := s.Save(ctx, "12", CaseKey, raw, v1); !errors.Is(err, kv.ErrConflict) {
		t.Fatalf("a save over a stale version: %v", err)
	}
	keys, _, _ = s.Load(ctx, "12")
	sameAs(t, "the case, changed", keys[CaseKey], asKept(t, full))
	if _, err := s.Save(ctx, "12", "other.json", []byte(`{}`), v2); err == nil {
		t.Fatal("a key nobody keeps was saved")
	}
	if names, err := s.List(ctx); err != nil || len(names) != 2 || names[0] != "12" || names[1] != "13" {
		t.Fatalf("list: %v %v", names, err)
	}
	// The index's lines are the cases' own.
	idx, found, err := readIndex(ctx, d.Pool())
	if err != nil || !found || len(idx.Lines) != 2 {
		t.Fatalf("the index: %d lines, %v", len(idx.Lines), err)
	}
	for i, c := range []Case{asKept(t, full), asKept(t, plain)} {
		if l := asKept(t, lineOf(&c)); !reflect.DeepEqual(asKept(t, idx.Lines[i]), l) {
			a, _ := json.Marshal(idx.Lines[i])
			b, _ := json.Marshal(l)
			t.Fatalf("line of C-%d:\n%s\nwant\n%s", c.Number, a, b)
		}
	}
	if err := s.Delete(ctx, "12"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "12"); err != nil {
		t.Fatalf("deleting what is gone: %v", err)
	}
	var n int
	if err := d.Pool().QueryRow(ctx, "SELECT (SELECT count(*) FROM case_messages) + (SELECT count(*) FROM case_people) + (SELECT count(*) FROM cases)").Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d rows left, want the plain case's: %v", n, err)
	}
	// What the case ConfigMaps held is copied once.
	old := &kv.MemoryObjects{}
	_, _ = old.Save(ctx, "12", CaseKey, raw, "")
	_, _ = old.Save(ctx, "13", CaseKey, rawPlain, "")
	fresh := NewCasesPG(dbtest.Open(t), nil)
	if n, err := fresh.Import(ctx, old, "test"); err != nil || n != 2 {
		t.Fatalf("copy: %d %v", n, err)
	}
	if n, err := fresh.Import(ctx, old, "test"); err != nil || n != 0 {
		t.Fatalf("second copy: %d %v", n, err)
	}
	keys, _, err = fresh.Load(ctx, "12")
	if err != nil {
		t.Fatal(err)
	}
	sameAs(t, "the case, copied", keys[CaseKey], asKept(t, full))
}

// The index, the known issues and the settings are one set: every field
// of a known issue and of a service comes back as a ConfigMap gives it;
// the index keeps its counter and the archived counts; the three are at
// one version.
func TestIssueDocumentsInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	set := NewSetPG(d)
	ctx := context.Background()
	var known knownDoc
	dbtest.Fill(&known)
	var config configDoc
	dbtest.Fill(&config)
	index := indexDoc{Next: 14, Archived: map[string]int{"2026-10": 3, "2026-11": 1}}
	version := "0"
	save := func(key string, doc any) {
		t.Helper()
		raw, _ := json.Marshal(doc)
		v, err := set.SaveKey(ctx, key, raw, version)
		if err != nil {
			t.Fatalf("saving %s: %v", key, err)
		}
		version = v
	}
	save(KnownKey, known)
	save(ConfigKey, config)
	save(IndexKey, index)
	for key, want := range map[string]any{KnownKey: asKept(t, known), ConfigKey: asKept(t, config), IndexKey: asKept(t, index)} {
		got, v, err := set.LoadKey(ctx, key)
		if err != nil || v != version {
			t.Fatalf("%s at %q, want %q: %v", key, v, version, err)
		}
		switch w := want.(type) {
		case knownDoc:
			sameAs(t, key, got, w)
		case configDoc:
			sameAs(t, key, got, w)
		case indexDoc:
			sameAs(t, key, got, w)
		}
	}
	// One known issue changes, one goes; a service goes.
	known.Known[0].Title, known.Known = "It is slow", known.Known[:1]
	config.Services = config.Services[1:]
	save(KnownKey, known)
	save(ConfigKey, config)
	got, _, _ := set.LoadKey(ctx, KnownKey)
	sameAs(t, "the known issues, changed", got, asKept(t, known))
	got, _, _ = set.LoadKey(ctx, ConfigKey)
	sameAs(t, "the settings, changed", got, asKept(t, config))
	var n int
	if err := d.Pool().QueryRow(ctx, "SELECT (SELECT count(*) FROM known_issues) + (SELECT count(*) FROM services)").Scan(&n); err != nil || n != 2 {
		t.Fatalf("%d known issues and services, want 2: %v", n, err)
	}
}
