package apps_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/apps/appstest"
	"github.com/ucesys/221b/server/internal/credentials"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// The applications over the three ways they are kept (Phase 13, DB-62):
// in memory, in their Secrets, and as rows of the database with the secret
// part in their Secrets.

// eachStore runs a test over the three stores; the database's is skipped
// without TEST_DATABASE.
func eachStore(t *testing.T, f func(t *testing.T, s apps.Store)) {
	t.Run("memory", func(t *testing.T) { f(t, apps.NewMemoryStore()) })
	t.Run("secrets", func(t *testing.T) { f(t, appstest.Secrets(appstest.Cluster())) })
	t.Run("database", func(t *testing.T) {
		s, _, _ := appstest.PG(t, nil)
		f(t, s)
	})
}

var at = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

// shop is an application with values, parameters and commands, as a
// person sets one up.
func shop(name string) apps.Record {
	return apps.Record{Version: apps.RecordVersion, Cluster: "local", Namespace: "web", Name: name,
		Source: apps.Source{Repo: "https://github.com/ucesys/shop", Path: "chart", Ref: "main", RefKind: apps.RefBranch},
		Commit: "767275b73ce4", CommitDate: at.Add(-time.Hour), CommitSubject: "Bump the chart", ChartVersion: "1.4.0",
		Values: "db:\n  password: hunter2\n", Params: []string{"image.tag=1.2.3", "db.user=shop"},
		Commands: []apps.Command{
			{Name: "Scale up", Note: "more pods", Params: []string{"replicas=5"}, Created: at, CreatedBy: "ann", Updated: at, UpdatedBy: "ann"},
			{Name: "Scale down", Params: []string{"replicas=1", "debug=false"}, Created: at, CreatedBy: "bob", Updated: at.Add(time.Minute), UpdatedBy: "bob"},
		},
		CredMigrated: true, CreatorNamespaces: []string{"web"}, CreatedBy: "ann", Created: at, UpdatedBy: "ann", Updated: at}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// read is a record as Get gives it, without the version it was read at.
func read(t *testing.T, s apps.Store, r apps.Record) (apps.Record, string) {
	t.Helper()
	got, token, err := s.Get(context.Background(), r.Cluster, r.Namespace, r.Name)
	if err != nil {
		t.Fatalf("reading %s: %v", r.Name, err)
	}
	if got.Rev == "" {
		t.Fatalf("%s was read without a version", r.Name)
	}
	got.Rev = ""
	return got, token
}

func list(t *testing.T, s apps.Store) []apps.Record {
	t.Helper()
	got, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// Every field of a record comes back from each store, alone and in the
// list; the list is by cluster, namespace and name; a token is kept by a
// save that gives none, replaced by one that gives one and removed by an
// empty one; and a removed record is gone.
func TestEveryStoreKeepsARecordWhole(t *testing.T) {
	eachStore(t, func(t *testing.T, s apps.Store) {
		ctx := context.Background()
		var full apps.Record
		dbtest.Fill(&full)
		full.Version, full.Rev, full.HasToken = apps.RecordVersion, "", false
		plain := apps.Record{Version: apps.RecordVersion, Cluster: "a", Namespace: "web", Name: "plain", Source: apps.Source{Repo: "https://github.com/ucesys/plain", Ref: "main", RefKind: apps.RefBranch}, Created: at, Updated: at}
		token := "ghp-kept-with-the-record"
		must(t, s.Save(ctx, full, &token))
		must(t, s.Save(ctx, plain, nil))

		want := full
		want.HasToken = true
		if got, tok := read(t, s, full); !reflect.DeepEqual(got, want) || tok != token {
			t.Fatalf("got  %+v\nwant %+v\ntoken %q", got, want, tok)
		}
		if got, tok := read(t, s, plain); !reflect.DeepEqual(got, plain) || tok != "" {
			t.Fatalf("got  %+v\nwant %+v\ntoken %q", got, plain, tok)
		}
		if got := list(t, s); !reflect.DeepEqual(got, []apps.Record{plain, want}) {
			t.Fatalf("the list:\ngot  %+v\nwant %+v", got, []apps.Record{plain, want})
		}

		// A save that gives no token keeps it; one that gives one replaces
		// it; an empty one removes it.
		next, _, err := s.Get(ctx, full.Cluster, full.Namespace, full.Name)
		must(t, err)
		next.Values, next.Params, next.Commands = "replicas: 3\n", []string{"a=b"}, next.Commands[:1]
		must(t, s.Save(ctx, next, nil))
		next.Rev = ""
		if got, tok := read(t, s, full); !reflect.DeepEqual(got, next) || tok != token {
			t.Fatalf("after a change:\ngot  %+v\nwant %+v\ntoken %q", got, next, tok)
		}
		other := "ghp-another"
		must(t, s.Save(ctx, next, &other))
		if _, tok := read(t, s, full); tok != other {
			t.Fatalf("a token given: %q", tok)
		}
		none := ""
		must(t, s.Save(ctx, next, &none))
		if got, tok := read(t, s, full); tok != "" || got.HasToken {
			t.Fatalf("a token removed: %q %v", tok, got.HasToken)
		}

		must(t, s.Delete(ctx, full.Cluster, full.Namespace, full.Name))
		if _, _, err := s.Get(ctx, full.Cluster, full.Namespace, full.Name); !errors.Is(err, apps.ErrNotFound) {
			t.Fatalf("a removed record: %v", err)
		}
		if got := list(t, s); !reflect.DeepEqual(got, []apps.Record{plain}) {
			t.Fatalf("the list after a removal: %+v", got)
		}
		must(t, s.Delete(ctx, full.Cluster, full.Namespace, full.Name))
	})
}

func exec(t *testing.T, d *db.DB, sql string, args ...any) {
	t.Helper()
	if _, err := d.Pool().Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func secretOf(t *testing.T, cs *fake.Clientset, r apps.Record) *corev1.Secret {
	t.Helper()
	sec, err := cs.CoreV1().Secrets(appstest.Namespace).Get(context.Background(), appstest.SecretName(r.Cluster, r.Namespace, r.Name), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the Secret of %s: %v", r.Name, err)
	}
	return sec
}

// secrets are the application Secrets as they are: what each holds, its
// labels and its version.
func secrets(t *testing.T, cs *fake.Clientset) map[string]corev1.Secret {
	t.Helper()
	l, err := cs.CoreV1().Secrets(appstest.Namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]corev1.Secret{}
	for _, sec := range l.Items {
		out[sec.Name] = *sec.DeepCopy()
	}
	return out
}

func keysOf(sec *corev1.Secret) []string {
	return slices.Sorted(maps.Keys(sec.Data))
}

// With a database, an application's Secret holds its values, parameters
// and commands' parameters under set.json, and its token: no record. The
// row and the commands' rows hold the rest, and none of the secret part.
func TestAnApplicationOfTheDatabaseKeepsItsSecretPartInItsSecret(t *testing.T) {
	s, d, cs := appstest.PG(t, nil)
	ctx := context.Background()
	rec := shop("shop")
	rec.Credential = "cr-0123456789ab"
	token := "ghp-kept-with-the-record"
	must(t, s.Save(ctx, rec, &token))
	sec := secretOf(t, cs, rec)
	if got := keysOf(sec); !slices.Equal(got, []string{"set.json", "token"}) {
		t.Fatalf("the Secret holds %v", got)
	}
	wantLabels := map[string]string{apps.LabelComponent: apps.ComponentValue, apps.LabelCluster: "local", apps.LabelAppNamespace: "web", apps.LabelAppName: "shop"}
	if !maps.Equal(sec.Labels, wantLabels) {
		t.Fatalf("its labels: %v", sec.Labels)
	}
	var set struct {
		Values   string   `json:"values"`
		Params   []string `json:"params"`
		Commands []struct {
			Name   string   `json:"name"`
			Params []string `json:"params"`
		} `json:"commands"`
	}
	must(t, json.Unmarshal(sec.Data["set.json"], &set))
	if set.Values != rec.Values || !slices.Equal(set.Params, rec.Params) || len(set.Commands) != 2 || set.Commands[1].Name != "Scale down" || !slices.Equal(set.Commands[1].Params, rec.Commands[1].Params) {
		t.Fatalf("set.json: %s", sec.Data["set.json"])
	}
	var rows, commands, clusters, creds int
	must(t, d.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM applications WHERE cluster_id = 'local' AND namespace = 'web' AND name = 'shop' AND version = 1 AND credential_id = 'cr-0123456789ab'),
		(SELECT count(*) FROM application_commands), (SELECT count(*) FROM clusters WHERE cluster_id = 'local'), (SELECT count(*) FROM credentials WHERE credential_id = 'cr-0123456789ab' AND NOT is_kept)`).Scan(&rows, &commands, &clusters, &creds))
	if rows != 1 || commands != 2 || clusters != 1 || creds != 1 {
		t.Fatalf("%d rows, %d commands, %d clusters, %d credentials", rows, commands, clusters, creds)
	}
	for _, text := range []string{"hunter2", "image.tag", "replicas=5", token} {
		if held := dbtest.Holding(t, d, text); len(held) != 0 {
			t.Fatalf("%q is in the database: %v", text, held)
		}
	}
	// A save that changes nothing of the secret part leaves the Secret as
	// it is; the row's version moves.
	got, _, err := s.Get(ctx, "local", "web", "shop")
	must(t, err)
	got.Commit = "0123456789ab"
	must(t, s.Save(ctx, got, nil))
	if again := secretOf(t, cs, rec); again.ResourceVersion != sec.ResourceVersion {
		t.Fatalf("the Secret was written again: %s, was %s", again.ResourceVersion, sec.ResourceVersion)
	}
	if after, _, _ := s.Get(ctx, "local", "web", "shop"); after.Rev != "2" || after.Commit != "0123456789ab" || after.Values != rec.Values {
		t.Fatalf("after: %+v", after)
	}
	// A removed application's Secret goes with it, as it always did.
	must(t, s.Delete(ctx, "local", "web", "shop"))
	if left := secrets(t, cs); len(left) != 0 {
		t.Fatalf("Secrets left after a removal: %v", slices.Collect(maps.Keys(left)))
	}
	must(t, d.Pool().QueryRow(ctx, "SELECT (SELECT count(*) FROM applications), (SELECT count(*) FROM application_commands)").Scan(&rows, &commands))
	if rows != 0 || commands != 0 {
		t.Fatalf("%d rows and %d commands left", rows, commands)
	}
}

// failing has every write of a Secret fail while on.
func failing(cs *fake.Clientset, on *bool, names ...string) {
	for _, verb := range []string{"create", "update"} {
		cs.PrependReactor(verb, "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
			var sec *corev1.Secret
			switch w := a.(type) {
			case k8stesting.UpdateAction:
				sec = w.GetObject().(*corev1.Secret)
			case k8stesting.CreateAction:
				sec = w.GetObject().(*corev1.Secret)
			}
			if !*on || (len(names) > 0 && !slices.Contains(names, sec.Name)) {
				return false, nil, nil
			}
			return true, nil, errors.New("the API server is away")
		})
	}
}

// The order of the two writes: the row in a transaction, the Secret, then
// the commit. A Secret that cannot be written leaves the row as it was,
// with its version, so the same save goes through once it can; and a new
// application whose Secret cannot be made has no row.
func TestASecretThatCannotBeWrittenLeavesTheRowAsItWas(t *testing.T) {
	s, d, cs := appstest.PG(t, nil)
	ctx := context.Background()
	must(t, s.Save(ctx, shop("shop"), nil))
	before, _, err := s.Get(ctx, "local", "web", "shop")
	must(t, err)
	away := true
	failing(cs, &away)

	change := before
	change.Source.Ref, change.Commit, change.Values, change.Commands = "v2", "ffffffffffff", "db:\n  password: other\n", change.Commands[:1]
	if err := s.Save(ctx, change, nil); err == nil || errors.Is(err, apps.ErrConflict) {
		t.Fatalf("a save whose Secret cannot be written: %v", err)
	}
	if after, _, err := s.Get(ctx, "local", "web", "shop"); err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("the record changed:\ngot  %+v\nwant %+v (%v)", after, before, err)
	}
	if err := s.Save(ctx, shop("blog"), nil); err == nil {
		t.Fatal("a new application was saved without its Secret")
	}
	var rows int
	must(t, d.Pool().QueryRow(ctx, "SELECT count(*) FROM applications").Scan(&rows))
	if _, _, err := s.Get(ctx, "local", "web", "blog"); !errors.Is(err, apps.ErrNotFound) || rows != 1 {
		t.Fatalf("the new application has a row (%d rows): %v", rows, err)
	}

	away = false
	must(t, s.Save(ctx, change, nil))
	change.Rev = ""
	if after, _ := read(t, s, change); !reflect.DeepEqual(after, change) {
		t.Fatalf("the same save, once the Secret can be written:\ngot  %+v\nwant %+v", after, change)
	}
}

// A row without its Secret is an application with no values, parameters
// or commands' parameters, said once in the log, and a save gives them
// again; a Secret without a row is not an application, and stays.
func TestTheTwoHalvesOfAnApplicationApart(t *testing.T) {
	var logs bytes.Buffer
	s, _, cs := appstest.PG(t, slog.New(slog.NewTextHandler(&logs, nil)))
	ctx := context.Background()
	rec := shop("shop")
	token := "ghp-kept-with-the-record"
	must(t, s.Save(ctx, rec, &token))
	must(t, cs.CoreV1().Secrets(appstest.Namespace).Delete(ctx, appstest.SecretName("local", "web", "shop"), metav1.DeleteOptions{}))

	bare := rec
	bare.Values, bare.Params = "", nil
	bare.Commands = slices.Clone(rec.Commands)
	for i := range bare.Commands {
		bare.Commands[i].Params = nil
	}
	for range 2 {
		if got, tok := read(t, s, rec); !reflect.DeepEqual(got, bare) || tok != "" {
			t.Fatalf("a row without its Secret:\ngot  %+v\nwant %+v\ntoken %q", got, bare, tok)
		}
		if got := list(t, s); !reflect.DeepEqual(got, []apps.Record{bare}) {
			t.Fatalf("the list: %+v", got)
		}
	}
	if n := strings.Count(logs.String(), "reads with no values"); n != 1 {
		t.Fatalf("said %d times:\n%s", n, logs.String())
	}
	// A save gives the values again, and a Secret that is lost again is
	// said again.
	got, _, err := s.Get(ctx, "local", "web", "shop")
	must(t, err)
	got.Values, got.Params, got.Commands = rec.Values, rec.Params, rec.Commands
	must(t, s.Save(ctx, got, nil))
	if got, _ := read(t, s, rec); !reflect.DeepEqual(got, rec) {
		t.Fatalf("after a save:\ngot  %+v\nwant %+v", got, rec)
	}
	// A Secret this console cannot read the values of reads the same way.
	sec := secretOf(t, cs, rec)
	sec.Data["set.json"] = []byte("not what the console wrote")
	_, err = cs.CoreV1().Secrets(appstest.Namespace).Update(ctx, sec, metav1.UpdateOptions{})
	must(t, err)
	if got, _ := read(t, s, rec); !reflect.DeepEqual(got, bare) {
		t.Fatalf("a Secret that cannot be read: %+v", got)
	}
	if n := strings.Count(logs.String(), "reads with no values"); n != 2 || strings.Contains(logs.String(), "not what the console wrote") {
		t.Fatalf("said %d times:\n%s", n, logs.String())
	}

	// A Secret with no row, as a console from before the move leaves one.
	must(t, appstest.Secrets(cs).Save(ctx, shop("orphan"), nil))
	if got := list(t, s); len(got) != 1 || got[0].Name != "shop" {
		t.Fatalf("a Secret without a row is listed: %+v", got)
	}
	if _, _, err := s.Get(ctx, "local", "web", "orphan"); !errors.Is(err, apps.ErrNotFound) {
		t.Fatalf("a Secret without a row is read: %v", err)
	}
	noSecret, noRow, err := s.Unmatched(ctx)
	if err != nil || len(noSecret) != 0 || !slices.Equal(noRow, []string{appstest.SecretName("local", "web", "orphan")}) {
		t.Fatalf("unmatched: %v %v %v", noSecret, noRow, err)
	}
	if orphan := secretOf(t, cs, shop("orphan")); !slices.Equal(keysOf(orphan), []string{"spec.json"}) {
		t.Fatalf("the Secret without a row changed: %v", keysOf(orphan))
	}
}

// A command's parameters are paired with it by its name, and one
// command's are never given to another: two commands of one name each
// keep their own, and when the two halves are out of step a command the
// Secret does not name reads with none.
func TestACommandKeepsItsOwnParameters(t *testing.T) {
	s, _, cs := appstest.PG(t, nil)
	ctx := context.Background()
	rec := shop("shop")
	rec.Commands = []apps.Command{
		{Name: "Deploy", Params: []string{"first=1"}, Created: at, Updated: at},
		{Name: "Deploy", Params: []string{"second=2"}, Created: at, Updated: at},
		{Name: "Roll back", Params: []string{}, Created: at, Updated: at},
		{Name: "Drain", Created: at, Updated: at},
	}
	must(t, s.Save(ctx, rec, nil))
	if got, _ := read(t, s, rec); !reflect.DeepEqual(got.Commands, rec.Commands) {
		t.Fatalf("got  %+v\nwant %+v", got.Commands, rec.Commands)
	}
	sec := secretOf(t, cs, rec)
	sec.Data["set.json"] = []byte(`{"values":"a: 1\n","commands":[{"name":"Drain","params":["force=true"]},{"name":"Gone","params":["x=y"]},{"name":"Deploy","params":["only=1"]}]}`)
	_, err := cs.CoreV1().Secrets(appstest.Namespace).Update(ctx, sec, metav1.UpdateOptions{})
	must(t, err)
	got, _ := read(t, s, rec)
	var params [][]string
	for _, c := range got.Commands {
		params = append(params, c.Params)
	}
	if !reflect.DeepEqual(params, [][]string{{"only=1"}, nil, nil, {"force=true"}}) {
		t.Fatalf("the parameters: %v", params)
	}
}

// The move (DB-63). The copy makes a row of each record the Secrets hold
// and changes no Secret; the rewrite then leaves each Secret with its
// values, parameters and commands' parameters under set.json, its token
// and its labels, and no spec.json. What the store reads is the same
// before, between and after. A Secret whose record cannot be read has no
// row and is left as it is, byte for byte. No Secret is deleted, and a
// second run does nothing.
func TestTheApplicationsLeaveTheirSecrets(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	cs := appstest.Cluster()
	old := appstest.Secrets(cs)
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))

	var full apps.Record
	dbtest.Fill(&full)
	full.Rev = ""
	token := "ghp-of-before-0.12.1"
	must(t, old.Save(ctx, shop("shop"), &token))
	must(t, old.Save(ctx, shop("blog"), nil))
	must(t, old.Save(ctx, full, nil))
	labels := map[string]string{apps.LabelComponent: apps.ComponentValue, apps.LabelCluster: "local"}
	for name, spec := range map[string]string{"b221-app-newer": `{"version":99,"cluster":"local","namespace":"web","name":"newer","values":"kept: true\n"}`, "b221-app-broken": "not a record"} {
		_, err := cs.CoreV1().Secrets(appstest.Namespace).Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: appstest.Namespace, Labels: labels},
			Data: map[string][]byte{"spec.json": []byte(spec), "token": []byte("tok")}}, metav1.CreateOptions{})
		must(t, err)
	}
	before, was := list(t, old), secrets(t, cs)
	if len(before) != 3 || len(was) != 5 {
		t.Fatalf("%d records in %d Secrets", len(before), len(was))
	}

	n, err := apps.CopyFromSecrets(ctx, d, old, log)
	if err != nil || n != 3 {
		t.Fatalf("copied %d: %v", n, err)
	}
	if now := secrets(t, cs); !reflect.DeepEqual(now, was) {
		t.Fatal("the copy changed a Secret")
	}
	s := apps.NewPGStore(d, old, log)
	if got := list(t, s); !reflect.DeepEqual(got, before) {
		t.Fatalf("between the copy and the rewrite:\ngot  %+v\nwant %+v", got, before)
	}

	n, err = apps.RewriteSecrets(ctx, d, old, log)
	if err != nil || n != 3 {
		t.Fatalf("rewrote %d: %v", n, err)
	}
	now := secrets(t, cs)
	if len(now) != len(was) {
		t.Fatalf("%d Secrets, were %d", len(now), len(was))
	}
	for _, r := range before {
		name := appstest.SecretName(r.Cluster, r.Namespace, r.Name)
		sec, old := now[name], was[name]
		want := []string{"set.json"}
		if r.HasToken {
			want = append(want, "token")
		}
		if !slices.Equal(keysOf(&sec), want) || !bytes.Equal(sec.Data["token"], old.Data["token"]) {
			t.Errorf("%s holds %v", r.Name, keysOf(&sec))
		}
		if sec.Labels[apps.LabelComponent] != apps.ComponentValue || sec.Labels[apps.LabelCluster] != r.Cluster || sec.Labels[apps.LabelAppNamespace] != r.Namespace || sec.Labels[apps.LabelAppName] != r.Name {
			t.Errorf("%s is labelled %v", r.Name, sec.Labels)
		}
	}
	for _, name := range []string{"b221-app-newer", "b221-app-broken"} {
		if !reflect.DeepEqual(now[name], was[name]) {
			t.Errorf("%s, which could not be read, was changed", name)
		}
	}
	if got := list(t, s); !reflect.DeepEqual(got, before) {
		t.Fatalf("after the rewrite:\ngot  %+v\nwant %+v", got, before)
	}
	if _, tok, err := s.Get(ctx, "local", "web", "shop"); err != nil || tok != token {
		t.Fatalf("the token after the rewrite: %q %v", tok, err)
	}
	if !strings.Contains(logs.String(), "b221-app-newer") || !strings.Contains(logs.String(), "b221-app-broken") || strings.Contains(logs.String(), "kept: true") || strings.Contains(logs.String(), "not a record") {
		t.Fatalf("the log of what was left:\n%s", logs.String())
	}

	// A second start: nothing to copy, nothing to rewrite, nothing changed.
	n, err = apps.CopyFromSecrets(ctx, d, old, log)
	if err != nil || n != 0 {
		t.Fatalf("a second copy: %d %v", n, err)
	}
	n, err = apps.RewriteSecrets(ctx, d, old, log)
	if err != nil || n != 0 {
		t.Fatalf("a second rewrite: %d %v", n, err)
	}
	if again := secrets(t, cs); !reflect.DeepEqual(again, now) {
		t.Fatal("a second run changed a Secret")
	}
	for _, text := range []string{"hunter2", "image.tag", "replicas=5", token} {
		if held := dbtest.Holding(t, d, text); len(held) != 0 {
			t.Fatalf("%q is in the database: %v", text, held)
		}
	}
}

// A rewrite that stops half way loses nothing: the Secrets not rewritten
// yet are read as they are, and the next run does the rest. A Secret that
// changes between its read and its write is read again and rewritten over
// what it holds then.
func TestARewriteCutShortIsFinishedLater(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	cs := appstest.Cluster()
	old := appstest.Secrets(cs)
	for _, name := range []string{"a", "b", "c"} {
		must(t, old.Save(ctx, shop(name), nil))
	}
	before := list(t, old)
	_, err := apps.CopyFromSecrets(ctx, d, old, nil)
	must(t, err)
	s := apps.NewPGStore(d, old, nil)

	away := true
	failing(cs, &away, appstest.SecretName("local", "web", "b"), appstest.SecretName("local", "web", "c"))
	n, err := apps.RewriteSecrets(ctx, d, old, nil)
	if err == nil || n != 1 {
		t.Fatalf("a rewrite cut short: %d %v", n, err)
	}
	if got := keysOf(secretOf(t, cs, shop("b"))); !slices.Equal(got, []string{"spec.json"}) {
		t.Fatalf("a Secret whose rewrite failed holds %v", got)
	}
	if got := list(t, s); !reflect.DeepEqual(got, before) {
		t.Fatalf("half way:\ngot  %+v\nwant %+v", got, before)
	}

	// While c is rewritten, something else writes its Secret: a token.
	away = false
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	raced := false
	cs.PrependReactor("update", "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		sec := a.(k8stesting.UpdateAction).GetObject().(*corev1.Secret)
		if raced || sec.Name != appstest.SecretName("local", "web", "c") {
			return false, nil, nil
		}
		raced = true
		cur, err := cs.Tracker().Get(gvr, appstest.Namespace, sec.Name)
		if err != nil {
			return true, nil, err
		}
		other := cur.(*corev1.Secret).DeepCopy()
		other.Data["token"], other.ResourceVersion = []byte("written meanwhile"), "100000"
		return false, nil, cs.Tracker().Update(gvr, other, appstest.Namespace)
	})
	n, err = apps.RewriteSecrets(ctx, d, old, nil)
	if err != nil || n != 2 || !raced {
		t.Fatalf("the rest: %d %v (raced %v)", n, err, raced)
	}
	c := secretOf(t, cs, shop("c"))
	if !slices.Equal(keysOf(c), []string{"set.json", "token"}) || string(c.Data["token"]) != "written meanwhile" {
		t.Fatalf("the Secret written meanwhile holds %v", keysOf(c))
	}
	before[2].HasToken = true
	if got := list(t, s); !reflect.DeepEqual(got, before) {
		t.Fatalf("after:\ngot  %+v\nwant %+v", got, before)
	}
}

// A repository given a new name is read under it by the applications that
// name it, with nothing of theirs written: the row keeps its key to the
// repository's row, its version and its Secret. A record read before the
// new name and saved after still names the repository. With the
// repositories not in the database, the applications are rewritten to the
// new name, as they were in their Secrets.
func TestARenamedRepositoryIsFollowed(t *testing.T) {
	pg, d, cs := appstest.PG(t, nil)
	ctx := context.Background()
	repos := apps.NewRepos(apps.RepoOptions{Backend: apps.NewReposPG(d), Credentials: credentials.New(credentials.Options{})})
	store := apps.WithRepositories(pg, repos)
	repos.AddUsers(apps.AppRepoUses(store))
	repos.AddRenamer(apps.AppRepoRenamer(store))
	_, _, err := repos.Save(ctx, "charts", apps.RepositoryInput{Kind: apps.KindGit, URL: "https://github.com/ucesys/charts"}, "ann")
	must(t, err)
	rec := shop("shop")
	rec.Source.Repo, rec.Source.Repository = "https://github.com/ucesys/charts", "charts"
	must(t, store.Save(ctx, rec, nil))
	early, _, err := store.Get(ctx, "local", "web", "shop")
	must(t, err)
	row := func() (repository string, version int) {
		t.Helper()
		must(t, d.Pool().QueryRow(ctx, "SELECT coalesce(repository_id::text, ''), version FROM applications WHERE name = 'shop'").Scan(&repository, &version))
		return repository, version
	}
	repository, version := row()
	sec := secretOf(t, cs, rec)
	if repository == "" {
		t.Fatal("the application does not point at its repository's row")
	}

	_, err = repos.Rename(ctx, "charts", "platform-charts")
	must(t, err)
	got, _, err := store.Get(ctx, "local", "web", "shop")
	if err != nil || got.Source.Repository != "platform-charts" || got.Rev != early.Rev {
		t.Fatalf("after the new name: %+v (version %s, was %s): %v", got.Source, got.Rev, early.Rev, err)
	}
	if all := list(t, store); len(all) != 1 || all[0].Source.Repository != "platform-charts" {
		t.Fatalf("the list after the new name: %+v", all)
	}
	if r, v := row(); r != repository || v != version || secretOf(t, cs, rec).ResourceVersion != sec.ResourceVersion {
		t.Fatalf("the application was written: repository %s (was %s), version %d (was %d)", r, repository, v, version)
	}
	// The repository is still in use, under its new name.
	if err := repos.Delete(ctx, "platform-charts"); !errors.Is(err, apps.ErrInUse) {
		t.Fatalf("removing a repository an application names: %v", err)
	}
	// Read before the new name, saved after: it names the old one.
	early.Values = "replicas: 2\n"
	must(t, store.Save(ctx, early, nil))
	if got, _, _ := store.Get(ctx, "local", "web", "shop"); got.Source.Repository != "platform-charts" || got.Values != "replicas: 2\n" {
		t.Fatalf("a save of what was read before the new name: %+v", got.Source)
	}
	if r, _ := row(); r != repository {
		t.Fatalf("the application points at %q, was %s", r, repository)
	}
	// A repository that is gone: the application reads the name it saved.
	exec(t, d, "DELETE FROM repositories")
	if got, _, _ := pg.Get(ctx, "local", "web", "shop"); got.Source.Repository != "charts" {
		t.Fatalf("with its repository gone: %+v", got.Source)
	}

	// The repositories kept elsewhere: the applications follow by a save.
	mem := apps.NewRepos(apps.RepoOptions{Credentials: credentials.New(credentials.Options{})})
	other := apps.WithRepositories(pg, mem)
	mem.AddRenamer(apps.AppRepoRenamer(other))
	_, _, err = mem.Save(ctx, "mirror", apps.RepositoryInput{Kind: apps.KindGit, URL: "https://github.com/ucesys/mirror"}, "ann")
	must(t, err)
	blog := shop("blog")
	blog.Source.Repo, blog.Source.Repository = "https://github.com/ucesys/mirror", "mirror"
	must(t, other.Save(ctx, blog, nil))
	_, err = mem.Rename(ctx, "mirror", "mirror-2")
	must(t, err)
	if got, _ := read(t, other, blog); got.Source.Repository != "mirror-2" || got.Values != blog.Values {
		t.Fatalf("followed by a save: %+v", got.Source)
	}
}

// No value of an application is in the database, in a log line or in an
// error: every secret part is filled with a marker, the store and the
// move are put through everything they do, failures among it, and the
// marker is looked for in every row of every table and in everything
// logged and returned.
func TestNoValueOfAnApplicationIsEverInTheDatabase(t *testing.T) {
	const marker = "MARKERq7Zk2x9secret"
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s, d, cs := appstest.PG(t, log)
	ctx := context.Background()
	var said []string
	note := func(err error) {
		if err != nil {
			said = append(said, err.Error())
		}
	}
	filled := func(name string) apps.Record {
		var r apps.Record
		dbtest.Fill(&r)
		r.Rev, r.Name, r.CommitSubject = "", name, "KNOWNsubject"
		r.Values = "db:\n  password: " + marker + "-values\n"
		r.Params = []string{"db.password=" + marker + "-param", marker + "-key=1"}
		for i := range r.Commands {
			r.Commands[i].Note = "KNOWNnote"
			r.Commands[i].Params = []string{"token=" + marker + "-command"}
		}
		return r
	}
	token := marker + "-token"

	// The store: made, read, listed, changed, refused, failed, removed.
	must(t, s.Save(ctx, filled("made"), &token))
	must(t, s.Save(ctx, filled("removed"), &token))
	made := filled("made")
	got, _, err := s.Get(ctx, made.Cluster, made.Namespace, "made")
	must(t, err)
	stale := got
	got.Values += "more: " + marker + "-changed\n"
	must(t, s.Save(ctx, got, nil))
	stale.Values = marker + "-stale"
	note(s.Save(ctx, stale, &token))
	away := true
	failing(cs, &away)
	fresh, _, err := s.Get(ctx, made.Cluster, made.Namespace, "made")
	must(t, err)
	fresh.Values = marker + "-unwritten"
	note(s.Save(ctx, fresh, &token))
	note(s.Save(ctx, filled("unmade"), &token))
	away = false
	list(t, s)
	must(t, s.Delete(ctx, made.Cluster, made.Namespace, "removed"))
	// A Secret that is gone, and one that cannot be read.
	must(t, s.Save(ctx, filled("lost"), &token))
	must(t, cs.CoreV1().Secrets(appstest.Namespace).Delete(ctx, appstest.SecretName(made.Cluster, made.Namespace, "lost"), metav1.DeleteOptions{}))
	must(t, s.Save(ctx, filled("unreadable"), &token))
	sec := secretOf(t, cs, filled("unreadable"))
	sec.Data["set.json"] = []byte(marker + " is not what the console wrote")
	_, err = cs.CoreV1().Secrets(appstest.Namespace).Update(ctx, sec, metav1.UpdateOptions{})
	must(t, err)
	list(t, s)
	_, _, err = s.Unmatched(ctx)
	note(err)

	// The move: Secrets of before it, one whose record cannot be read and
	// one of a newer format, a rewrite that fails and one that goes through.
	old := appstest.Secrets(cs)
	must(t, old.Save(ctx, filled("moved"), &token))
	must(t, old.Save(ctx, filled("moved late"), &token))
	labels := map[string]string{apps.LabelComponent: apps.ComponentValue}
	for name, spec := range map[string]string{"b221-app-newer": `{"version":99,"values":"` + marker + `"}`, "b221-app-broken": marker + " is not a record", "b221-app-typed": `{"version":"` + marker + `"}`} {
		_, err := cs.CoreV1().Secrets(appstest.Namespace).Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: appstest.Namespace, Labels: labels},
			Data: map[string][]byte{"spec.json": []byte(spec), "token": []byte(token)}}, metav1.CreateOptions{})
		must(t, err)
	}
	n, err := apps.CopyFromSecrets(ctx, d, old, log)
	if err != nil || n != 2 {
		t.Fatalf("copied %d: %v", n, err)
	}
	away = true
	_, err = apps.RewriteSecrets(ctx, d, old, log)
	if err == nil {
		t.Fatal("a rewrite whose Secrets cannot be written did not fail")
	}
	note(err)
	away = false
	if n, err := apps.RewriteSecrets(ctx, d, old, log); err != nil || n != 2 {
		t.Fatalf("rewrote %d: %v", n, err)
	}
	if got, tok := read(t, s, filled("moved")); got.Values != filled("moved").Values || tok != token {
		t.Fatal("the moved application lost its values or its token")
	}

	if held := dbtest.Holding(t, d, marker); len(held) != 0 {
		t.Fatalf("the marker is in the database: %v", held)
	}
	// The look is not an empty one: what is not secret is found, and the
	// Secrets do hold the marker.
	if held := dbtest.Holding(t, d, "KNOWNsubject"); held["applications"] != 5 {
		t.Fatalf("the applications' rows were not looked in: %v", held)
	}
	if held := dbtest.Holding(t, d, "KNOWNnote"); held["application_commands"] != 10 {
		t.Fatalf("the commands' rows were not looked in: %v", held)
	}
	if sec := secretOf(t, cs, filled("moved")); !bytes.Contains(sec.Data["set.json"], []byte(marker+"-command")) || !bytes.Contains(sec.Data["set.json"], []byte(marker+"-values")) {
		t.Fatal("the Secret does not hold the secret part")
	}
	if len(said) != 4 {
		t.Fatalf("%d errors, not the four the test makes: %v", len(said), said)
	}
	if text := logs.String() + strings.Join(said, "\n"); strings.Contains(text, marker) {
		t.Fatalf("the marker was logged or returned:\n%s", text)
	}
	if !strings.Contains(logs.String(), "reads with no values") || !strings.Contains(logs.String(), "b221-app-broken") {
		t.Fatalf("the log does not say what the test met:\n%s", logs.String())
	}
}
