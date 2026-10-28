package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

// ErrNotFound says there is no record for the application.
var ErrNotFound = errors.New("no such application")

// Store keeps the records: written and read by the console alone, like
// the cluster connections (X-04).
type Store interface {
	// List returns every record, by cluster, namespace and name.
	List(ctx context.Context) ([]Record, error)
	// Get returns one record and its repository token ("" for none).
	Get(ctx context.Context, cluster, namespace, name string) (Record, string, error)
	// Save writes a record; a nil token keeps the saved one, an empty one
	// removes it.
	Save(ctx context.Context, r Record, token *string) error
	// Delete removes a record and its token.
	Delete(ctx context.Context, cluster, namespace, name string) error
	// Kind names the storage for the logs: "secrets" or "memory".
	Kind() string
}

// Labels and keys of the record Secrets.
const (
	LabelComponent = "app.kubernetes.io/component"
	ComponentValue = "helm-app"
	LabelCluster   = "221b.io/cluster"
	keySpec        = "spec.json"
	keyToken       = "token"
)

// SecretStore keeps each record in one Secret of the console's namespace,
// named <prefix>-app-<id> and labelled so they can be listed.
type SecretStore struct {
	secrets   corev1client.SecretInterface
	namespace string
	prefix    string
}

// NewSecretStore makes a store in the namespace with the name prefix.
func NewSecretStore(cfg *rest.Config, namespace, prefix string) (*SecretStore, error) {
	c := rest.CopyConfig(cfg)
	c.ContentType, c.AcceptContentTypes = "application/json", "application/json"
	cs, err := kubernetes.NewForConfig(c)
	if err != nil {
		return nil, fmt.Errorf("creating the client for the application Secrets: %w", err)
	}
	return &SecretStore{secrets: cs.CoreV1().Secrets(namespace), namespace: namespace, prefix: prefix}, nil
}

func (s *SecretStore) Kind() string { return "secrets" }

func (s *SecretStore) name(cluster, namespace, name string) string {
	return s.prefix + "-app-" + ID(cluster, namespace, name)
}

func (s *SecretStore) List(ctx context.Context) ([]Record, error) {
	list, err := s.secrets.List(ctx, metav1.ListOptions{LabelSelector: LabelComponent + "=" + ComponentValue})
	if err != nil {
		return nil, fmt.Errorf("listing the application Secrets in %s: %w", s.namespace, err)
	}
	var out []Record
	for i := range list.Items {
		r, err := unmarshal(list.Items[i].Data[keySpec])
		if err != nil {
			slog.Warn("application Secret skipped: it cannot be read", "secret", list.Items[i].Name, "err", err)
			continue
		}
		r.HasToken = len(list.Items[i].Data[keyToken]) > 0
		out = append(out, r)
	}
	sortRecords(out)
	return out, nil
}

func (s *SecretStore) Get(ctx context.Context, cluster, namespace, name string) (Record, string, error) {
	sec, err := s.secrets.Get(ctx, s.name(cluster, namespace, name), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return Record{}, "", ErrNotFound
	}
	if err != nil {
		return Record{}, "", fmt.Errorf("reading the application Secret: %w", err)
	}
	r, err := unmarshal(sec.Data[keySpec])
	if err != nil {
		return Record{}, "", err
	}
	r.HasToken = len(sec.Data[keyToken]) > 0
	return r, string(sec.Data[keyToken]), nil
}

func (s *SecretStore) Save(ctx context.Context, r Record, token *string) error {
	r.Version = RecordVersion
	name := s.name(r.Cluster, r.Namespace, r.Name)
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	cur, err := s.secrets.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		sec := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.namespace, Labels: map[string]string{LabelComponent: ComponentValue, LabelCluster: r.Cluster}},
			Data:       map[string][]byte{keySpec: data},
		}
		if token != nil && *token != "" {
			sec.Data[keyToken] = []byte(*token)
		}
		if _, err := s.secrets.Create(ctx, sec, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("saving the application: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading the application Secret: %w", err)
	}
	if cur.Data == nil {
		cur.Data = map[string][]byte{}
	}
	cur.Data[keySpec] = data
	if token != nil {
		if *token == "" {
			delete(cur.Data, keyToken)
		} else {
			cur.Data[keyToken] = []byte(*token)
		}
	}
	if _, err := s.secrets.Update(ctx, cur, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("saving the application: %w", err)
	}
	return nil
}

func (s *SecretStore) Delete(ctx context.Context, cluster, namespace, name string) error {
	err := s.secrets.Delete(ctx, s.name(cluster, namespace, name), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("removing the application's record: %w", err)
	}
	return nil
}

func unmarshal(data []byte) (Record, error) {
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return r, fmt.Errorf("the record is not valid: %w", err)
	}
	if r.Version > RecordVersion {
		return r, fmt.Errorf("the record was saved by a newer console (format %d)", r.Version)
	}
	return r, nil
}

func sortRecords(rs []Record) {
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].Cluster != rs[j].Cluster {
			return rs[i].Cluster < rs[j].Cluster
		}
		return rs[i].Key() < rs[j].Key()
	})
}

// MemoryStore keeps records until the server restarts: for running
// outside a cluster and for tests.
type MemoryStore struct {
	mu     sync.Mutex
	recs   map[string]Record
	tokens map[string]string
}

// NewMemoryStore makes an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{recs: map[string]Record{}, tokens: map[string]string{}}
}

func (m *MemoryStore) Kind() string { return "memory" }

func (m *MemoryStore) List(context.Context) ([]Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Record, 0, len(m.recs))
	for id, r := range m.recs {
		r.HasToken = m.tokens[id] != ""
		out = append(out, r)
	}
	sortRecords(out)
	return out, nil
}

func (m *MemoryStore) Get(_ context.Context, cluster, namespace, name string) (Record, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := ID(cluster, namespace, name)
	r, ok := m.recs[id]
	if !ok {
		return Record{}, "", ErrNotFound
	}
	r.HasToken = m.tokens[id] != ""
	return r, m.tokens[id], nil
}

func (m *MemoryStore) Save(_ context.Context, r Record, token *string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r.Version = RecordVersion
	id := ID(r.Cluster, r.Namespace, r.Name)
	m.recs[id] = r
	if token != nil {
		if *token == "" {
			delete(m.tokens, id)
		} else {
			m.tokens[id] = *token
		}
	}
	return nil
}

func (m *MemoryStore) Delete(_ context.Context, cluster, namespace, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := ID(cluster, namespace, name)
	delete(m.recs, id)
	delete(m.tokens, id)
	return nil
}
