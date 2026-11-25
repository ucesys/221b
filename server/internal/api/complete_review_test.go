package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/roles"
)

// A proposal waiting on an alert is not expired as resolved while the
// cluster has not listed every kind (kube#7): an alert made of events, or
// a claim's, is missing until they are listed.
func TestACardWaitsUntilTheClusterIsComplete(t *testing.T) {
	var st *cards.Store
	_, _, _, _, local, store := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) {
		st = cards.New(cards.Options{Audit: o.Audit, Logger: quietLog(), Cluster: func(id string) (cards.ClusterInfo, bool) {
			return cards.ClusterInfo{Name: "Here"}, id == clusters.LocalID
		}})
		st.Register(cards.KindKubectl, &cardExec{print: "v1"})
		o.Cards, o.CardRoles = st, []roles.ID{roles.SuperAdmin}
	})
	set := func(state corev1.ContainerState) {
		local.Update("pods", "web", "shop", func(o runtime.Object) {
			o.(*corev1.Pod).Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", RestartCount: 4, Ready: state.Running != nil, State: state}}
		})
		store.Notify(kube.Change{Info: true, Views: map[string][]string{live.ViewAlerts: {kube.AllRows}}})
		store.Process()
	}
	set(corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}})
	p := agentProposal(clusters.LocalID, cards.KindKubectl)
	p.Condition = cards.Condition{Kind: cards.ConditionAlert, Args: map[string]any{"kind": "pod", "namespace": "web", "name": "shop"}}
	ctx := context.Background()
	id, err := st.Propose(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	// A new leader: the core kinds are listed, the events not yet; the
	// alert's row is missing.
	local.SetInfo(func(i *kube.Info) { i.Complete = false })
	set(corev1.ContainerState{Running: &corev1.ContainerStateRunning{}})
	st.Sweep(ctx)
	if c, _ := st.Get(ctx, id); c.State != cards.Queued {
		t.Fatalf("expired before the cluster was complete: %+v", c)
	}
	local.SetInfo(func(i *kube.Info) { i.Complete = true })
	set(corev1.ContainerState{Running: &corev1.ContainerStateRunning{}})
	st.Sweep(ctx)
	if c, _ := st.Get(ctx, id); c.State != cards.Expired || !strings.Contains(c.Reason, "its alert resolved") {
		t.Fatalf("once complete: %+v", c)
	}
}

// An acknowledgement is not deleted for good because the cluster has not
// listed its alert yet (kube#14): while it is not complete, acknowledging
// one alert leaves the others' acknowledgements as they are.
func TestAcknowledgementsOutliveAClusterNotListedYet(t *testing.T) {
	srv, viewer, _, _, local, store := multiSetupWith(t, "viewer@corp.example", nil)
	fail := func(ns, name string, failing bool) {
		local.Update("pods", ns, name, func(o runtime.Object) {
			p := o.(*corev1.Pod)
			if failing {
				p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", RestartCount: 4, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}}
			} else {
				p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
			}
		})
		store.Notify(kube.Change{Info: true, Views: map[string][]string{live.ViewAlerts: {kube.AllRows}}})
		store.Process()
	}
	fail("web", "shop", true)
	fail("kube-system", "dns", true)
	for _, key := range []string{"pod:web/shop", "pod:kube-system/dns"} {
		if r, b := send(t, viewer, http.MethodPost, srv.URL+"/api/v1/alerts/ack", `{"key":"`+key+`","acknowledge":true}`); r.StatusCode != http.StatusOK {
			t.Fatalf("ack %s: %d %s", key, r.StatusCode, b)
		}
	}
	// A start: the events are not listed yet, so dns's alert is not built.
	local.SetInfo(func(i *kube.Info) { i.Complete = false })
	fail("kube-system", "dns", false)
	if r, b := send(t, viewer, http.MethodPost, srv.URL+"/api/v1/alerts/ack", `{"key":"pod:web/shop","acknowledge":false}`); r.StatusCode != http.StatusOK {
		t.Fatalf("unack: %d %s", r.StatusCode, b)
	}
	// Listed: dns's alert is there again, still acknowledged.
	local.SetInfo(func(i *kube.Info) { i.Complete = true })
	fail("kube-system", "dns", true)
	_, b := send(t, viewer, http.MethodGet, srv.URL+"/api/v1/snapshot?views=alerts", "")
	if !strings.Contains(b, `"key":"pod:kube-system/dns"`) || !strings.Contains(b, `"ack":{"by":"viewer@corp.example"`) {
		t.Fatalf("the acknowledgement of dns went: %s", b)
	}
}
