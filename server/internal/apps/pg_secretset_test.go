package apps_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/apps/appstest"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kube/kubetest"
)

// An application has a Secret only when it has something secret: values,
// parameters, commands' parameters or a repository token (step 0051). Its
// row says which (applications.is_secret_set), so a Secret that is lost is
// told apart from one that was never needed.

// bareApp is an application with nothing secret: no values, no parameters,
// and commands without parameters.
func bareApp(name string) apps.Record {
	r := shop(name)
	r.Values, r.Params = "", nil
	r.Commands = slices.Clone(r.Commands)
	for i := range r.Commands {
		r.Commands[i].Params = nil
	}
	return r
}

// secretSet is what the row of an application says of its Secret.
func secretSet(t *testing.T, d *db.DB, r apps.Record) bool {
	t.Helper()
	var set bool
	must(t, d.Pool().QueryRow(context.Background(), "SELECT is_secret_set FROM applications WHERE cluster_id = $1 AND namespace = $2 AND name = $3",
		r.Cluster, r.Namespace, r.Name).Scan(&set))
	return set
}

// hasSecret says the application's Secret is there.
func hasSecret(t *testing.T, cs *fake.Clientset, r apps.Record) bool {
	t.Helper()
	_, err := cs.CoreV1().Secrets(appstest.Namespace).Get(context.Background(), appstest.SecretName(r.Cluster, r.Namespace, r.Name), metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	return err == nil
}

// emptySecret makes the Secret a console before step 0051 made for an
// application with nothing secret: "{}" under set.json, and the token given.
func emptySecret(t *testing.T, cs *fake.Clientset, r apps.Record, token string) {
	t.Helper()
	data := map[string][]byte{"set.json": []byte("{}")}
	if token != "" {
		data["token"] = []byte(token)
	}
	_, err := cs.CoreV1().Secrets(appstest.Namespace).Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: appstest.SecretName(r.Cluster, r.Namespace, r.Name), Namespace: appstest.Namespace,
			Labels: map[string]string{apps.LabelComponent: apps.ComponentValue, apps.LabelCluster: r.Cluster}},
		Data: data}, metav1.CreateOptions{})
	must(t, err)
}

// A save of an application with nothing secret makes no Secret, and its
// row says it has none; it reads whole, without a word in the log, and is
// not missing a Secret. Values given make the Secret; taken away, they
// delete it. A token alone keeps a Secret, with the token, through saves
// that give none; an empty token removes it and the Secret with it.
func TestAnApplicationWithNothingSecretHasNoSecret(t *testing.T) {
	var logs bytes.Buffer
	s, d, cs := appstest.PG(t, slog.New(slog.NewTextHandler(&logs, nil)))
	ctx := context.Background()
	bare := bareApp("bare")
	must(t, s.Save(ctx, bare, nil))
	if hasSecret(t, cs, bare) || secretSet(t, d, bare) {
		t.Fatalf("an application with nothing secret: Secret %v, is_secret_set %v", hasSecret(t, cs, bare), secretSet(t, d, bare))
	}
	for range 2 {
		if got, tok := read(t, s, bare); !equal(got, bare) || tok != "" {
			t.Fatalf("got  %+v\nwant %+v", got, bare)
		}
		if got := list(t, s); len(got) != 1 || !equal(got[0], bare) {
			t.Fatalf("the list: %+v", got)
		}
	}
	if noSecret, noRow, err := s.Unmatched(ctx); err != nil || len(noSecret) != 0 || len(noRow) != 0 {
		t.Fatalf("unmatched: %v %v %v", noSecret, noRow, err)
	}
	if strings.Contains(logs.String(), "reads with no values") {
		t.Fatalf("an application with no Secret to have was said:\n%s", logs.String())
	}

	// Values given make the Secret; taken away, they delete it.
	got, _, err := s.Get(ctx, "local", "web", "bare")
	must(t, err)
	got.Values = "replicas: 2\n"
	must(t, s.Save(ctx, got, nil))
	if !hasSecret(t, cs, bare) || !secretSet(t, d, bare) {
		t.Fatal("values given made no Secret")
	}
	got, _, err = s.Get(ctx, "local", "web", "bare")
	must(t, err)
	if got.Values != "replicas: 2\n" {
		t.Fatalf("the values: %q", got.Values)
	}
	got.Values = ""
	must(t, s.Save(ctx, got, nil))
	if hasSecret(t, cs, bare) || secretSet(t, d, bare) {
		t.Fatal("values taken away left the Secret")
	}

	// A token alone keeps a Secret, which holds it.
	token := "ghp-only-a-token"
	got, _, err = s.Get(ctx, "local", "web", "bare")
	must(t, err)
	must(t, s.Save(ctx, got, &token))
	if !hasSecret(t, cs, bare) || !secretSet(t, d, bare) || string(secretOf(t, cs, bare).Data["token"]) != token {
		t.Fatal("an application with only a token has no Secret holding it")
	}
	got, _, err = s.Get(ctx, "local", "web", "bare")
	must(t, err)
	got.Commit = "0123456789ab"
	must(t, s.Save(ctx, got, nil))
	if _, tok := read(t, s, bare); tok != token || !secretSet(t, d, bare) {
		t.Fatalf("a save that gives no token lost it: %q", tok)
	}
	none := ""
	got, _, err = s.Get(ctx, "local", "web", "bare")
	must(t, err)
	must(t, s.Save(ctx, got, &none))
	if hasSecret(t, cs, bare) || secretSet(t, d, bare) {
		t.Fatal("the token removed left the Secret")
	}
	if got, tok := read(t, s, bare); got.HasToken || tok != "" {
		t.Fatalf("the token removed: %q %v", tok, got.HasToken)
	}
	if strings.Contains(logs.String(), "reads with no values") {
		t.Fatalf("said:\n%s", logs.String())
	}
}

// equal compares two records but their version.
func equal(a, b apps.Record) bool {
	a.Rev, b.Rev = "", ""
	return reflect.DeepEqual(a, b)
}

// A Secret that holds nothing, as a console before step 0051 made, is
// deleted by the next save of its application, which then says it has
// none; a Secret of a removed application of the same name is too.
func TestASaveDeletesASecretThatHoldsNothing(t *testing.T) {
	s, d, cs := appstest.PG(t, nil)
	ctx := context.Background()
	bare := bareApp("bare")
	must(t, s.Save(ctx, bare, nil))
	exec(t, d, "UPDATE applications SET is_secret_set = true")
	emptySecret(t, cs, bare, "")
	got, _, err := s.Get(ctx, "local", "web", "bare")
	must(t, err)
	got.Commit = "0123456789ab"
	must(t, s.Save(ctx, got, nil))
	if hasSecret(t, cs, bare) || secretSet(t, d, bare) {
		t.Fatalf("the save kept the Secret that holds nothing: Secret %v, is_secret_set %v", hasSecret(t, cs, bare), secretSet(t, d, bare))
	}
	// A new application over the Secret of one removed, token and all.
	fresh := bareApp("fresh")
	emptySecret(t, cs, fresh, "ghp-of-the-removed-one")
	must(t, s.Save(ctx, fresh, nil))
	if hasSecret(t, cs, fresh) || secretSet(t, d, fresh) {
		t.Fatal("a new application kept the Secret of a removed one")
	}
	// One that holds a record never copied is written, as before, and kept.
	uncopied := bareApp("uncopied")
	must(t, appstest.Secrets(cs).Save(ctx, uncopied, nil))
	must(t, s.Save(ctx, uncopied, nil))
	if !hasSecret(t, cs, uncopied) || !secretSet(t, d, uncopied) || !slices.Equal(keysOf(secretOf(t, cs, uncopied)), []string{"set.json"}) {
		t.Fatal("a Secret that held a record never copied was not written as before")
	}
}

// A row that says it has no Secret reads with nothing secret, whatever
// Secret is there, and says nothing.
func TestARowWithoutASecretToHaveReadsWithoutAWord(t *testing.T) {
	var logs bytes.Buffer
	s, d, _ := appstest.PG(t, slog.New(slog.NewTextHandler(&logs, nil)))
	ctx := context.Background()
	rec := shop("shop")
	must(t, s.Save(ctx, rec, nil))
	must(t, s.Save(ctx, bareApp("lost"), nil))
	exec(t, d, "UPDATE applications SET is_secret_set = false WHERE name = 'shop'")
	exec(t, d, "UPDATE applications SET is_secret_set = true WHERE name = 'lost'")
	if got, tok := read(t, s, rec); !equal(got, bareApp("shop")) || tok != "" {
		t.Fatalf("a row with no Secret to have:\ngot  %+v\nwant %+v", got, bareApp("shop"))
	}
	list(t, s)
	if n := strings.Count(logs.String(), "reads with no values"); n != 1 || !strings.Contains(logs.String(), "name=lost") {
		t.Fatalf("said %d times (once, for lost):\n%s", n, logs.String())
	}
}

// Unmatched says an application is without its Secret only when its row
// says it has one.
func TestUnmatchedPassesOverAnApplicationWithNoSecretToHave(t *testing.T) {
	s, d, _ := appstest.PG(t, nil)
	ctx := context.Background()
	must(t, s.Save(ctx, bareApp("bare"), nil))
	must(t, s.Save(ctx, bareApp("lost"), nil))
	exec(t, d, "UPDATE applications SET is_secret_set = true WHERE name = 'lost'")
	noSecret, noRow, err := s.Unmatched(ctx)
	if err != nil || !slices.Equal(noSecret, []string{"local/web/lost"}) || len(noRow) != 0 {
		t.Fatalf("unmatched: %v %v %v", noSecret, noRow, err)
	}
}

// The start's pass deletes a Secret that holds an empty secret part and no
// token, and sets its row to say it has none; a Secret with a token or
// values stays, and a Secret that is not there is left to Unmatched.
func TestTheStartDeletesTheSecretsThatHoldNothing(t *testing.T) {
	var logs bytes.Buffer
	s, d, cs := appstest.PG(t, slog.New(slog.NewTextHandler(&logs, nil)))
	ctx := context.Background()
	for _, name := range []string{"empty", "token", "lost"} {
		must(t, s.Save(ctx, bareApp(name), nil))
	}
	must(t, s.Save(ctx, shop("shop"), nil))
	exec(t, d, "UPDATE applications SET is_secret_set = true")
	emptySecret(t, cs, bareApp("empty"), "")
	emptySecret(t, cs, bareApp("token"), "ghp-kept")
	n, err := s.DropEmptySecrets(ctx)
	if err != nil || n != 1 {
		t.Fatalf("deleted %d: %v", n, err)
	}
	if hasSecret(t, cs, bareApp("empty")) || secretSet(t, d, bareApp("empty")) {
		t.Fatal("the Secret that holds nothing stayed")
	}
	if !hasSecret(t, cs, bareApp("token")) || !secretSet(t, d, bareApp("token")) || !hasSecret(t, cs, shop("shop")) || !secretSet(t, d, shop("shop")) {
		t.Fatal("a Secret that holds something was deleted")
	}
	if !secretSet(t, d, bareApp("lost")) {
		t.Fatal("an application whose Secret is not there was guessed at")
	}
	noSecret, _, err := s.Unmatched(ctx)
	if err != nil || !slices.Equal(noSecret, []string{"local/web/lost"}) {
		t.Fatalf("unmatched: %v %v", noSecret, err)
	}
	if n, err := s.DropEmptySecrets(ctx); err != nil || n != 0 {
		t.Fatalf("a second pass: %d %v", n, err)
	}
	if got, _ := read(t, s, bareApp("empty")); !equal(got, bareApp("empty")) {
		t.Fatalf("after: %+v", got)
	}
	if strings.Contains(logs.String(), "name=empty") {
		t.Fatalf("said:\n%s", logs.String())
	}
}

// The move from 0.12's Secrets: an application copied with nothing secret
// and no token has a row that says it has no Secret, and the rewrite
// deletes its Secret in place of writing "{}" into it; one with only a
// token keeps its Secret, with the token alone beside an empty part.
func TestTheMoveLeavesNoSecretWhereNothingIsSecret(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	d := dbtest.Open(t)
	ctx := context.Background()
	cs := kubetest.Cluster()
	old := appstest.Secrets(cs)
	token := "ghp-of-before-0.12.1"
	must(t, old.Save(ctx, bareApp("bare"), nil))
	must(t, old.Save(ctx, bareApp("tokened"), &token))
	must(t, old.Save(ctx, shop("shop"), nil))
	before := list(t, old)

	n, err := apps.CopyFromSecrets(ctx, d, old, log)
	if err != nil || n != 3 {
		t.Fatalf("copied %d: %v", n, err)
	}
	if secretSet(t, d, bareApp("bare")) || !secretSet(t, d, bareApp("tokened")) || !secretSet(t, d, shop("shop")) {
		t.Fatal("the copy's rows do not say which application has a Secret")
	}
	s := apps.NewPGStore(d, old, log)
	if got := list(t, s); !reflect.DeepEqual(got, before) {
		t.Fatalf("between the copy and the rewrite:\ngot  %+v\nwant %+v", got, before)
	}
	n, err = apps.RewriteSecrets(ctx, d, old, log)
	if err != nil || n != 3 {
		t.Fatalf("rewrote %d: %v", n, err)
	}
	if hasSecret(t, cs, bareApp("bare")) || secretSet(t, d, bareApp("bare")) {
		t.Fatal("the Secret of an application with nothing secret was kept")
	}
	if sec := secretOf(t, cs, bareApp("tokened")); !slices.Equal(keysOf(sec), []string{"set.json", "token"}) || string(sec.Data["token"]) != token || string(sec.Data["set.json"]) != `{"commands":[{"name":"Scale up","params":null},{"name":"Scale down","params":null}]}` {
		t.Fatalf("the Secret of an application with only a token holds %v: %s", keysOf(sec), sec.Data["set.json"])
	}
	if got := list(t, s); !reflect.DeepEqual(got, before) {
		t.Fatalf("after the rewrite:\ngot  %+v\nwant %+v", got, before)
	}
	if noSecret, noRow, err := s.Unmatched(ctx); err != nil || len(noSecret) != 0 || len(noRow) != 0 {
		t.Fatalf("unmatched: %v %v %v", noSecret, noRow, err)
	}
	if strings.Contains(logs.String(), "reads with no values") {
		t.Fatalf("said:\n%s", logs.String())
	}
	if n, err := apps.RewriteSecrets(ctx, d, old, log); err != nil || n != 0 {
		t.Fatalf("a second rewrite: %d %v", n, err)
	}
	if _, _, err := s.Get(ctx, "local", "web", "bare"); errors.Is(err, apps.ErrNotFound) {
		t.Fatal("the application whose Secret was deleted is gone")
	}
}
