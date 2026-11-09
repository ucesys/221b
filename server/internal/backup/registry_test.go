package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// compact is a part's JSON on one line, as a test compares it.
func compact(raw json.RawMessage) string {
	var b bytes.Buffer
	_ = json.Compact(&b, raw)
	return b.String()
}

// secretRecord is a record of a made-up package with a secret in it.
type secretRecord struct {
	Name  string `json:"name"`
	Token string `json:"token,omitempty"`
}

// tokens is that package's store.
type tokens map[string]string

// part registers the made-up package as a package would: the export writes
// the placeholder for each token, the restore keeps the token here.
func (t tokens) part() Part {
	return Part{
		Name: "tokens", Label: "Tokens", How: "are added or updated; a token here stays", Order: 200,
		LeftOut: []string{"the tokens' values"},
		Export: func(ctx context.Context, env *Env) (any, error) {
			var out []secretRecord
			for n, tok := range t {
				out = append(out, secretRecord{Name: n, Token: Secret(tok)})
			}
			return out, nil
		},
		Restore: func(ctx context.Context, raw json.RawMessage, env *Env) (int, error) {
			var list []secretRecord
			if err := json.Unmarshal(raw, &list); err != nil {
				return 0, err
			}
			n := 0
			for _, r := range list {
				tok, err := Keep(t[r.Name], r.Token)
				if err != nil {
					env.Note("token %s is not restored: %v", r.Name, err)
					continue
				}
				if tok == "" && r.Token != "" {
					env.Note("token %s: paste it again", r.Name)
				}
				t[r.Name] = tok
				n++
			}
			return n, nil
		},
	}
}

// A part's secrets go out as placeholders, and a restore keeps the
// secrets the console has, brings missing records back without one,
// refuses a secret in the file, and says so.
func TestARegisteredPartKeepsItsSecrets(t *testing.T) {
	ctx := context.Background()
	here := tokens{"ci": "ghp_here", "docs": "ghp_docs"}
	r := NewRegistry()
	r.Register(here.part())
	r.LeftOut("sign-in sessions")
	f, err := r.Export(ctx, &Env{By: "ops", Taken: time.Date(2026, 11, 5, 3, 0, 0, 0, time.UTC), Version: "0.12.0"})
	if err != nil {
		t.Fatal(err)
	}
	gz, err := f.Gzip()
	if err != nil {
		t.Fatal(err)
	}
	back, err := Read(gz, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := compact(back.Parts["tokens"])
	if strings.Contains(raw, "ghp_") || !strings.Contains(raw, `"token":"<secret: not in the backup>"`) {
		t.Fatalf("the part in the file: %s", raw)
	}
	if back.Format != Format || back.By != "ops" || f.Name() != "221b-backup-2026-11-05-030000.json.gz" || strings.Join(back.LeftOut, "|") != "any secret: each is the placeholder \"<secret: not in the backup>\", and a restore never puts one over a secret the console has|sign-in sessions|the tokens' values" {
		t.Fatalf("the header: %+v", back)
	}

	// Into a console that has ci with another token, and no docs; the file
	// also has a token in clear, which is refused.
	there := tokens{"ci": "ghp_there"}
	r2 := NewRegistry()
	r2.Register(there.part())
	var list []secretRecord
	_ = json.Unmarshal(back.Parts["tokens"], &list)
	list = append(list, secretRecord{Name: "leak", Token: "ghp_clear"})
	back.Parts["tokens"], _ = json.Marshal(list)
	back.Parts["jobs"] = json.RawMessage(`[{"name":"nightly"}]`)
	rep := r2.Restore(ctx, back, map[string]bool{"tokens": true, "jobs": true}, &Env{By: "ops"})
	notes := strings.Join(rep.Notes, "\n")
	if rep.Restored["tokens"] != 2 || there["ci"] != "ghp_there" || there["docs"] != "" || there["leak"] != "" {
		t.Fatalf("restored %+v, tokens %v", rep.Restored, there)
	}
	for _, want := range []string{"token docs: paste it again", "token leak is not restored: the file holds a secret", "jobs: this console does not know this part"} {
		if !strings.Contains(notes, want) {
			t.Errorf("the notes lack %q:\n%s", want, notes)
		}
	}
	if _, ok := rep.Restored["jobs"]; ok {
		t.Error("an unknown part was counted")
	}
}

// Optional parts are exported only when asked; an empty part is left out;
// a part not picked is not restored, but its Check runs; ErrNotApplied
// counts nothing and adds no note of its own.
func TestPartsOptionalEmptyAndUnpicked(t *testing.T) {
	ctx := context.Background()
	r := NewRegistry()
	checked, restored := 0, 0
	r.Register(Part{Name: "chats", Label: "Chat conversations", Optional: true, Order: 300, Export: func(context.Context, *Env) (any, error) { return []string{"hello"}, nil }})
	r.Register(Part{Name: "none", Label: "Nothing", Order: 250, Export: func(context.Context, *Env) (any, error) { return map[string]int{}, nil }})
	r.Register(Part{Name: "jobs", Label: "Jobs", Order: 200,
		Export: func(context.Context, *Env) (any, error) { return []string{"nightly"}, nil },
		Restore: func(ctx context.Context, raw json.RawMessage, env *Env) (int, error) {
			restored++
			env.Note("jobs: off here")
			return 0, ErrNotApplied
		},
		Check: func(ctx context.Context, raw json.RawMessage, env *Env) {
			checked++
			if !env.Picked("jobs") {
				env.Note("the jobs were not restored")
			}
		}})
	f, _ := r.Export(ctx, &Env{})
	if strings.Join(f.Names(), ",") != "jobs" {
		t.Fatalf("without the optional parts: %v", f.Names())
	}
	f, _ = r.Export(ctx, &Env{Optional: true})
	if strings.Join(f.Names(), ",") != "jobs,chats" || Count(f.Parts["jobs"]) != 1 {
		t.Fatalf("with them: %v", f.Names())
	}
	rep := r.Restore(ctx, f, map[string]bool{}, &Env{})
	if restored != 0 || checked != 1 || strings.Join(rep.Notes, "|") != "the jobs were not restored" {
		t.Fatalf("not picked: %d %d %v", restored, checked, rep.Notes)
	}
	rep = r.Restore(ctx, f, map[string]bool{"jobs": true}, &Env{})
	if _, ok := rep.Restored["jobs"]; ok || strings.Join(rep.Notes, "|") != "jobs: off here" {
		t.Fatalf("not applied: %+v", rep)
	}
	if infos := r.Infos(); len(infos) != 3 || infos[0].Name != "jobs" || !infos[0].Restorable || infos[2].Restorable || !infos[2].Optional {
		t.Fatalf("infos %+v", infos)
	}
}

func TestRegisteringTwiceIsAMistake(t *testing.T) {
	r := NewRegistry()
	p := Part{Name: "a", Label: "A", Export: func(context.Context, *Env) (any, error) { return nil, nil }}
	r.Register(p)
	for _, bad := range []Part{p, {Name: "format", Label: "F", Export: p.Export}, {Name: "b", Label: "B"}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%q was taken", bad.Name)
				}
			}()
			r.Register(bad)
		}()
	}
}

// A file of either format reads, gzipped or not; anything else is refused
// saying why; an export's error fails the backup.
func TestReadingFiles(t *testing.T) {
	if f, err := Read([]byte(`{"format":"221b-backup/1","version":"0.10.2","taken":"2026-10-30T10:00:00Z","leftOut":[],"settings":{"refreshSeconds":7}}`), nil); err != nil || string(f.Parts["settings"]) != `{"refreshSeconds":7}` {
		t.Fatalf("%v %v", f, err)
	}
	for in, want := range map[string]string{`{"format":"nope"}`: "not a 221B backup (format", "not json": "neither gzipped JSON nor JSON", "\x1f\x8b\x01": "damaged"} {
		if _, err := Read([]byte(in), nil); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", in, err)
		}
	}
	r := NewRegistry()
	r.Register(Part{Name: "a", Label: "A", Export: func(context.Context, *Env) (any, error) { return nil, errors.New("reading A failed") }})
	if _, err := r.Export(context.Background(), &Env{}); err == nil || err.Error() != "reading A failed" {
		t.Fatalf("%v", err)
	}
}
