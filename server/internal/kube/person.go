package kube

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	authzv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Person is who a read is made for (P3-02): the signed-in person's user
// name and groups, which the API server applies its RBAC to, by
// impersonation for live reads and by SubjectAccessReview for the cached
// rows. The zero Person is the console itself.
type Person struct {
	User   string
	Groups []string
}

// Nobody reports whether the person is the console itself.
func (p Person) Nobody() bool { return p.User == "" }

// Key identifies the person for caches: the user and the sorted groups.
func (p Person) Key() string {
	if p.User == "" {
		return ""
	}
	groups := append([]string(nil), p.Groups...)
	sort.Strings(groups)
	return p.User + "\x00" + strings.Join(groups, "\x00")
}

// groups are the person's groups as Kubernetes sees a signed-in user:
// with system:authenticated, which every real user carries.
func (p Person) groups() []string {
	out := append([]string(nil), p.Groups...)
	for _, g := range out {
		if g == "system:authenticated" {
			return out
		}
	}
	return append(out, "system:authenticated")
}

// personClients keeps one client per person who read something live, so
// the transport is built once for them. The TLS transport underneath is
// shared across them by client-go; only the impersonation differs.
type personClients struct {
	mu      sync.Mutex
	clients map[string]*kubernetes.Clientset
}

// maxPersonClients bounds the cache; past it, it starts over.
const maxPersonClients = 256

// as returns a client that reads as the person, or the console's own for
// nobody.
func (c *Cluster) as(p Person) (*kubernetes.Clientset, error) {
	if p.Nobody() {
		return c.cs, nil
	}
	key := p.Key()
	c.people.mu.Lock()
	defer c.people.mu.Unlock()
	if cs, ok := c.people.clients[key]; ok {
		return cs, nil
	}
	cfg := rest.CopyConfig(c.clientCfg)
	cfg.Impersonate = rest.ImpersonationConfig{UserName: p.User, Groups: p.groups()}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("preparing a client for %s: %w", p.User, err)
	}
	if c.people.clients == nil || len(c.people.clients) >= maxPersonClients {
		c.people.clients = map[string]*kubernetes.Clientset{}
	}
	c.people.clients[key] = cs
	return cs, nil
}

// CanI asks the API server whether the person may do something, with a
// SubjectAccessReview made by the console. The console needs create on
// subjectaccessreviews, which the chart grants.
func (c *Cluster) CanI(ctx context.Context, p Person, verb, group, resource, namespace string) (bool, error) {
	if p.Nobody() {
		return true, nil
	}
	sar := &authzv1.SubjectAccessReview{Spec: authzv1.SubjectAccessReviewSpec{
		User:               p.User,
		Groups:             p.groups(),
		ResourceAttributes: &authzv1.ResourceAttributes{Verb: verb, Group: group, Resource: resource, Namespace: namespace},
	}}
	out, err := c.cs.AuthorizationV1().SubjectAccessReviews().Create(ctx, sar, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	return out.Status.Allowed, nil
}
