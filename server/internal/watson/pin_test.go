package watson

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/apps"
)

// moveBranch commits on the branch the repository is on.
func moveBranch(t *testing.T, dir, name, text string) {
	t.Helper()
	r, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	w, err := r.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Add("."); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Commit("moved on", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@example.com", When: time.Now()}}); err != nil {
		t.Fatal(err)
	}
}

func serveCommits(t *testing.T, dir string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("[uploadpack]\n\tallowAnySHA1InWant = true\n"); err != nil {
		t.Fatal(err)
	}
}

func pinSetup(t *testing.T, o Options) (*Service, Cluster, *apps.MemoryStore) {
	t.Helper()
	apps.AllowLocalRepos = true
	t.Cleanup(func() { apps.AllowLocalRepos = false })
	store := apps.NewMemoryStore()
	o.Apps = store
	svc, cl, _, _, _ := setup(t, o)
	return svc, cl, store
}

// card submits a line and waits for its plan.
func card(t *testing.T, svc *Service, cl Cluster, who Who, line string) Item {
	t.Helper()
	it := svc.Submit(context.Background(), cl, who, line, "typed", "", false)[0]
	return wait(t, svc, it.ID, func(i Item) bool { return i.Status != StatusPending || (i.Plan != nil && i.Plan.Ready) })
}

func manager(w Who) Who {
	w.ManageApps = true
	return w
}

func TestADeployRunsTheCommitItsCardShowedAfterTheBranchMoves(t *testing.T) {
	svc, cl, store := pinSetup(t, Options{})
	repo := gitChart(t)
	serveCommits(t, repo)
	line := "helm install hello --git " + repo + " --path charts/web --ref master -n web"
	it := card(t, svc, cl, alice(), line)
	if it.Status != StatusPending || !strings.Contains(strings.Join(it.Plan.Impact, " "), "at commit") {
		t.Fatalf("%s %v %v", it.Status, it.Plan.Impact, it.Plan.DryRun)
	}
	commit := it.Plan.pin.commit
	moveBranch(t, repo, "charts/web/values.yaml", "replicas: 9\nimage: nginx:bad\n")
	if _, err := svc.Approve(context.Background(), cl, alice(), it.ID); err != nil {
		t.Fatal(err)
	}
	done := wait(t, svc, it.ID, func(i Item) bool { return finished(i.Status) })
	if done.Status != StatusDone {
		t.Fatalf("%s: %s", done.Status, outputOf(svc, it.ID))
	}
	rec, _, err := store.Get(context.Background(), "local", "web", "hello")
	if err != nil || rec.Commit != commit {
		t.Fatalf("the record says %q, the card %q: %v", rec.Commit, commit, err)
	}
}

func TestADeployIsRefusedWhenTheRefCannotBePinned(t *testing.T) {
	svc, cl, store := pinSetup(t, Options{})
	repo := gitChart(t)
	it := card(t, svc, cl, alice(), "helm install hello --git "+repo+" --path charts/web --ref master -n web")
	moveBranch(t, repo, "charts/web/values.yaml", "replicas: 9\nimage: nginx:bad\n")
	if _, err := svc.Approve(context.Background(), cl, alice(), it.ID); err != nil {
		t.Fatal(err)
	}
	done := wait(t, svc, it.ID, func(i Item) bool { return finished(i.Status) })
	if out := outputOf(svc, it.ID); done.Status != StatusFailed || !strings.Contains(out, "cannot be fetched") || !strings.Contains(out, "fresh card") {
		t.Fatalf("%s: %s", done.Status, out)
	}
	if _, _, err := store.Get(context.Background(), "local", "web", "hello"); err == nil {
		t.Fatal("a record was saved for a deploy that did not run")
	}
}

func TestADeployWithACardThatPinnedNothingIsRefused(t *testing.T) {
	svc, cl, _ := pinSetup(t, Options{})
	it := card(t, svc, cl, alice(), "helm install hello --git /nothing/here --ref master -n web")
	if it.Plan.pin.commit != "" {
		t.Fatalf("pinned %+v", it.Plan.pin)
	}
	if _, err := svc.Approve(context.Background(), cl, alice(), it.ID); err != nil {
		t.Fatal(err)
	}
	wait(t, svc, it.ID, func(i Item) bool { return finished(i.Status) })
	if out := outputOf(svc, it.ID); !strings.Contains(out, "could not settle on a commit") {
		t.Fatal(out)
	}
}

func TestADeployIsRefusedWhenTheValuesChangedSinceTheCard(t *testing.T) {
	svc, cl, store := pinSetup(t, Options{})
	repo := gitChart(t)
	ctx := context.Background()
	src := apps.Source{Repo: repo, Path: "charts/web", Ref: "master", RefKind: apps.RefBranch}
	if err := store.Save(ctx, apps.Record{Cluster: "local", Namespace: "web", Name: "hello", Source: src, Values: "replicas: 2\n"}, nil); err != nil {
		t.Fatal(err)
	}
	who := manager(alice())
	it := card(t, svc, cl, who, "helm install hello --git "+repo+" --path charts/web --ref master -n web --app")
	rec, _, _ := store.Get(ctx, "local", "web", "hello")
	rec.Values = "replicas: 7\n"
	if err := store.Save(ctx, rec, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Approve(ctx, cl, who, it.ID); err != nil {
		t.Fatal(err)
	}
	done := wait(t, svc, it.ID, func(i Item) bool { return finished(i.Status) })
	if out := outputOf(svc, it.ID); done.Status != StatusFailed || !strings.Contains(out, "values changed after this card was made") {
		t.Fatalf("%s: %s", done.Status, out)
	}
}

func TestASecondPersonApprovesThePinnedDeploy(t *testing.T) {
	svc, cl, store := pinSetup(t, Options{TwoPerson: func(Cluster) bool { return true }})
	repo := gitChart(t)
	serveCommits(t, repo)
	ctx := context.Background()
	src := apps.Source{Repo: repo, Path: "charts/web", Ref: "master", RefKind: apps.RefBranch}
	if err := store.Save(ctx, apps.Record{Cluster: "local", Namespace: "web", Name: "hello", Source: src, Values: "replicas: 2\n"}, nil); err != nil {
		t.Fatal(err)
	}
	it := card(t, svc, cl, manager(alice()), "helm install hello --git "+repo+" --path charts/web --ref master -n web --app")
	commit := it.Plan.pin.commit
	moveBranch(t, repo, "charts/web/values.yaml", "replicas: 9\nimage: nginx:bad\n")
	if _, err := svc.Approve(ctx, cl, manager(bob()), it.ID); err != nil {
		t.Fatal(err)
	}
	done := wait(t, svc, it.ID, func(i Item) bool { return finished(i.Status) })
	if done.Status != StatusDone {
		t.Fatalf("%s: %s", done.Status, outputOf(svc, it.ID))
	}
	if rec, _, err := store.Get(ctx, "local", "web", "hello"); err != nil || rec.Commit != commit {
		t.Fatalf("%q, the card %q: %v", rec.Commit, commit, err)
	}
}

func TestAppNeedsTheRightsOfThePersonInThatNamespace(t *testing.T) {
	asked := map[string]bool{}
	access := func(_ context.Context, _ Cluster, who Who, ns string) (bool, error) {
		asked[who.User+"/"+ns] = true
		return ns == "team-a", nil
	}
	svc, cl, store := pinSetup(t, Options{AppAccess: access})
	ctx := context.Background()
	if err := store.Save(ctx, apps.Record{Cluster: "local", Namespace: "team-b", Name: "kept", Source: apps.Source{Repo: "https://git.invalid/a/secret-place", Ref: "main", RefKind: apps.RefBranch}}, nil); err != nil {
		t.Fatal(err)
	}
	who := manager(alice())
	kept := svc.Submit(ctx, cl, who, "helm upgrade kept -n team-b --app --dry-run=client", "typed", "", true)[0]
	none := svc.Submit(ctx, cl, who, "helm upgrade other -n team-b --app --dry-run=client", "typed", "", true)[0]
	if kept.Status != StatusRefused || kept.Rule != "apps" || kept.Reason != none.Reason || strings.Contains(kept.Reason, "secret-place") {
		t.Fatalf("%s %q, and with no record %q", kept.Status, kept.Reason, none.Reason)
	}
	if !asked[who.User+"/team-b"] {
		t.Fatal("Kubernetes was not asked")
	}
}

func TestWhoeverApprovesAnAppCommandNeedsTheRightsToo(t *testing.T) {
	access := func(_ context.Context, _ Cluster, who Who, ns string) (bool, error) {
		return who.User == alice().User, nil
	}
	svc, cl, store := pinSetup(t, Options{AppAccess: access, TwoPerson: func(Cluster) bool { return true }})
	repo := gitChart(t)
	ctx := context.Background()
	src := apps.Source{Repo: repo, Path: "charts/web", Ref: "master", RefKind: apps.RefBranch}
	if err := store.Save(ctx, apps.Record{Cluster: "local", Namespace: "web", Name: "hello", Source: src}, nil); err != nil {
		t.Fatal(err)
	}
	it := card(t, svc, cl, manager(alice()), "helm install hello --git "+repo+" --path charts/web --ref master -n web --app")
	if _, err := svc.Approve(ctx, cl, manager(bob()), it.ID); err != nil {
		t.Fatal(err)
	}
	done := wait(t, svc, it.ID, func(i Item) bool { return finished(i.Status) })
	if out := outputOf(svc, it.ID); done.Status != StatusFailed || !strings.Contains(out, "does not let you manage applications in web") {
		t.Fatalf("%s: %s", done.Status, out)
	}
}

// forbidding puts a server before the simulator that refuses the person
// what the rule matches, while the console's own account reads everything.
func forbidding(t *testing.T, cl *Cluster, rule func(r *http.Request) bool) {
	t.Helper()
	sim := cl.Config.Host
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Impersonate-User") != "" && rule(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403,"message":"forbidden"}`))
			return
		}
		req, _ := http.NewRequestWithContext(r.Context(), r.Method, sim+r.URL.RequestURI(), r.Body)
		req.Header = r.Header.Clone()
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer res.Body.Close()
		for k, v := range res.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(res.StatusCode)
		buf := make([]byte, 32<<10)
		for {
			n, err := res.Body.Read(buf)
			w.Write(buf[:n])
			if err != nil {
				return
			}
		}
	}))
	t.Cleanup(front.Close)
	cl.Config = &rest.Config{Host: front.URL}
}

func pods(r *http.Request) bool { return strings.Contains(r.URL.Path, "/pods") }

func TestACardShowsNothingThePersonCannotRead(t *testing.T) {
	svc, cl, _, _, _ := setup(t, Options{})
	cs, err := kubernetes.NewForConfig(cl.Config)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	list, err := cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil || len(list.Items) == 0 {
		t.Fatalf("%v", err)
	}
	p := list.Items[0]
	p.Annotations = map[string]string{"note": "hidden-value"}
	if _, err := cs.CoreV1().Pods(p.Namespace).Update(ctx, &p, metav1.UpdateOptions{}); err != nil {
		t.Skipf("the simulator keeps no annotation: %v", err)
	}
	forbidding(t, &cl, pods)
	it := card(t, svc, cl, alice(), "kubectl annotate pod "+p.Name+" -n "+p.Namespace+" note-")
	text := strings.Join(append(it.Plan.Targets, it.Plan.Impact...), "\n")
	if strings.Contains(text, "hidden-value") || !strings.Contains(text, "could not read") || !strings.Contains(text, "leaves it out") {
		t.Fatalf("%s", text)
	}
	if len(it.Plan.guards) != 0 {
		t.Fatalf("guards on what the card could not read: %+v", it.Plan.guards)
	}
}

func TestACardsSelectorIsReadAsThePerson(t *testing.T) {
	svc, cl, _, _, _ := setup(t, Options{})
	forbidding(t, &cl, pods)
	it := card(t, svc, cl, alice(), "kubectl delete pod -n web -l app=x")
	text := strings.Join(it.Plan.Impact, "\n")
	if !strings.Contains(text, "could not read") || strings.Contains(text, "No pod matches") {
		t.Fatalf("%s", text)
	}
}

func TestACordonCardSaysItCouldNotReadThePodsAndStaysValid(t *testing.T) {
	svc, cl, _, _, _ := setup(t, Options{CheckEvery: 20 * time.Millisecond})
	forbidding(t, &cl, pods)
	it := card(t, svc, cl, alice(), "kubectl drain worker-001 --ignore-daemonsets")
	text := strings.Join(it.Plan.Impact, "\n")
	if !strings.Contains(text, "could not read the pods on worker-001") || strings.Contains(text, "no pod needs to be evicted") {
		t.Fatalf("%s", text)
	}
	time.Sleep(200 * time.Millisecond)
	if now, _, _ := svc.Snapshot(it.ID, 0); now.Status != StatusPending {
		t.Fatalf("the card lapsed: %s", now.Reason)
	}
}

func TestADeployCardStillPinsWhenTheReleaseCannotBeRead(t *testing.T) {
	apps.AllowLocalRepos = true
	t.Cleanup(func() { apps.AllowLocalRepos = false })
	svc, cl, _, _, _ := setup(t, Options{Apps: apps.NewMemoryStore()})
	forbidding(t, &cl, func(r *http.Request) bool { return strings.Contains(r.URL.Path, "/secrets") })
	repo := gitChart(t)
	it := card(t, svc, cl, alice(), "helm upgrade hello --git "+repo+" --path charts/web --ref master -n web")
	text := strings.Join(it.Plan.Impact, "\n")
	if !strings.Contains(text, "could not read the release") || !strings.Contains(text, "Deploys chart web") || strings.Contains(text, "There is no release") {
		t.Fatalf("%s", text)
	}
	if it.Plan.pin.commit == "" {
		t.Fatal("no pin")
	}
}
