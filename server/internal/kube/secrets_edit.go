package kube

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Secret editing (Phase 10, SE-01 to SE-06): the only reads of a Secret's
// values the console makes, each as the person, live and on request;
// nothing of them enters the cache, a log or an export. The server hands
// a value to a browser only where the chart turned revealing on.

// GetSecret reads a Secret with its values, as the person.
func (c *Cluster) GetSecret(ctx context.Context, p Person, namespace, name string) (*corev1.Secret, error) {
	cs, err := c.as(p)
	if err != nil {
		return nil, err
	}
	sec, err := cs.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("reading the Secret: %w", err)
	}
	return sec, nil
}

// UpdateSecret writes a Secret back, as the person; the resourceVersion
// it carries must be the one read, so an edit never overwrites another.
func (c *Cluster) UpdateSecret(ctx context.Context, p Person, sec *corev1.Secret) (*corev1.Secret, error) {
	cs, err := c.as(p)
	if err != nil {
		return nil, err
	}
	out, err := cs.CoreV1().Secrets(sec.Namespace).Update(ctx, sec, metav1.UpdateOptions{FieldManager: "221b"})
	if err != nil {
		return nil, fmt.Errorf("writing the Secret: %w", err)
	}
	return out, nil
}

// CreateSecret makes a Secret, as the person.
func (c *Cluster) CreateSecret(ctx context.Context, p Person, sec *corev1.Secret) (*corev1.Secret, error) {
	cs, err := c.as(p)
	if err != nil {
		return nil, err
	}
	out, err := cs.CoreV1().Secrets(sec.Namespace).Create(ctx, sec, metav1.CreateOptions{FieldManager: "221b"})
	if err != nil {
		return nil, fmt.Errorf("creating the Secret: %w", err)
	}
	return out, nil
}
