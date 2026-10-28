package kube

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CreateNamespace makes a namespace, as the person (Phase 10, NS-01).
func (c *Cluster) CreateNamespace(ctx context.Context, p Person, name string, labels map[string]string) error {
	cs, err := c.as(p)
	if err != nil {
		return err
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
	if _, err := cs.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{FieldManager: "221b"}); err != nil {
		return fmt.Errorf("creating the namespace: %w", err)
	}
	return nil
}
