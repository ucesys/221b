package credentials

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/certpin"
	"github.com/ucesys/221b/server/internal/certpin/certpintest"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// open makes a test's service. Its list is kept in the database's rows,
// over the secrets in memory, when TEST_STORE is "database", which runs
// every test of this package over the database (Phase 13, DB-60); the
// "index" key of the secrets' store otherwise.
func open(t testing.TB, o Options) *Service {
	t.Helper()
	if os.Getenv("TEST_STORE") == "database" && o.Repo == nil {
		o.Repo = NewPGRepo(dbtest.Open(t))
	}
	return New(o)
}

// ticking is a clock a second further at every reading: whole seconds,
// since the database keeps microseconds.
func ticking() (now func() time.Time, reset func()) {
	start := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	tick := start
	return func() time.Time { tick = tick.Add(time.Second); return tick }, func() { tick = start }
}

func exec(t *testing.T, d *db.DB, sql string, args ...any) {
	t.Helper()
	if _, err := d.Pool().Exec(ctx, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// Everything the service keeps of a credential beside its secret reads
// back from the database as it does from the Secret's "index" key: the
// same steps on both leave the same list and the same note of the
// upgrade, field for field; a list with every field set comes back whole;
// and another replica's service reads the same.
func TestCredentialsInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	now, reset := ticking()
	cert := certpintest.SelfSigned(t)
	srv := certpintest.NewSwitchable(t, cert, nil)
	fp := certpin.Fingerprint(cert.Leaf.Raw)
	ca := certpintest.New(t, certpintest.Options{CN: "private-ca.test", IsCA: true}).PEM
	hc, dial := pinService(t).o.HTTP, pinService(t).o.Dial
	overSecret := New(Options{HTTP: hc, Dial: dial, Now: now})
	open := func() *Service { return New(Options{Repo: NewPGRepo(d), HTTP: hc, Dial: dial, Now: now}) }

	// steps returns the ids it made, in order: each service gives its own.
	steps := func(s *Service) []string {
		t.Helper()
		var ids []string
		mk := func(c Credential, sec Secret) Credential {
			t.Helper()
			c, err := s.Create(ctx, c, sec, "ann")
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, c.ID)
			return c
		}
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		gh := mk(Credential{Name: "GitHub", Kind: KindToken, Endpoint: "https://github.com/ucesys", Description: "the organisation's", Expires: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
			SendAs: SendAs{Method: SendBasic, Username: "x-access-token"}}, Secret{Token: "tok-1"})
		key := mk(Credential{Name: "Status API", Kind: KindToken, Endpoint: "https://status.example.com", SendAs: SendAs{Method: SendHeader, Header: "X-API-Key", Prefix: "key "}}, Secret{Token: "tok-2"})
		mk(Credential{Name: "Metrics", Kind: KindToken, Endpoint: "https://metrics.example.com", SendAs: SendAs{Method: SendQuery, Param: "api_key"}}, Secret{Token: "tok-3"})
		mk(Credential{Name: "Portal", Kind: KindToken, Endpoint: "https://portal.example.com", SendAs: SendAs{Method: SendCookie, Cookie: "session"}}, Secret{Token: "tok-4"})
		mk(Credential{Name: "Charts", Kind: KindUserPass, Endpoint: "https://charts.example.com", Username: "reader", CAPEM: ca}, Secret{Password: "pw-1"})
		mk(Credential{Name: "Bucket", Kind: KindAccessKey, Endpoint: "https://minio.example.com", Region: "eu-west-1"}, Secret{AccessKeyID: "AKIA1", SecretAccessKey: "sk-1", SessionToken: "st-1"})
		mk(Credential{Name: "Service account", Kind: KindOAuth2, Endpoint: "https://api.example.com", TokenURL: "https://login.example.com/oauth/token", ClientID: "console", Scope: "read write"},
			Secret{ClientSecret: "cs-1"})
		scratch := mk(Credential{Name: "Scratch", Kind: KindToken, Endpoint: "https://scratch.example.com"}, Secret{Token: "tok-5"})
		_, err := s.Update(ctx, gh.ID, Credential{Name: "GitHub (ucesys)", Kind: KindToken, Endpoint: "https://github.com/ucesys", Description: "renamed", Expires: time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC),
			SendAs: SendAs{Method: SendBasic, Username: "x-access-token"}}, &Secret{Token: "tok-1b"}, "bob")
		must(err)
		_, err = s.Delete(ctx, scratch.ID)
		must(err)
		// A last test with every field set, its certificate among them.
		var last Test
		dbtest.Fill(&last)
		must(s.NoteTest(ctx, key.ID, last))
		// A certificate trusted, and a test that sees it.
		pinned := mk(Credential{Name: "Self-signed", Kind: KindToken, Endpoint: srv.URL}, Secret{Token: "tok-6"})
		_, _, err = s.Trust(ctx, pinned.ID, "", fp, "ann")
		must(err)
		// The upgrade's note: one made, one left as it was, then dismissed.
		made, err := s.Migrate(ctx, []Consumer{&fakeConsumer{switched: map[string]string{}, found: []Found{
			{Key: "a", Label: "app web/shop", Endpoint: "https://gitlab.example.com/shop", Kind: KindToken, SendAs: SendAs{Method: SendBasic, Username: "oauth2"}, Secret: Secret{Token: "glpat-1"}},
			{Key: "b", Label: "Helm repository broken", Endpoint: "", Kind: KindToken, Secret: Secret{Token: "x"}},
		}}}, "the upgrade")
		must(err)
		for _, c := range made {
			ids = append(ids, c.ID)
		}
		must(s.DismissMigration(ctx))
		return ids
	}
	// read is what a service keeps, the ids it gave written as their place.
	read := func(s *Service, ids []string) string {
		t.Helper()
		list, err := s.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		m, err := s.Migration(ctx)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.MarshalIndent(map[string]any{"credentials": list, "migration": m}, "", " ")
		out := string(b)
		for i, id := range ids {
			out = strings.ReplaceAll(out, id, fmt.Sprint("cr-", i))
		}
		return out
	}

	want := read(overSecret, steps(overSecret))
	reset()
	ids := steps(open())
	got := read(open(), ids)
	if got != want {
		t.Fatalf("the database's credentials differ from the Secret's.\nthe database:\n%s\nthe Secret:\n%s", got, want)
	}
	// The scenario reaches the parts of a credential the kinds made here have.
	for _, part := range []string{`"description"`, `"expires"`, `"caPEM"`, `"pinnedCert"`, `"sendAs"`, `"header"`, `"prefix"`, `"param"`, `"cookie"`, `"username"`, `"region"`, `"tokenURL"`, `"clientId"`,
		`"scope"`, `"set"`, `"lastTest"`, `"certificate"`, `"names"`, `"pinnedWas"`, `"suggest"`, `"from"`, `"notes"`, `"dismissed"`, `"changedBy": "bob"`} {
		if !strings.Contains(want, part) {
			t.Errorf("the scenario has no %s", part)
		}
	}
	// A row a kept credential, and nothing left of the one removed.
	var rows, kept int
	if err := d.Pool().QueryRow(ctx, "SELECT count(*), count(*) FILTER (WHERE is_kept) FROM credentials").Scan(&rows, &kept); err != nil || rows != 9 || kept != 9 {
		t.Errorf("%d rows, %d kept, want 9 and 9: %v", rows, kept, err)
	}
	// The row alone sends nothing: a service that has the list and not the
	// secret (a write cut short, a Secret lost) says the secret is to be
	// given again, where the credential is used.
	if u, err := open().Resolve(ctx, ids[0], UseGit, "https://github.com/ucesys/221b"); err == nil || !strings.Contains(err.Error(), "give its secret again") {
		t.Errorf("a credential whose secret is not there resolved as %+v: %v", u, err)
	}

	// Every field of a credential and of the note is a column: a list with
	// each one set, the kinds' own parts among them, comes back whole.
	var full index
	dbtest.Fill(&full)
	repo := NewPGRepo(dbtest.Open(t))
	for _, c := range full.Credentials {
		if err := repo.Add(ctx, c, MaxCredentials); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.UpdateMigration(ctx, func(m *Migration) error { *m = full.Migration; return nil }); err != nil {
		t.Fatal(err)
	}
	list, err := repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m, err := repo.Migration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(full)
	back, _ := json.Marshal(index{Credentials: list, Migration: m})
	if string(back) != string(b) {
		t.Fatalf("a list with every field set came back changed\nkept:  %s\nsaved: %s", back, b)
	}
}

// DB-75: the repository changes one credential at a time. A change is made
// over the credential as it is kept when the change is made: writers of
// one credential take turns and none loses another's change; one of a
// name another has is refused with why, and changes nothing; the count is
// held to its bound; an id kept already is refused.
func TestTheRepositoryChangesOneCredentialAtATime(t *testing.T) {
	for name, repo := range map[string]Repo{"memory": NewMemoryRepo(), "database": NewPGRepo(dbtest.Open(t))} {
		t.Run(name, func(t *testing.T) {
			for i, n := range []string{"one", "two"} {
				if err := repo.Add(ctx, Credential{ID: fmt.Sprint("cr-", i), Name: n, Kind: KindToken, Endpoint: "https://example.com"}, 2); err != nil {
					t.Fatal(err)
				}
			}
			if err := repo.Add(ctx, Credential{ID: "cr-9", Name: "three", Kind: KindToken, Endpoint: "https://example.com"}, 2); !errors.Is(err, ErrInvalid) {
				t.Fatalf("a third of two: %v", err)
			}
			if err := repo.Add(ctx, Credential{ID: "cr-0", Name: "again", Kind: KindToken, Endpoint: "https://example.com"}, 9); !errors.Is(err, ErrInvalid) {
				t.Fatalf("an id kept already: %v", err)
			}
			var wg sync.WaitGroup
			for i := range 20 {
				wg.Go(func() {
					if _, err := repo.Update(ctx, "cr-0", func(c *Credential) error {
						c.Description += fmt.Sprint(i % 10)
						return nil
					}); err != nil {
						t.Error(err)
					}
				})
			}
			wg.Wait()
			if c, err := repo.Get(ctx, "cr-0"); err != nil || len(c.Description) != 20 {
				t.Fatalf("20 changes at once kept %d: %v", len(c.Description), err)
			}
			if _, err := repo.Update(ctx, "cr-1", func(c *Credential) error { c.Name = "ONE"; return nil }); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "named ONE already") {
				t.Fatalf("a name another has: %v", err)
			}
			if c, _ := repo.Get(ctx, "cr-1"); c.Name != "two" {
				t.Fatalf("a refused change changed %q", c.Name)
			}
			if _, err := repo.Update(ctx, "cr-x", func(*Credential) error { return nil }); !errors.Is(err, ErrNotFound) {
				t.Fatalf("a change of none: %v", err)
			}
			if gone, err := repo.Remove(ctx, "cr-1"); err != nil || !gone {
				t.Fatalf("removing: %v %v", gone, err)
			}
			if gone, err := repo.Remove(ctx, "cr-1"); err != nil || gone {
				t.Fatalf("removing again: %v %v", gone, err)
			}
			if list, err := repo.List(ctx); err != nil || len(list) != 1 || list[0].ID != "cr-0" {
				t.Fatalf("the list: %+v %v", list, err)
			}
		})
	}
}

// A credential keeps its row for as long as something points at it: a
// change updates the row in place, so a repository's and a source's key
// stay; one removed while pointed at leaves a row with only its id, which
// a credential restored under that id fills again; one nothing points at
// is deleted.
func TestACredentialKeepsItsRowWhileSomethingPointsAtIt(t *testing.T) {
	d := dbtest.Open(t)
	now, _ := ticking()
	s := New(Options{Repo: NewPGRepo(d), Now: now})
	c := token(t, s, "GitHub", "https://github.com/ucesys", SendAs{}, "tok-1")
	repo := db.NewID()
	exec(t, d, "INSERT INTO repositories (repository_id, credential_id, name) VALUES ($1, $2, 'charts')", repo, c.ID)
	exec(t, d, "INSERT INTO knowledge_sources (knowledge_source_id, credential_id, name) VALUES ('src-1', $1, 'wiki')", c.ID)
	pointed := func(when string) {
		t.Helper()
		var r, k string
		if err := d.Pool().QueryRow(ctx, `SELECT coalesce((SELECT credential_id FROM repositories WHERE repository_id = $1), ''),
			coalesce((SELECT credential_id FROM knowledge_sources WHERE knowledge_source_id = 'src-1'), '')`, repo).Scan(&r, &k); err != nil || r != c.ID || k != c.ID {
			t.Fatalf("%s: the repository names %q and the source %q, want %s: %v", when, r, k, c.ID, err)
		}
	}
	if _, err := s.Update(ctx, c.ID, Credential{Name: "GitHub (renamed)", Kind: KindToken, Endpoint: "https://github.com/ucesys"}, &Secret{Token: "tok-2"}, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := s.NoteTest(ctx, c.ID, Test{At: now(), OK: true, Text: "200 OK"}); err != nil {
		t.Fatal(err)
	}
	pointed("after a change and a test")

	// Removed while still named (nothing registered its uses here, as when
	// what names it was restored by itself): the row stays, with only its id.
	if _, err := s.Delete(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	pointed("after the credential was removed")
	var kept bool
	var name, kind, by string
	var parts []string
	var created *time.Time
	if err := d.Pool().QueryRow(ctx, "SELECT is_kept, name, kind, updated_by, secret_parts, created_at FROM credentials WHERE credential_id = $1", c.ID).Scan(&kept, &name, &kind, &by, &parts, &created); err != nil ||
		kept || name != "" || kind != "" || by != "" || len(parts) != 0 || created != nil {
		t.Fatalf("the row left: kept %v, name %q, kind %q, by %q, parts %v, created %v: %v", kept, name, kind, by, parts, created, err)
	}
	if list, _ := s.List(ctx); len(list) != 0 {
		t.Fatalf("a row with only its id is listed: %+v", list)
	}
	if _, err := s.Get(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a row with only its id is a credential: %v", err)
	}
	// Its name is free again.
	other := token(t, s, "GitHub (renamed)", "https://github.com/other", SendAs{}, "tok-3")

	// A backup's credential of that id fills the row.
	c.Name = "GitHub"
	n, err := s.Import(ctx, []BackupEntry{{Credential: c, Secret: `{"token":"tok-4"}`}}, func(string) error { return nil },
		func(sealed, _, _ string) (string, error) { return sealed, nil }, func(string, ...any) {})
	if err != nil || n != 1 {
		t.Fatalf("restored %d: %v", n, err)
	}
	pointed("after the credential was restored")
	if got, err := s.Get(ctx, c.ID); err != nil || got.Name != "GitHub" || !slices.Equal(got.Set, []string{"token"}) {
		t.Fatalf("restored: %+v %v", got, err)
	}

	// One nothing points at leaves no row.
	if _, err := s.Delete(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := d.Pool().QueryRow(ctx, "SELECT count(*) FROM credentials WHERE credential_id = $1", other.ID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d rows left of a credential nothing points at: %v", left, err)
	}

	// Every table with a key to credentials is one removeRow asks about: a
	// table given such a key later must be added there, or a removed
	// credential's row would be deleted from under it.
	rows, err := d.Pool().Query(ctx, "SELECT DISTINCT conrelid::regclass::text FROM pg_constraint WHERE contype = 'f' AND confrelid = 'credentials'::regclass ORDER BY 1")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	var table string
	for rows.Next() {
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if rows.Err() != nil || !slices.Equal(tables, pointAtCredentials) {
		t.Fatalf("the tables with a key to credentials are %v, and removeRow asks about %v: %v", tables, pointAtCredentials, rows.Err())
	}
}

// Replicas that change the list at once each have their change kept: a
// save that lost to another is tried again over what is kept now, as it
// was over the Secret's version.
func TestWritersOfTheListTakeTurns(t *testing.T) {
	d := dbtest.Open(t)
	mem := &MemorySecret{}
	var wg sync.WaitGroup
	for i := range 8 {
		s := New(Options{Secrets: mem, Repo: NewPGRepo(d)})
		wg.Go(func() {
			if _, err := s.Create(ctx, Credential{Name: fmt.Sprint("replica ", i), Kind: KindToken, Endpoint: "https://example.com"}, Secret{Token: fmt.Sprint("tok-", i)}, "ann"); err != nil {
				t.Errorf("replica %d: %v", i, err)
			}
		})
	}
	wg.Wait()
	list, err := New(Options{Secrets: mem, Repo: NewPGRepo(d)}).List(ctx)
	if err != nil || len(list) != 8 {
		t.Fatalf("%d of 8 credentials kept: %v", len(list), err)
	}
	// Two of one name, made at once: one is kept, the other refused with why.
	errs := make([]error, 2)
	for i := range errs {
		s := New(Options{Secrets: mem, Repo: NewPGRepo(d)})
		wg.Go(func() {
			_, errs[i] = s.Create(ctx, Credential{Name: "Twin", Kind: KindToken, Endpoint: "https://example.com"}, Secret{Token: "tok"}, "ann")
		})
	}
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) || !errors.Is(errors.Join(errs...), ErrInvalid) {
		t.Fatalf("two of one name at once: %v, %v", errs[0], errs[1])
	}
}

// asBefore writes the list a service keeps into the Secret's "index" key,
// as a console before 0.13 kept it there: what the copy reads (DB-61).
func asBefore(t testing.TB, s *Service, into *MemorySecret) {
	t.Helper()
	list, err := s.o.Repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.o.Repo.Migration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(index{Credentials: list, Migration: m})
	_, version, _, _ := into.LoadKey(ctx, indexKey)
	if err := into.SaveKeyIf(ctx, indexKey, b, version); err != nil {
		t.Fatal(err)
	}
}

// failingSecret is a Secret that cannot be read.
type failingSecret struct{ *MemorySecret }

func (failingSecret) LoadKey(context.Context, string) ([]byte, string, bool, error) {
	return nil, "", false, errors.New("the API server is away")
}

// The list is copied out of the Secret once (DB-61): a row with only its
// id is filled, the Secret then keeps its secrets and no list, a copy
// that fails leaves the list where it was, and a list a console from
// before the move writes later is removed and not read.
func TestTheListIsCopiedOutOfTheSecretOnce(t *testing.T) {
	d := dbtest.Open(t)
	now, _ := ticking()
	mem := &MemorySecret{}
	before := New(Options{Secrets: mem, Now: now})
	a := token(t, before, "GitHub", "https://github.com/ucesys", SendAs{Method: SendBasic, Username: "x-access-token"}, "tok-a")
	b, err := before.Create(ctx, Credential{Name: "Charts", Kind: KindUserPass, Endpoint: "https://charts.example.com", Username: "reader"}, Secret{Password: "pw-b"}, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if err := before.NoteTest(ctx, a.ID, Test{At: now(), OK: true, Method: "Basic", Status: 200, Text: "200 OK", By: "ann"}); err != nil {
		t.Fatal(err)
	}
	if _, err := before.Migrate(ctx, []Consumer{&fakeConsumer{switched: map[string]string{}, found: []Found{{Key: "x", Label: "Helm repository broken", Kind: KindToken, Secret: Secret{Token: "x"}}}}}, "the upgrade"); err != nil {
		t.Fatal(err)
	}
	read := func(s *Service) string {
		t.Helper()
		list, err := s.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		m, err := s.Migration(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := json.Marshal(map[string]any{"credentials": list, "migration": m})
		return string(out)
	}
	want := read(before)
	asBefore(t, before, mem)
	// A repository of the database already names the first: a row with
	// only its id, as apps/pg.go makes it.
	repo := db.NewID()
	exec(t, d, "INSERT INTO credentials (credential_id) VALUES ($1)", a.ID)
	exec(t, d, "INSERT INTO repositories (repository_id, credential_id, name) VALUES ($1, $2, 'charts')", repo, a.ID)
	keys := func() map[string]string {
		m := map[string]string{}
		for k, v := range mem.data {
			m[k] = string(v)
		}
		return m
	}
	asItWas := keys()
	copied := func() bool {
		t.Helper()
		ok, err := d.Imported(ctx, ListImportID)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	// A Secret that cannot be read, and a list that cannot be: nothing is
	// copied, nothing is recorded, and the Secret is as it was.
	if n, err := CopyList(ctx, d, failingSecret{mem}); err == nil || n != 0 || copied() {
		t.Fatalf("a Secret that cannot be read: copied %d, recorded %v: %v", n, copied(), err)
	}
	broken := &MemorySecret{}
	if err := broken.SaveKeyIf(ctx, indexKey, []byte(`{"credentials":[{"id":"cr-1","name":"half a li`), ""); err != nil {
		t.Fatal(err)
	}
	if n, err := CopyList(ctx, d, broken); err == nil || n != 0 || copied() || strings.Contains(err.Error(), "half a li") {
		t.Fatalf("a list that cannot be read: copied %d, recorded %v: %v", n, copied(), err)
	}
	if _, _, found, _ := broken.LoadKey(ctx, indexKey); !found {
		t.Fatal("a list that could not be copied left its Secret")
	}
	if got := keys(); fmt.Sprint(got) != fmt.Sprint(asItWas) {
		t.Fatal("a copy that failed changed the Secret")
	}

	// The copy.
	if n, err := CopyList(ctx, d, mem); err != nil || n != 2 || !copied() {
		t.Fatalf("copied %d, recorded %v: %v", n, copied(), err)
	}
	after := New(Options{Secrets: mem, Repo: NewPGRepo(d), Now: now})
	if got := read(after); got != want {
		t.Fatalf("the list copied differs.\nthe database:\n%s\nthe Secret:\n%s", got, want)
	}
	var kept bool
	var of string
	if err := d.Pool().QueryRow(ctx, `SELECT c.is_kept, coalesce(r.credential_id, '') FROM repositories r LEFT JOIN credentials c USING (credential_id) WHERE r.repository_id = $1`, repo).Scan(&kept, &of); err != nil || !kept || of != a.ID {
		t.Fatalf("the row with only its id: kept %v, named by the repository as %q: %v", kept, of, err)
	}
	// The list leaves the Secret, alone: every secret stays, byte for byte.
	if removed, err := RemoveList(ctx, mem); err != nil || !removed {
		t.Fatalf("removed %v: %v", removed, err)
	}
	delete(asItWas, indexKey)
	if got := keys(); fmt.Sprint(got) != fmt.Sprint(asItWas) || len(got) != 2 || got[secretKey(a.ID)] == "" || got[secretKey(b.ID)] == "" {
		t.Fatalf("the Secret after the list left it holds %d keys", len(got))
	}
	if removed, err := RemoveList(ctx, mem); err != nil || removed {
		t.Fatalf("removed again: %v %v", removed, err)
	}
	if u, err := after.Resolve(ctx, a.ID, UseGit, "https://github.com/ucesys/221b"); err != nil || u.Token != "tok-a" || u.Username != "x-access-token" {
		t.Fatalf("the copied credential resolves as %+v: %v", u, err)
	}
	if noCredential, noSecret, err := Unmatched(ctx, NewPGRepo(d), mem); err != nil || len(noCredential) != 0 || len(noSecret) != 0 {
		t.Fatalf("the halves after the copy: %v %v %v", noCredential, noSecret, err)
	}

	// A console from before the move, still running or brought back, finds
	// no list and writes one of its own. It is not copied: the database's
	// list is the one that counts. It is removed, and the secret it wrote
	// stays, named by nothing.
	older := New(Options{Secrets: mem, Now: now})
	late := token(t, older, "Written by an older console", "https://old.example.com", SendAs{}, "tok-late")
	asBefore(t, older, mem)
	if n, err := CopyList(ctx, d, mem); err != nil || n != 0 {
		t.Fatalf("a second copy: %d %v", n, err)
	}
	if got := read(after); got != want {
		t.Fatalf("a list written later was read:\n%s", got)
	}
	if removed, err := RemoveList(ctx, mem); err != nil || !removed {
		t.Fatalf("the later list: removed %v: %v", removed, err)
	}
	if got := keys(); len(got) != 3 || got[secretKey(late.ID)] == "" || got[indexKey] != "" {
		t.Fatalf("the Secret then holds %d keys", len(got))
	}
	if noCredential, noSecret, err := Unmatched(ctx, NewPGRepo(d), mem); err != nil || !slices.Equal(noCredential, []string{late.ID}) || len(noSecret) != 0 {
		t.Fatalf("the halves then: %v %v %v", noCredential, noSecret, err)
	}
	// And a credential whose secret is gone is said so, and removed by nothing.
	if err := mem.DeleteKeyIf(ctx, secretKey(b.ID), fmt.Sprint(mem.version)); err != nil {
		t.Fatal(err)
	}
	if _, noSecret, err := Unmatched(ctx, NewPGRepo(d), mem); err != nil || !slices.Equal(noSecret, []string{b.ID}) {
		t.Fatalf("a credential without its secret: %v %v", noSecret, err)
	}

	// A database that keeps credentials already (the copy failed at an
	// earlier start, and people added some since): the database's stay as
	// they are, one of the Secret's under an id the database has is left,
	// and one whose name is taken comes over under a name of its own.
	d2 := dbtest.Open(t)
	mem2 := &MemorySecret{}
	old := New(Options{Secrets: mem2, Now: now})
	same := token(t, old, "Registry", "https://registry.example.com", SendAs{}, "tok-r")
	clash := token(t, old, "github", "https://github.com/elsewhere", SendAs{}, "tok-g")
	asBefore(t, old, mem2)
	here := New(Options{Secrets: mem2, Repo: NewPGRepo(d2), Now: now})
	newer := same
	newer.Name, newer.Description = "Registry (edited)", "the database's"
	if err := here.add(ctx, newer, Secret{Token: "tok-r"}); err != nil {
		t.Fatal(err)
	}
	token(t, here, "GitHub", "https://github.com/ucesys", SendAs{}, "tok-h")
	if n, err := CopyList(ctx, d2, mem2); err != nil || n != 1 {
		t.Fatalf("copied %d into a database that keeps some: %v", n, err)
	}
	list, err := here.List(ctx)
	if err != nil || len(list) != 3 {
		t.Fatalf("%d credentials: %v", len(list), err)
	}
	if got, _ := here.Get(ctx, same.ID); got.Name != "Registry (edited)" || got.Description != "the database's" {
		t.Fatalf("the database's credential was written over: %+v", got)
	}
	if got, _ := here.Get(ctx, clash.ID); got.Name != "github (copied at upgrade)" || got.Endpoint != "https://github.com/elsewhere" {
		t.Fatalf("the one whose name was taken: %+v", got)
	}
}

// No secret is ever in the database: every secret part of a credential
// holds a marker, through every way a secret reaches the service, and the
// marker is in no column of any row of any table, nor in the log. The
// secrets' store holds it, so the test did put it somewhere.
func TestNoSecretIsEverInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	const marker = "MARKERq7Zk2x9secret"
	// A far end that sends back whatever it was sent, the way some do in
	// an error's text.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+r.Header.Get("Authorization")+`"`)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, "refused: %s %s %v", r.Header.Get("Authorization"), r.URL.RawQuery, r.Header)
	}))
	t.Cleanup(srv.Close)
	var logs bytes.Buffer
	mem := &MemorySecret{}
	now, _ := ticking()
	s := New(Options{Secrets: mem, Repo: NewPGRepo(d), HTTP: srv.Client(), Dial: pinService(t).o.Dial, Now: now, Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	every := Secret{Token: marker + "-token", Password: marker + "-password", AccessKeyID: marker + "-akid", SecretAccessKey: marker + "-sak", SessionToken: marker + "-session",
		PrivateKey: marker + "-key", Passphrase: marker + "-passphrase", ClientSecret: marker + "-client", KeyPEM: marker + "-pem"}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	// Every part at once, on a credential with every other field set, as
	// the service adds one whatever its kind.
	for i, kind := range Kinds {
		var c Credential
		dbtest.Fill(&c)
		c.ID, c.Kind, c.Name = newID(), kind, fmt.Sprint("every part ", i)
		must(s.add(ctx, c, every))
	}
	// The page: made, changed with a new secret, tested against a far end
	// that repeats it.
	tok, err := s.Create(ctx, Credential{Name: "Token", Kind: KindToken, Endpoint: srv.URL}, Secret{Token: marker + "-made"}, "ann")
	must(err)
	_, err = s.Create(ctx, Credential{Name: "User", Kind: KindUserPass, Endpoint: srv.URL + "/basic", Username: "reader"}, Secret{Password: marker + "-made"}, "ann")
	must(err)
	_, err = s.Create(ctx, Credential{Name: "Bucket", Kind: KindAccessKey, Endpoint: "https://minio.example.com", Region: "eu-west-1"}, every, "ann")
	must(err)
	_, err = s.Create(ctx, Credential{Name: "Client", Kind: KindOAuth2, Endpoint: "https://api.example.com", TokenURL: "https://login.example.com/token", ClientID: "console"}, every, "ann")
	must(err)
	query, err := s.Create(ctx, Credential{Name: "Query", Kind: KindToken, Endpoint: srv.URL + "/q", SendAs: SendAs{Method: SendQuery, Param: "key"}}, Secret{Token: marker + "-query"}, "ann")
	must(err)
	_, err = s.Update(ctx, tok.ID, Credential{Name: "Token", Kind: KindToken, Endpoint: srv.URL, Description: "changed"}, &Secret{Token: marker + "-changed"}, "bob")
	must(err)
	for _, id := range []string{tok.ID, query.ID} {
		res, err := s.RunTest(ctx, id, "", "ann")
		must(err)
		if res.OK || res.Status != http.StatusUnauthorized || strings.Contains(res.Text+res.Suggest, marker) {
			t.Fatalf("the test of %s: %+v", id, res)
		}
	}
	// The upgrade: secrets found with what used them, one that becomes a
	// credential and one that cannot, noted with why.
	_, err = s.Migrate(ctx, []Consumer{&fakeConsumer{switched: map[string]string{}, found: []Found{
		{Key: "a", Label: "app web/shop", Endpoint: "https://gitlab.example.com/shop", Kind: KindToken, Secret: Secret{Token: marker + "-found"}},
		{Key: "b", Label: "Helm repository broken", Endpoint: "", Kind: KindToken, Secret: Secret{Token: marker + "-broken"}},
		{Key: "c", Label: "Helm repository keyed", Endpoint: "ssh://git@git.example.com/x", Kind: KindSSH, Secret: Secret{PrivateKey: marker + "-notakey", Passphrase: marker + "-pp"}},
	}}}, "the upgrade")
	must(err)
	// A backup and its restore: sealed going out, opened coming back.
	out, err := s.Export(ctx, func(clear string) string { return "sealed:" + clear })
	must(err)
	_, err = s.Delete(ctx, tok.ID)
	must(err)
	n, err := s.Import(ctx, out, func(string) error { return nil }, func(sealed, _, _ string) (string, error) { return strings.TrimPrefix(sealed, "sealed:"), nil }, func(string, ...any) {})
	if err != nil || n != 1 {
		t.Fatalf("restored %d: %v", n, err)
	}
	if u, err := s.Resolve(ctx, tok.ID, UseHTTP, srv.URL); err != nil || u.Token != marker+"-changed" {
		t.Fatalf("the restored secret: %v", err)
	}

	list, err := s.List(ctx)
	must(err)
	if len(list) != len(Kinds)+6 {
		t.Fatalf("%d credentials kept", len(list))
	}
	if held := dbtest.Holding(t, d, marker); len(held) != 0 {
		t.Fatalf("a secret is in the database, in the tables %v", held)
	}
	if strings.Contains(logs.String(), marker) {
		t.Fatal("a secret is in the log")
	}
	// What looks for the marker finds one that is there.
	if held := dbtest.Holding(t, d, "every part 0"); held["credentials"] != 1 {
		t.Fatalf("the look through the tables finds %v of a name that is kept", held)
	}
	parts := 0
	for key, v := range mem.data {
		if strings.HasPrefix(key, "secret-") && strings.Contains(string(v), marker) {
			parts++
		}
	}
	if parts != len(list) || mem.data[indexKey] != nil {
		t.Fatalf("%d of %d secrets are in the Secret's store, and a list: %v", parts, len(list), mem.data[indexKey] != nil)
	}
}
