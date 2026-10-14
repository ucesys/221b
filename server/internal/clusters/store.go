package clusters

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/version"
)

// ErrNotFound says there is no such connection.
var ErrNotFound = errors.New("no such cluster connection")

// Store keeps connections: specs beside their credentials, which are
// written and read by the server only (X-04, P3-07).
type Store interface {
	// List returns every connection's spec, by name.
	List(ctx context.Context) ([]Spec, error)
	// Get returns one connection with its credential.
	Get(ctx context.Context, id string) (Spec, Credential, error)
	// Save writes a connection; a nil credential keeps the saved one.
	Save(ctx context.Context, s Spec, c *Credential) error
	// Delete removes a connection and its credential (P3-08).
	Delete(ctx context.Context, id string) error
	// Kind names the storage for the logs: "secrets" or "memory".
	Kind() string
}

// MemoryStore keeps connections until the server restarts: for running
// outside a cluster and for tests.
type MemoryStore struct {
	mu    sync.Mutex
	specs map[string]Spec
	creds map[string]Credential
}

// NewMemoryStore makes an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{specs: map[string]Spec{}, creds: map[string]Credential{}}
}

func (m *MemoryStore) Kind() string { return "memory" }

func (m *MemoryStore) List(context.Context) ([]Spec, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Spec, 0, len(m.specs))
	for _, s := range m.specs {
		out = append(out, s)
	}
	sortSpecs(out)
	return out, nil
}

func (m *MemoryStore) Get(_ context.Context, id string) (Spec, Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.specs[id]
	if !ok {
		return Spec{}, Credential{}, ErrNotFound
	}
	return s, m.creds[id], nil
}

func (m *MemoryStore) Save(_ context.Context, s Spec, c *Credential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.specs[s.ID] = s
	if c != nil {
		m.creds[s.ID] = *c
	}
	return nil
}

func (m *MemoryStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.specs[id]; !ok {
		return ErrNotFound
	}
	delete(m.specs, id)
	delete(m.creds, id)
	return nil
}

func sortSpecs(specs []Spec) {
	sort.Slice(specs, func(i, j int) bool {
		if specs[i].Local != specs[j].Local {
			return specs[i].Local
		}
		return specs[i].Name < specs[j].Name
	})
}

// Labels and keys of the connection Secrets.
const (
	LabelComponent = "app.kubernetes.io/component"
	ComponentValue = "cluster-connection"
	LabelCluster   = "221b.ucesys.com/cluster"
	keySpec        = "spec.json"
	keyToken       = "token"
	keyKubeconfig  = "kubeconfig"
	keyCert        = "tls.crt"
	keyKey         = "tls.key"
	keyCA          = "ca.crt"
	keyOIDCIssuer  = "oidc-issuer"
	keyOIDCClient  = "oidc-client-id"
	keyOIDCSecret  = "oidc-client-secret"
	keyOIDCRefresh = "oidc-refresh-token"
	keyOIDCScopes  = "oidc-scopes"
)

// SecretStore keeps each connection in one Secret of the console's
// namespace, named <prefix>-cluster-<id> and labelled so they can be
// listed. It needs get, list, create, update and delete on Secrets in that
// namespace.
type SecretStore struct {
	secrets   corev1client.SecretInterface
	namespace string
	prefix    string
	labels    map[string]string
}

// NewSecretStore makes a store in the namespace with the name prefix (the
// chart's full name) and labels every Secret carries.
func NewSecretStore(cfg *rest.Config, namespace, prefix string, labels map[string]string) (*SecretStore, error) {
	c := rest.CopyConfig(cfg)
	c.ContentType, c.AcceptContentTypes = "application/json", "application/json"
	if c.UserAgent == "" {
		c.UserAgent = "221b/" + version.Version
	}
	cs, err := kubernetes.NewForConfig(c)
	if err != nil {
		return nil, fmt.Errorf("creating the client for the connection Secrets: %w", err)
	}
	all := map[string]string{LabelComponent: ComponentValue}
	for k, v := range labels {
		all[k] = v
	}
	return &SecretStore{secrets: cs.CoreV1().Secrets(namespace), namespace: namespace, prefix: prefix, labels: all}, nil
}

func (s *SecretStore) Kind() string { return "secrets" }

func (s *SecretStore) name(id string) string { return s.prefix + "-cluster-" + id }

func (s *SecretStore) List(ctx context.Context) ([]Spec, error) {
	list, err := s.secrets.List(ctx, metav1.ListOptions{LabelSelector: LabelComponent + "=" + ComponentValue})
	if err != nil {
		return nil, fmt.Errorf("listing the connection Secrets in %s: %w", s.namespace, err)
	}
	var out []Spec
	for i := range list.Items {
		spec, err := UnmarshalSpec(list.Items[i].Data[keySpec])
		if err != nil {
			return nil, fmt.Errorf("reading the connection Secret %s: %w", list.Items[i].Name, err)
		}
		out = append(out, spec)
	}
	sortSpecs(out)
	return out, nil
}

func (s *SecretStore) Get(ctx context.Context, id string) (Spec, Credential, error) {
	sec, err := s.secrets.Get(ctx, s.name(id), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return Spec{}, Credential{}, ErrNotFound
		}
		return Spec{}, Credential{}, fmt.Errorf("reading the connection Secret %s: %w", s.name(id), err)
	}
	spec, err := UnmarshalSpec(sec.Data[keySpec])
	if err != nil {
		return Spec{}, Credential{}, fmt.Errorf("reading the connection Secret %s: %w", sec.Name, err)
	}
	return spec, credentialOf(sec.Data), nil
}

func credentialOf(data map[string][]byte) Credential {
	get := func(k string) string { return string(data[k]) }
	return Credential{Token: get(keyToken), Kubeconfig: get(keyKubeconfig), Context: get("context"), CertPEM: get(keyCert), KeyPEM: get(keyKey), CAPEM: get(keyCA), OIDCIssuer: get(keyOIDCIssuer), OIDCClientID: get(keyOIDCClient), OIDCClientSecret: get(keyOIDCSecret), OIDCRefreshToken: get(keyOIDCRefresh), OIDCScopes: get(keyOIDCScopes)}
}

func dataOf(c Credential) map[string][]byte {
	data := map[string][]byte{}
	put := func(k, v string) {
		if v != "" {
			data[k] = []byte(v)
		}
	}
	put(keyToken, c.Token)
	put(keyKubeconfig, c.Kubeconfig)
	put("context", c.Context)
	put(keyCert, c.CertPEM)
	put(keyKey, c.KeyPEM)
	put(keyCA, c.CAPEM)
	put(keyOIDCIssuer, c.OIDCIssuer)
	put(keyOIDCClient, c.OIDCClientID)
	put(keyOIDCSecret, c.OIDCClientSecret)
	put(keyOIDCRefresh, c.OIDCRefreshToken)
	put(keyOIDCScopes, c.OIDCScopes)
	return data
}

func (s *SecretStore) Save(ctx context.Context, spec Spec, c *Credential) error {
	raw, err := MarshalSpec(spec)
	if err != nil {
		return err
	}
	name := s.name(spec.ID)
	existing, err := s.secrets.Get(ctx, name, metav1.GetOptions{})
	switch {
	case err == nil:
		data := existing.Data
		if c != nil {
			data = dataOf(*c)
		}
		if data == nil {
			data = map[string][]byte{}
		}
		data[keySpec] = raw
		existing.Data = data
		if existing.Labels == nil {
			existing.Labels = map[string]string{}
		}
		for k, v := range s.labels {
			existing.Labels[k] = v
		}
		existing.Labels[LabelCluster] = spec.ID
		if _, err := s.secrets.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("updating the connection Secret %s: %w", name, err)
		}
		return nil
	case apierrors.IsNotFound(err):
		var data map[string][]byte
		if c != nil {
			data = dataOf(*c)
		} else {
			data = map[string][]byte{}
		}
		data[keySpec] = raw
		labels := map[string]string{LabelCluster: spec.ID}
		for k, v := range s.labels {
			labels[k] = v
		}
		sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.namespace, Labels: labels}, Type: corev1.SecretTypeOpaque, Data: data}
		if _, err := s.secrets.Create(ctx, sec, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("creating the connection Secret %s: %w", name, err)
		}
		return nil
	default:
		return fmt.Errorf("reading the connection Secret %s: %w", name, err)
	}
}

func (s *SecretStore) Delete(ctx context.Context, id string) error {
	err := s.secrets.Delete(ctx, s.name(id), metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("deleting the connection Secret %s: %w", s.name(id), err)
	}
	return nil
}
