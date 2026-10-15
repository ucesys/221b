package api

import (
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/live"
)

// Anyone signed in acknowledges an alert they may see, for everyone; the
// acknowledgement lapses when the alert changes (asked at the Phase 4
// review).
func TestAcknowledgeAlerts(t *testing.T) {
	deny := map[string]bool{"pods/kube-system": true}
	srv, viewer, _, auditOut, local, store := multiSetupWith(t, "viewer@corp.example", deny)
	fail := func(ns, name, reason string) {
		local.Update("pods", ns, name, func(o runtime.Object) {
			p := o.(*corev1.Pod)
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", RestartCount: 4, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}}}}
		})
		store.Notify(kube.Change{Views: map[string][]string{live.ViewAlerts: {kube.AllRows}}})
		store.Process()
	}
	fail("web", "shop", "CrashLoopBackOff")
	fail("kube-system", "dns", "CrashLoopBackOff")
	alerts := func() string {
		store.Process()
		_, b := send(t, viewer, http.MethodGet, srv.URL+"/api/v1/snapshot?views=alerts", "")
		return b
	}
	if !strings.Contains(alerts(), `"key":"pod:web/shop"`) {
		t.Fatalf("no alert for web/shop: %s", alerts())
	}

	for name, c := range map[string]struct {
		body string
		code int
	}{
		"not json":      {`key=pod:web/shop`, 400},
		"no key":        {`{"acknowledge":true}`, 400},
		"not an alert":  {`{"key":"pod:web/nothing","acknowledge":true}`, 409},
		"bad key":       {`{"key":"nothing","acknowledge":true}`, 400},
		"not theirs":    {`{"key":"pod:kube-system/dns","acknowledge":true}`, 403},
		"unknown field": {`{"key":"pod:web/shop","acknowledge":true,"by":"someone"}`, 400},
	} {
		if r, b := send(t, viewer, http.MethodPost, srv.URL+"/api/v1/alerts/ack", c.body); r.StatusCode != c.code {
			t.Errorf("%s: %d %s", name, r.StatusCode, b)
		}
	}

	// A viewer acknowledges; everyone sees who did.
	r, b := send(t, viewer, http.MethodPost, srv.URL+"/api/v1/alerts/ack", `{"key":"pod:web/shop","acknowledge":true}`)
	if r.StatusCode != http.StatusOK || !strings.Contains(b, `"by":"viewer@corp.example"`) {
		t.Fatalf("ack %d %s", r.StatusCode, b)
	}
	if !strings.Contains(auditOut.String(), `"action":"alert.ack"`) || !strings.Contains(auditOut.String(), `"target":"pod:web/shop"`) {
		t.Fatalf("no audit record: %s", auditOut.String())
	}
	if b := alerts(); !strings.Contains(b, `"ack":{"by":"viewer@corp.example"`) {
		t.Fatalf("the row is not marked: %s", b)
	}
	// More restarts of the same failure keep it acknowledged.
	fail("web", "shop", "CrashLoopBackOff")
	if b := alerts(); !strings.Contains(b, `"ack":{"by":"viewer@corp.example"`) {
		t.Fatalf("the acknowledgement lapsed on more of the same: %s", b)
	}
	// A different failure is a new alert.
	fail("web", "shop", "ImagePullBackOff")
	if b := alerts(); strings.Contains(b, `"ack":`) {
		t.Fatalf("the acknowledgement held after the alert changed: %s", b)
	}

	// Taken back.
	send(t, viewer, http.MethodPost, srv.URL+"/api/v1/alerts/ack", `{"key":"pod:web/shop","acknowledge":true}`)
	if r, b := send(t, viewer, http.MethodPost, srv.URL+"/api/v1/alerts/ack", `{"key":"pod:web/shop","acknowledge":false}`); r.StatusCode != 200 {
		t.Fatalf("unack %d %s", r.StatusCode, b)
	}
	if b := alerts(); strings.Contains(b, `"ack":`) {
		t.Fatalf("still acknowledged: %s", b)
	}
	if !strings.Contains(auditOut.String(), `"action":"alert.unack"`) {
		t.Fatalf("no audit record of the unack: %s", auditOut.String())
	}
}
