package kube

import (
	"context"
	"errors"
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
//
// With a database the sessions are rows of auth_sessions (DB-66) and this
// store is not run: it is then only what reads the Secrets for the
// one-time copy (All) and empties them after it (Empty).
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

// awayError marks an error of the API server that means it could not be
// asked, which is what makes a sign-in's session a signed one
// (auth.StoreAway, DB-65).
type awayError struct{ error }

func (e awayError) Unwrap() error { return e.error }

// StoreAway says the store could not be asked.
func (awayError) StoreAway() bool { return true }

// away marks err when the API server did not answer, or answered that it
// cannot serve just now, and leaves a refusal as it is. Not answering is
// every error that is not an answer of the API server's: the connection
// refused, reset or timed out, a name that does not resolve, a certificate
// that does not verify, the time given to the call run out. "Not now" is
// 429 (too many requests) and every 5xx: an internal error (which is how
// etcd being away reads), the server unavailable, a timeout. Everything
// else the API server says is a refusal: forbidden, unauthorized, a
// conflict, already exists, not found, invalid, too large.
func away(err error) error {
	if err == nil {
		return nil
	}
	var status apierrors.APIStatus
	if !errors.As(err, &status) {
		if errors.Is(err, context.Canceled) {
			return err
		}
		return awayError{err}
	}
	if code := status.Status().Code; code == 429 || code >= 500 {
		return awayError{err}
	}
	return err
}

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
		return nil, false, away(fmt.Errorf("reading the session Secret: %w", err))
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
			if replace != nil && !replace(nil) {
				return false, nil
			}
			sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: s.labels}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{sessionKey: b}}
			_, err = s.secrets.Create(ctx, sec, metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(err) {
				continue
			}
			if err != nil {
				return false, away(fmt.Errorf("creating the session Secret: %w", err))
			}
			return true, nil
		}
		if err != nil {
			return false, away(fmt.Errorf("reading the session Secret: %w", err))
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
			return false, away(fmt.Errorf("saving the session Secret: %w", err))
		}
		return true, nil
	}
	return false, fmt.Errorf("saving the session Secret: it kept changing")
}

// Delete removes a session record; one already gone is not an error.
func (s *SessionStore) Delete(ctx context.Context, key string) error {
	err := s.secrets.Delete(ctx, s.name(key), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return away(fmt.Errorf("deleting the session Secret: %w", err))
	}
	return nil
}

// List returns the records in the cache, by key. A cache that has not
// been filled yet holds nothing, which is not the same as no sessions: it
// is an error, so nothing is taken to have ended.
func (s *SessionStore) List(context.Context) (map[string][]byte, error) {
	if !s.informer.HasSynced() {
		return nil, errors.New("the session Secrets have not been listed yet")
	}
	out := map[string][]byte{}
	list, err := s.lister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("listing the session Secrets: %w", err)
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
	return out, nil
}

// All reads every session Secret from the API server, not from the cache,
// which need not be running, and returns the records by key: what the
// one-time copy into the database reads (DB-66). A Secret that holds no
// record, one emptied after the copy, is not among them.
func (s *SessionStore) All(ctx context.Context) (map[string][]byte, error) {
	out := map[string][]byte{}
	head := s.prefix + "-session-"
	opts := metav1.ListOptions{LabelSelector: "app.kubernetes.io/component=" + sessionComponent, Limit: 500}
	for {
		list, err := s.secrets.List(ctx, opts)
		if err != nil {
			return nil, away(fmt.Errorf("listing the session Secrets: %w", err))
		}
		for _, sec := range list.Items {
			key, ok := strings.CutPrefix(sec.Name, head)
			if !ok {
				continue
			}
			if b, ok := sec.Data[sessionKey]; ok {
				out[key] = b
			}
		}
		if list.Continue == "" {
			return out, nil
		}
		opts.Continue = list.Continue
	}
}

// Empty takes the record out of a session's Secret and leaves the Secret,
// with no keys: the session is a row of the database from then on, and
// the console deletes no Secret because a record set moved (DB-61). An
// emptied Secret is no session to any console: one from before the move
// reads it as a session that ended. It reports whether it emptied one; a
// Secret already empty, or gone, is not an error.
func (s *SessionStore) Empty(ctx context.Context, key string) (bool, error) {
	name := s.name(key)
	for range 3 {
		cur, err := s.secrets.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, away(fmt.Errorf("reading the session Secret: %w", err))
		}
		if _, ok := cur.Data[sessionKey]; !ok {
			return false, nil
		}
		// Over the version read: a console from before the move may be
		// writing the session's last activity.
		cur.Data = map[string][]byte{}
		_, err = s.secrets.Update(ctx, cur, metav1.UpdateOptions{})
		if apierrors.IsConflict(err) {
			continue
		}
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, away(fmt.Errorf("emptying the session Secret: %w", err))
		}
		return true, nil
	}
	return false, fmt.Errorf("emptying the session Secret: it kept changing")
}
