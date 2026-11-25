package kube_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/sim"
)

// status writes a Kubernetes Status answer.
func status(w http.ResponseWriter, code int, reason, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"`+reason+`","code":`+strconv.Itoa(code)+`,"message":"`+message+`"}`)
}

// A refusal keeps the API server's reason (kube#15): the YAML view says
// what RBAC said, and the settings ConfigMap's errors say what went wrong,
// a 1 MiB refusal included.
func TestARefusalKeepsTheAPIServersReason(t *testing.T) {
	s := sim.New(sim.Options{Nodes: 2, Pods: 4})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/namespaces/shop/pods/web":
			status(w, 403, "Forbidden", `pods \"web\" is forbidden: User \"alice\" cannot get resource \"pods\" in the namespace \"shop\"`)
			return
		case r.URL.Path == "/api/v1/namespaces/console/configmaps/broken":
			status(w, 500, "InternalError", `etcdserver: request timed out`)
			return
		case r.URL.Path == "/api/v1/namespaces/console/configmaps/full" && r.Method == http.MethodPatch:
			status(w, 422, "Invalid", `ConfigMap \"full\" is invalid: []: Too long: must have at most 1048576 bytes`)
			return
		}
		s.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	cfg := &rest.Config{Host: srv.URL}
	cl, err := kube.New(cfg, kube.Options{Name: "sim", Mode: kube.ModeURL, HealthInterval: time.Hour, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	pods, _ := kube.KindOf("Pod")
	_, err = cl.ObjectYAML(context.Background(), kube.Person{}, pods, "shop", "web")
	if err == nil || !strings.Contains(err.Error(), `User "alice" cannot get resource "pods"`) {
		t.Errorf("the YAML read's error: %v", err)
	}

	broken, err := kube.NewConfigMapStorage(cfg, "console/broken")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := broken.LoadKey(context.Background(), "settings.json"); err == nil || !strings.Contains(err.Error(), "etcdserver: request timed out") {
		t.Errorf("the ConfigMap read's error: %v", err)
	}
	full, _ := kube.NewConfigMapStorage(cfg, "console/full")
	if _, err := full.SaveKey(context.Background(), "settings.json", []byte("{}"), ""); !errors.Is(err, kv.ErrFull) {
		t.Errorf("a save past 1 MiB: %v", err)
	}
}
