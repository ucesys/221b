package kube

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/sherlock"
	"github.com/ucesys/221b/server/internal/version"
)

// SecretKey keeps the Claude API key in a Secret of the console's own
// namespace (X-04): written when an administrator saves it, read at start,
// never sent back to a browser.
type SecretKey struct {
	secrets corev1client.SecretInterface
	ref     string
	name    string
	labels  map[string]string
}

// NewSecretKey returns the store for the Secret namespace/name.
func NewSecretKey(cfg *rest.Config, namespace, name string, labels map[string]string) (*SecretKey, error) {
	c := rest.CopyConfig(cfg)
	c.ContentType, c.AcceptContentTypes = "application/json", "application/json"
	if c.UserAgent == "" {
		c.UserAgent = "221b/" + version.Version
	}
	cs, err := kubernetes.NewForConfig(c)
	if err != nil {
		return nil, fmt.Errorf("creating the client for the Secret: %w", err)
	}
	return &SecretKey{secrets: cs.CoreV1().Secrets(namespace), ref: namespace + "/" + name, name: name, labels: labels}, nil
}

func (s *SecretKey) Kind() string { return "secret" }

func (s *SecretKey) Load(ctx context.Context) (map[string][]byte, error) {
	sec, err := s.secrets.Get(ctx, s.name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, sherlock.ErrNoKey
	}
	if err != nil {
		return nil, fmt.Errorf("reading the Secret %s: %w", s.ref, err)
	}
	if len(sec.Data) == 0 {
		return nil, sherlock.ErrNoKey
	}
	return sec.Data, nil
}

func (s *SecretKey) Save(ctx context.Context, data map[string][]byte) error {
	existing, err := s.secrets.Get(ctx, s.name, metav1.GetOptions{})
	switch {
	case err == nil:
		existing.Data = data
		if existing.Labels == nil {
			existing.Labels = map[string]string{}
		}
		for k, v := range s.labels {
			existing.Labels[k] = v
		}
		if _, err := s.secrets.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("saving the Secret %s: %w", s.ref, err)
		}
		return nil
	case apierrors.IsNotFound(err):
		sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: s.name, Labels: s.labels}, Type: corev1.SecretTypeOpaque, Data: data}
		if _, err := s.secrets.Create(ctx, sec, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("creating the Secret %s: %w", s.ref, err)
		}
		return nil
	default:
		return fmt.Errorf("reading the Secret %s: %w", s.ref, err)
	}
}

func (s *SecretKey) Delete(ctx context.Context) error {
	err := s.secrets.Delete(ctx, s.name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting the Secret %s: %w", s.ref, err)
	}
	return nil
}
