package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/apps/appstest"
	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/sim"
	"github.com/ucesys/221b/server/internal/watson"
)

// An application's commands (asked October 30, 2026): saved with their
// parameters, unique by name within the application, renamed, run as a
// Watson card carrying the parameters as --set flags, and deleted.
func TestAnApplicationsCommandsAreSavedAndRun(t *testing.T) {
	store := appstest.Store(t)
	simSrv := httptest.NewServer(sim.New(sim.Options{Nodes: 3, Pods: 12, Seed: 7}))
	t.Cleanup(simSrv.Close)
	srv, c, m, _, local, liveStore := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		o.Apps = store
		o.Watson = watson.New(watson.Options{Audit: o.Audit, Logger: quietLog(), CheckEvery: 50 * time.Millisecond})
	})
	ctx := context.Background()
	// Watson wants the local connection running, as watsonSetup has it.
	local.restCfg = &rest.Config{Host: simSrv.URL}
	if err := m.SetLocal(ctx, clusters.Spec{Name: "Here", Env: clusters.EnvOther, Mode: clusters.ModeWrite, Server: simSrv.URL, Auth: clusters.AuthInCluster, CA: clusters.CAPEM}, local, liveStore); err != nil {
		t.Fatal(err)
	}
	rec := apps.Record{Cluster: clusters.LocalID, Namespace: "web", Name: "shop", Source: apps.Source{Repo: "https://git.invalid/example/shop", Ref: "main", RefKind: apps.RefBranch}, Params: []string{"replicas=2"}}
	if err := store.Save(ctx, rec, nil); err != nil {
		t.Fatal(err)
	}
	base := srv.URL + "/api/v1/apps/web/shop/commands/"

	resp, body := send(t, c, http.MethodPut, base+"scale%20up", `{"params":["replicas=5","image.tag=v2"],"note":"more pods"}`)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"commands":[{"name":"scale up"`) || !strings.Contains(body, `"note":"more pods"`) {
		t.Fatalf("saving a command: %s %s", resp.Status, body)
	}
	for _, bad := range []string{`{"params":["nokv"]}`, `{"params":["a='x'"]}`, `{"params":["a=$HOME"]}`, `{"params":[]}`, `{"params":["a=b"],"name":"-dash"}`, `{"params":["a=b"],"note":"` + strings.Repeat("n", 49) + `"}`} {
		if resp, body := send(t, c, http.MethodPut, base+"bad", bad); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s was accepted: %s %s", bad, resp.Status, body)
		}
	}
	if resp, body := send(t, c, http.MethodPut, base+"other", `{"name":"Scale Up","params":["a=b"]}`); resp.StatusCode != http.StatusConflict {
		t.Fatalf("a second command with the same name, in another case: %s %s", resp.Status, body)
	}
	if resp, body := send(t, c, http.MethodPut, base+"scale%20up", `{"name":"Scale up more","params":["replicas=9"]}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("renaming a command: %s %s", resp.Status, body)
	}
	got, _, err := store.Get(ctx, clusters.LocalID, "web", "shop")
	if err != nil || len(got.Commands) != 1 || got.Commands[0].Name != "Scale up more" || len(got.Commands[0].Params) != 1 || got.Commands[0].Params[0] != "replicas=9" || got.Commands[0].CreatedBy == "" {
		t.Fatalf("the record after the rename: %+v, %v", got.Commands, err)
	}

	resp, body = send(t, c, http.MethodPost, base+"Scale%20up%20more/run", "")
	if resp.StatusCode != http.StatusAccepted || !strings.Contains(body, `helm upgrade shop --git https://git.invalid/example/shop --ref main -n web --app --set 'replicas=9'`) {
		t.Fatalf("running a command: %s %s", resp.Status, body)
	}
	if resp, _ := send(t, c, http.MethodPost, base+"nothing/run", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("running a command that is not there: %s", resp.Status)
	}

	if resp, body := send(t, c, http.MethodDelete, base+"scale%20up%20more", ""); resp.StatusCode != http.StatusOK || strings.Contains(body, `"commands"`) {
		t.Fatalf("deleting a command: %s %s", resp.Status, body)
	}
	if resp, _ := send(t, c, http.MethodDelete, base+"scale%20up%20more", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("deleting it again: %s", resp.Status)
	}
	// A viewer may not.
	v := signIn(t, srv, "viewer@corp.example")
	if resp, _ := send(t, v, http.MethodPut, base+"x", `{"params":["a=b"]}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a viewer saved a command: %s", resp.Status)
	}
}

// DB-74: a saved command run is recorded, in the audit log and the
// console's log, with the values of its parameters hidden and their names
// kept; the card itself still shows the line whole to who approves it.
func TestASavedCommandsParametersAreNotRecorded(t *testing.T) {
	store, events := appstest.Store(t), &audit.MemoryStore{}
	simSrv := httptest.NewServer(sim.New(sim.Options{Nodes: 3, Pods: 12, Seed: 7}))
	t.Cleanup(simSrv.Close)
	srv, c, m, logs, local, liveStore := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		o.Apps = store
		o.Audit.SetStore(events)
		o.Watson = watson.New(watson.Options{Audit: o.Audit, Logger: quietLog(), CheckEvery: 50 * time.Millisecond})
	})
	ctx := context.Background()
	local.restCfg = &rest.Config{Host: simSrv.URL}
	if err := m.SetLocal(ctx, clusters.Spec{Name: "Here", Env: clusters.EnvOther, Mode: clusters.ModeWrite, Server: simSrv.URL, Auth: clusters.AuthInCluster, CA: clusters.CAPEM}, local, liveStore); err != nil {
		t.Fatal(err)
	}
	rec := apps.Record{Cluster: clusters.LocalID, Namespace: "web", Name: "shop", Source: apps.Source{Repo: "https://git.invalid/example/shop", Ref: "main", RefKind: apps.RefBranch},
		Commands: []apps.Command{{Name: "rotate", Params: []string{"db.password=MARKPW74", "replicas=3"}}}}
	if err := store.Save(ctx, rec, nil); err != nil {
		t.Fatal(err)
	}
	resp, body := send(t, c, http.MethodPost, srv.URL+"/api/v1/apps/web/shop/commands/rotate/run", "")
	if resp.StatusCode != http.StatusAccepted || !strings.Contains(body, "--set 'db.password=MARKPW74'") {
		t.Fatalf("running the command: %s %s", resp.Status, body)
	}
	list, _ := events.List(ctx)
	var recorded []string
	for _, e := range list {
		if strings.HasPrefix(e.Action, "watson.") {
			recorded = append(recorded, e.Detail)
		}
	}
	// The names stay; after db.password= the audit log's own scrubbing
	// may cut further (it redacts what follows password=).
	all := strings.Join(recorded, "\n")
	if len(recorded) == 0 || !strings.Contains(all, `--set 'db.password=`) || !strings.Contains(all, `--set 'replicas=***'`) {
		t.Fatalf("the audit events of the run:\n%s", all)
	}
	if strings.Contains(all, "MARKPW74") || strings.Contains(logs.String(), "MARKPW74") {
		t.Fatalf("a parameter's value is recorded:\n%s\n%s", all, logs.String())
	}
}

// staleStore is a store where someone else saved first.
type staleStore struct{ apps.Store }

func (staleStore) Save(context.Context, apps.Record, *string) error { return apps.ErrConflict }

// A save that lost to another says so, and asks to look again, rather
// than overwrite it.
func TestASaveThatLostToAnotherIsAConflict(t *testing.T) {
	store := appstest.Store(t)
	ctx := context.Background()
	rec := apps.Record{Cluster: clusters.LocalID, Namespace: "web", Name: "shop", Source: apps.Source{Repo: "https://git.invalid/example/shop", Ref: "main", RefKind: apps.RefBranch}}
	if err := store.Save(ctx, rec, nil); err != nil {
		t.Fatal(err)
	}
	simSrv := httptest.NewServer(sim.New(sim.Options{Nodes: 3, Pods: 12, Seed: 7}))
	t.Cleanup(simSrv.Close)
	srv, c, m, _, local, liveStore := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		o.Apps = staleStore{store}
		o.Watson = watson.New(watson.Options{Audit: o.Audit, Logger: quietLog()})
	})
	local.restCfg = &rest.Config{Host: simSrv.URL}
	if err := m.SetLocal(ctx, clusters.Spec{Name: "Here", Env: clusters.EnvOther, Mode: clusters.ModeWrite, Server: simSrv.URL, Auth: clusters.AuthInCluster, CA: clusters.CAPEM}, local, liveStore); err != nil {
		t.Fatal(err)
	}
	resp, body := send(t, c, http.MethodPut, srv.URL+"/api/v1/apps/web/shop/commands/scale", `{"params":["replicas=5"]}`)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, "changed while") {
		t.Fatalf("saving a command: %s %s", resp.Status, body)
	}
}
