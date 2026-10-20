package kube

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	listersv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	"github.com/ucesys/221b/server/internal/version"
)

// Labels and key of the session Secrets.
const (
	sessionComponent = "session"
	sessionKey       = "session.json"
	// missFor is how long a key that is not there is remembered as
	// missing, so a cookie nobody issued costs the API server one read
	// every few seconds at most.
	missFor = 2 * time.Second
)

// SessionStore keeps sign-in sessions in Secrets of the console's own
// namespace, one each, named <prefix>-session-<key>, so every replica sees
// every session (P7-02). The key is a hash of the session's ID, never the
// ID itself: someone who can read Secrets cannot sign in with what they
// read. Each replica watches the session Secrets and answers from its
// cache; a key it has not seen yet, a sign-in on another replica a moment
// ago, is read from the API server. It needs get, list, watch, create,
// update and delete on Secrets in that namespace.
type SessionStore struct {
	secrets   corev1client.SecretInterface
	namespace string
	prefix    string
	labels    map[string]string
	factory   informers.SharedInformerFactory
	informer  cache.SharedIndexInformer
	lister    listersv1.SecretNamespaceLister
	now       func() time.Time

	mu     sync.Mutex
	missed map[string]time.Time
}

// NewSessionStore makes the store in the namespace with the name prefix
// (the chart's full name) and labels every Secret carries.
func NewSessionStore(cfg *rest.Config, namespace, prefix string, labels map[string]string) (*SessionStore, error) {
	c := rest.CopyConfig(cfg)
	c.ContentType, c.AcceptContentTypes = "application/json", "application/json"
	if c.UserAgent == "" {
		c.UserAgent = "221b/" + version.Version
	}
	cs, err := kubernetes.NewForConfig(c)
	if err != nil {
		return nil, fmt.Errorf("creating the client for the session Secrets: %w", err)
	}
	return NewSessionStoreFor(cs, namespace, prefix, labels), nil
}

// NewSessionStoreFor makes the store over a client, for tests.
func NewSessionStoreFor(cs kubernetes.Interface, namespace, prefix string, labels map[string]string) *SessionStore {
	all := map[string]string{"app.kubernetes.io/component": sessionComponent}
	for k, v := range labels {
		if k != "app.kubernetes.io/component" {
			all[k] = v
		}
	}
	selector := "app.kubernetes.io/component=" + sessionComponent
	factory := informers.NewSharedInformerFactoryWithOptions(cs, 0, informers.WithNamespace(namespace), informers.WithTweakListOptions(func(o *metav1.ListOptions) { o.LabelSelector = selector }))
	inf := factory.Core().V1().Secrets()
	return &SessionStore{
		secrets:   cs.CoreV1().Secrets(namespace),
		namespace: namespace,
		prefix:    prefix,
		labels:    all,
		factory:   factory,
		informer:  inf.Informer(),
		lister:    inf.Lister().Secrets(namespace),
		now:       time.Now,
		missed:    map[string]time.Time{},
	}
}

// Run watches the session Secrets until ctx is done.
func (s *SessionStore) Run(ctx context.Context) {
	s.factory.Start(ctx.Done())
	<-ctx.Done()
	s.factory.Shutdown()
}

// Synced waits until the cache holds every session, or ctx is done.
func (s *SessionStore) Synced(ctx context.Context) bool {
	return cache.WaitForCacheSync(ctx.Done(), s.informer.HasSynced)
}

func (s *SessionStore) name(key string) string { return s.prefix + "-session-" + key }

// Get returns a session record by key: from the cache, or from the API
// server when the cache has not seen it. ok is false when there is none.
func (s *SessionStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if sec, err := s.lister.Get(s.name(key)); err == nil {
		b, ok := sec.Data[sessionKey]
		return b, ok, nil
	}
	s.mu.Lock()
	until, gone := s.missed[key]
	s.mu.Unlock()
	if gone && s.now().Before(until) {
		return nil, false, nil
	}
	sec, err := s.secrets.Get(ctx, s.name(key), metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		s.mu.Lock()
		s.missed[key] = s.now().Add(missFor)
		if len(s.missed) > 10000 {
			now := s.now()
			for k, t := range s.missed {
				if now.After(t) {
					delete(s.missed, k)
				}
			}
		}
		s.mu.Unlock()
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("reading the session Secret: %w", err)
	}
	b, ok := sec.Data[sessionKey]
	return b, ok, nil
}

// Put writes a session record, creating its Secret, or replacing it when
// replace, given the record as the API server has it now, says so. It
// reports whether it wrote.
func (s *SessionStore) Put(ctx context.Context, key string, b []byte, replace func(current []byte) bool) (bool, error) {
	name := s.name(key)
	s.mu.Lock()
	delete(s.missed, key)
	s.mu.Unlock()
	for range 3 {
		cur, err := s.secrets.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: s.labels}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{sessionKey: b}}
			_, err = s.secrets.Create(ctx, sec, metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(err) {
				continue
			}
			if err != nil {
				return false, fmt.Errorf("creating the session Secret: %w", err)
			}
			return true, nil
		}
		if err != nil {
			return false, fmt.Errorf("reading the session Secret: %w", err)
		}
		if replace != nil && !replace(cur.Data[sessionKey]) {
			return false, nil
		}
		cur.Data = map[string][]byte{sessionKey: b}
		if cur.Labels == nil {
			cur.Labels = map[string]string{}
		}
		for k, v := range s.labels {
			cur.Labels[k] = v
		}
		_, err = s.secrets.Update(ctx, cur, metav1.UpdateOptions{})
		if apierrors.IsConflict(err) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("saving the session Secret: %w", err)
		}
		return true, nil
	}
	return false, fmt.Errorf("saving the session Secret: it kept changing")
}

// Delete removes a session record; one already gone is not an error.
func (s *SessionStore) Delete(ctx context.Context, key string) error {
	err := s.secrets.Delete(ctx, s.name(key), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting the session Secret: %w", err)
	}
	return nil
}

// List returns the records in the cache, by key.
func (s *SessionStore) List() map[string][]byte {
	out := map[string][]byte{}
	list, err := s.lister.List(labels.Everything())
	if err != nil {
		return out
	}
	head := s.prefix + "-session-"
	for _, sec := range list {
		key, ok := strings.CutPrefix(sec.Name, head)
		if !ok {
			continue
		}
		if b, ok := sec.Data[sessionKey]; ok {
			out[key] = b
		}
	}
	return out
}
