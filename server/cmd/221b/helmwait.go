package main

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// The ConfigMaps of before wait for the Helm operation that brought this
// console to end (the 0.13.0 release check: helm upgrade --wait with Helm
// 4 waits for every object the chart renders to be there, the ConfigMaps
// of before too, so a console that copied and deleted them during the
// wait failed the upgrade with "ConfigMap … not found"). Helm marks each
// revision's Secret with its status: while the newest is pending (an
// install, an upgrade or a rollback under way), the copy is done and the
// deletion waits; a release Helm does not keep (helm template, a GitOps
// tool) does not wait.

// helmPendingWait is how long the deletion waits at most, and how often
// it looks.
const (
	helmPendingWait  = 30 * time.Minute
	helmPendingEvery = 15 * time.Second
)

// helmPending says whether the newest revision of the Helm release name
// in namespace is pending; false when Helm keeps no revision of it.
func helmPending(ctx context.Context, cs kubernetes.Interface, namespace, name string) (bool, error) {
	list, err := cs.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{LabelSelector: "owner=helm,name=" + name})
	if err != nil {
		return false, err
	}
	newest, status := -1, ""
	for _, s := range list.Items {
		v, err := strconv.Atoi(s.Labels["version"])
		if err != nil || v <= newest {
			continue
		}
		newest, status = v, s.Labels["status"]
	}
	return strings.HasPrefix(status, "pending"), nil
}

// waitForHelm returns once the release named by self ("namespace/name")
// is not pending, after at most helmPendingWait, or when ctx ends; a
// release it cannot read does not hold it.
func waitForHelm(ctx context.Context, cs kubernetes.Interface, self string, log *slog.Logger) {
	ns, name, ok := strings.Cut(self, "/")
	if cs == nil || !ok || name == "" {
		return
	}
	end := time.Now().Add(helmPendingWait)
	said := false
	for {
		pending, err := helmPending(ctx, cs, ns, name)
		if err != nil {
			log.Warn("the Helm release could not be read: the ConfigMaps of before are deleted without waiting for it", "release", self, "err", err)
			return
		}
		if !pending {
			if said {
				log.Info("the Helm release is no longer pending: the ConfigMaps of before are deleted now", "release", self)
			}
			return
		}
		if !said {
			log.Info("the ConfigMaps of before are copied; they are deleted once the Helm operation under way ends (helm upgrade --wait waits for them)", "release", self)
			said = true
		}
		if time.Now().After(end) {
			log.Warn("the Helm release is still pending after 30 minutes: the ConfigMaps of before are deleted now", "release", self)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(helmPendingEvery):
		}
	}
}

// waitForHelmThenRemove deletes the ConfigMaps of before once the Helm
// operation that started this console is no longer pending.
func (w *wiring) waitForHelmThenRemove(ctx context.Context) {
	var cs kubernetes.Interface
	if w.restCfg != nil {
		if c, err := kubernetes.NewForConfig(w.restCfg); err == nil {
			cs = c
		}
	}
	waitForHelm(ctx, cs, w.cfg.SelfRelease, w.dbLog())
	w.removeCopiedSources(ctx)
}
