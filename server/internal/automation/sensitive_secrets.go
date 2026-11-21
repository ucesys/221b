package automation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

// The sensitive values in Kubernetes Secrets (DB-71): one Secret a
// cluster, <prefix>-automation-<id>, in the console's namespace, beside
// the applications' and the connections'. Its key "key" is what the
// digests of that cluster are made with; every other key is "v." and a
// digest's 24 hex digits, and holds the value that digest stands for. A
// key a value is one write to add or take away, and a Secret that cannot
// be read as a whole does not exist: each value reads alone.
//
// The Secret is written over the version read, and read again when it
// changed meanwhile. It is never deleted: one whose cluster is gone, or
// whose rows a restored database no longer has, stays where it is.

const (
	// SensitiveComponent is the label value that lists these Secrets.
	SensitiveComponent = "automation-values"
	labelComponent     = "app.kubernetes.io/component"
	labelCluster       = "221b.io/cluster"
	keyDigestKey       = "key"
	valuePrefix        = "v."
)

// SecretSensitive keeps the sensitive values in Secrets.
type SecretSensitive struct {
	secrets   corev1client.SecretInterface
	namespace string
	prefix    string
	labels    map[string]string
}

// NewSecretSensitive keeps the sensitive values in the Secrets of a
// namespace, named <prefix>-automation-<id>; labels are put on each.
func NewSecretSensitive(cfg *rest.Config, namespace, prefix string, labels map[string]string) (*SecretSensitive, error) {
	c := rest.CopyConfig(cfg)
	c.ContentType, c.AcceptContentTypes = "application/json", "application/json"
	cs, err := kubernetes.NewForConfig(c)
	if err != nil {
		return nil, fmt.Errorf("creating the client for Automation's Secrets: %w", err)
	}
	return NewSecretSensitiveOn(cs.CoreV1().Secrets(namespace), namespace, prefix, labels), nil
}

// NewSecretSensitiveOn is NewSecretSensitive over a client's Secrets.
func NewSecretSensitiveOn(secrets corev1client.SecretInterface, namespace, prefix string, labels map[string]string) *SecretSensitive {
	return &SecretSensitive{secrets: secrets, namespace: namespace, prefix: prefix, labels: labels}
}

// SecretName is the name of the Secret that keeps a cluster's values.
func (s *SecretSensitive) SecretName(cluster string) string {
	sum := sha256.Sum256([]byte(cluster))
	return s.prefix + "-automation-" + hex.EncodeToString(sum[:10])
}

func valueKey(digest string) string { return valuePrefix + strings.TrimPrefix(digest, hiddenPrefix) }

func (s *SecretSensitive) labelsOf(cluster string) map[string]string {
	out := map[string]string{}
	maps.Copy(out, s.labels)
	out[labelComponent] = SensitiveComponent
	if cluster != "" && len(validation.IsValidLabelValue(cluster)) == 0 {
		out[labelCluster] = cluster
	}
	return out
}

// edit changes a cluster's Secret: f is given its data, a copy, and says
// whether it changed it. A Secret that is not there is made when create
// is set, with a key of its own.
func (s *SecretSensitive) edit(ctx context.Context, cluster string, create bool, f func(data map[string][]byte) (bool, error)) error {
	name := s.SecretName(cluster)
	for range 6 {
		cur, err := s.secrets.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			if !create {
				return nil
			}
			data := map[string][]byte{keyDigestKey: newDigestKey()}
			if _, err := f(data); err != nil {
				return err
			}
			sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.namespace, Labels: s.labelsOf(cluster)}, Data: data}
			if _, err = s.secrets.Create(ctx, sec, metav1.CreateOptions{}); apierrors.IsAlreadyExists(err) {
				continue
			}
			return s.failed("making", err)
		}
		if err != nil {
			return s.failed("reading", err)
		}
		next := cur.DeepCopy()
		if next.Data == nil {
			next.Data = map[string][]byte{}
		}
		changed := false
		if len(next.Data[keyDigestKey]) == 0 {
			// A Secret someone emptied: the digests of before stand for
			// nothing any more, and the values kept are read by them still.
			next.Data[keyDigestKey], changed = newDigestKey(), true
		}
		did, err := f(next.Data)
		if err != nil {
			return err
		}
		if !did && !changed {
			return nil
		}
		if _, err = s.secrets.Update(ctx, next, metav1.UpdateOptions{}); apierrors.IsConflict(err) {
			continue
		}
		return s.failed("writing", err)
	}
	return errors.New("the Secret that keeps the sensitive values kept changing while it was written")
}

// failed is an error of the API server, without what was sent: the name
// of the Secret is not a secret, its contents never reach here.
func (s *SecretSensitive) failed(doing string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s the Secret of sensitive values in %s: %w", doing, s.namespace, err)
}

func (s *SecretSensitive) Key(ctx context.Context, cluster string) ([]byte, error) {
	var key []byte
	err := s.edit(ctx, cluster, true, func(data map[string][]byte) (bool, error) {
		key = append([]byte(nil), data[keyDigestKey]...)
		return false, nil
	})
	if err == nil && len(key) == 0 {
		err = errors.New("the Secret of sensitive values has no key")
	}
	return key, err
}

func (s *SecretSensitive) Put(ctx context.Context, cluster string, values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	return s.edit(ctx, cluster, true, func(data map[string][]byte) (bool, error) {
		changed := false
		for d, v := range values {
			k := valueKey(d)
			if _, kept := data[k]; !kept {
				data[k], changed = []byte(v), true
			}
		}
		size := 0
		for k, v := range data {
			size += len(k) + len(v)
		}
		if changed && size > MaxSensitiveBytes {
			return false, fmt.Errorf("%w: this cluster's sensitive values would take %d KiB together, over the %d KiB their Secret holds: remove some first", ErrFull, size>>10, MaxSensitiveBytes>>10)
		}
		return changed, nil
	})
}

func (s *SecretSensitive) Get(ctx context.Context, cluster string, digests []string) (map[string]string, error) {
	out := map[string]string{}
	if len(digests) == 0 {
		return out, nil
	}
	sec, err := s.secrets.Get(ctx, s.SecretName(cluster), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return out, nil
	}
	if err != nil {
		return nil, s.failed("reading", err)
	}
	for _, d := range digests {
		if v, ok := sec.Data[valueKey(d)]; ok {
			out[d] = string(v)
		}
	}
	return out, nil
}

func (s *SecretSensitive) Drop(ctx context.Context, cluster string, digests []string) error {
	if len(digests) == 0 {
		return nil
	}
	return s.edit(ctx, cluster, false, func(data map[string][]byte) (bool, error) {
		changed := false
		for _, d := range digests {
			if _, kept := data[valueKey(d)]; kept {
				delete(data, valueKey(d))
				changed = true
			}
		}
		return changed, nil
	})
}

func (s *SecretSensitive) Held(ctx context.Context) (map[string][]string, error) {
	list, err := s.secrets.List(ctx, metav1.ListOptions{LabelSelector: labelComponent + "=" + SensitiveComponent})
	if err != nil {
		return nil, s.failed("listing", err)
	}
	out := map[string][]string{}
	for i := range list.Items {
		sec := &list.Items[i]
		// Another release in the namespace labels its own Secrets the
		// same way: the name says whose (the fourteenth cluster test's N1).
		if !strings.HasPrefix(sec.Name, s.prefix+"-automation-") {
			continue
		}
		// A Secret whose cluster's id is no label is named by its own name.
		cluster, ok := sec.Labels[labelCluster]
		if !ok {
			cluster = "secret " + sec.Name
		}
		for k := range sec.Data {
			if d, isValue := strings.CutPrefix(k, valuePrefix); isValue {
				out[cluster] = append(out[cluster], hiddenPrefix+d)
			}
		}
		sort.Strings(out[cluster])
	}
	return out, nil
}
