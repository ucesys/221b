package clusters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
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

// ErrConflict is an edit saved over a version of the connection that is
// no longer the saved one (DB-92): someone else saved it since it was
// read.
var ErrConflict = errors.New("the connection was changed by someone else since it was read")

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
	// Revisions returns each saved connection's revision, which changes
	// with every save, so a replica notices what another one changed
	// (P7-02). The local cluster's saved name is among them.
	Revisions(ctx context.Context) (map[string]string, error)
	// Kind names the storage for the logs: "secrets", "database" (the
	// credentials then stay in the Secrets, DB-64) or "memory".
	Kind() string
}

// TestKeeper is a store that keeps each connection's last test, so that a
// restart does not lose it (DB-64); its List and Get give it as
// Spec.Tested. A test is no save of the connection: it never changes the
// spec, but it changes the revision, so the other replicas show it too.
type TestKeeper interface {
	// SaveTest keeps t as the connection's last test; ErrNotFound for a
	// connection the store does not keep (the local cluster before its
	// name was ever saved).
	SaveTest(ctx context.Context, id string, t LastTest) error
}

// MemoryStore keeps connections until the server restarts: for running
// outside a cluster and for tests.
type MemoryStore struct {
	mu    sync.Mutex
	specs map[string]Spec
	creds map[string]Credential
	tests map[string]LastTest
	revs  map[string]int
	saves int
	// vers is each connection's version, as a row's (DB-92).
	vers map[string]int
}

// NewMemoryStore makes an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{specs: map[string]Spec{}, creds: map[string]Credential{}, tests: map[string]LastTest{}, revs: map[string]int{}, vers: map[string]int{}}
}

func (m *MemoryStore) Revisions(context.Context) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]string, len(m.revs))
	for id, r := range m.revs {
		out[id] = strconv.Itoa(r)
	}
	return out, nil
}

func (m *MemoryStore) Kind() string { return "memory" }

func (m *MemoryStore) List(context.Context) ([]Spec, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Spec, 0, len(m.specs))
	for id, s := range m.specs {
		out = append(out, m.withTest(id, s))
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
	return m.withTest(id, s), m.creds[id], nil
}

// withTest is a spec with its kept test, under the lock.
func (m *MemoryStore) withTest(id string, s Spec) Spec {
	s.Tested, s.RowVersion = nil, m.vers[id]
	if t, ok := m.tests[id]; ok {
		s.Tested = &t
	}
	return s
}

// SaveTest keeps a connection's last test, and changes its revision.
func (m *MemoryStore) SaveTest(_ context.Context, id string, t LastTest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.specs[id]; !ok {
		return ErrNotFound
	}
	t.Result.Steps = append([]Step(nil), t.Result.Steps...)
	m.tests[id] = t
	m.saves++
	m.revs[id] = m.saves
	return nil
}

func (m *MemoryStore) Save(_ context.Context, s Spec, c *Credential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// An edit is held to the version it was read at, as in the database.
	if s.RowVersion > 0 && s.RowVersion != m.vers[s.ID] {
		return ErrConflict
	}
	m.vers[s.ID]++
	// A save never writes the test (TestKeeper).
	s.Tested, s.RowVersion = nil, 0
	m.specs[s.ID] = s
	if c != nil {
		m.creds[s.ID] = *c
	}
	m.saves++
	m.revs[s.ID] = m.saves
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
	delete(m.tests, id)
	delete(m.revs, id)
	m.vers[id]++
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
	return NewSecretStoreOn(cs.CoreV1().Secrets(namespace), namespace, prefix, labels), nil
}

// NewSecretStoreOn makes a store over the Secrets of a namespace as a
// client gives them: for a test's cluster that is not there.
func NewSecretStoreOn(secrets corev1client.SecretInterface, namespace, prefix string, labels map[string]string) *SecretStore {
	all := map[string]string{LabelComponent: ComponentValue}
	for k, v := range labels {
		all[k] = v
	}
	return &SecretStore{secrets: secrets, namespace: namespace, prefix: prefix, labels: all}
}

func (s *SecretStore) Kind() string { return "secrets" }

func (s *SecretStore) name(id string) string { return s.prefix + "-cluster-" + id }

// list lists the connection Secrets of this console: the label marks a
// connection Secret, and the name says whose, since another release in the
// namespace labels its own the same way (the eleventh cluster test's N1).
func (s *SecretStore) list(ctx context.Context) (*corev1.SecretList, error) {
	list, err := s.secrets.List(ctx, metav1.ListOptions{LabelSelector: LabelComponent + "=" + ComponentValue})
	if err != nil {
		return nil, err
	}
	own := list.Items[:0]
	for _, sec := range list.Items {
		if strings.HasPrefix(sec.Name, s.prefix+"-cluster-") {
			own = append(own, sec)
		}
	}
	list.Items = own
	return list, nil
}

func (s *SecretStore) List(ctx context.Context) ([]Spec, error) {
	list, err := s.list(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the connection Secrets in %s: %w", s.namespace, err)
	}
	var out []Spec
	for i := range list.Items {
		spec, err := UnmarshalSpec(list.Items[i].Data[keySpec])
		if err != nil {
			// One unreadable Secret (a newer console's format after a
			// rollback, or a stray label) must not hide every other
			// connection: it is skipped and said in the log.
			slog.Warn("connection Secret skipped: it cannot be read", "secret", list.Items[i].Name, "err", err)
			continue
		}
		out = append(out, spec)
	}
	sortSpecs(out)
	return out, nil
}

// Revisions lists the connection Secrets' resourceVersions, by the
// connection each one holds.
func (s *SecretStore) Revisions(ctx context.Context) (map[string]string, error) {
	list, err := s.list(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the connection Secrets in %s: %w", s.namespace, err)
	}
	out := make(map[string]string, len(list.Items))
	for i := range list.Items {
		if id := s.idOf(&list.Items[i]); id != "" {
			out[id] = list.Items[i].ResourceVersion
		}
	}
	return out, nil
}

// idOf is the connection a Secret is of, by its label or its name; ""
// for a Secret that carries the label and is not named as a connection's.
func (s *SecretStore) idOf(sec *corev1.Secret) string {
	id := sec.Labels[LabelCluster]
	if id == "" {
		id = strings.TrimPrefix(sec.Name, s.prefix+"-cluster-")
	}
	if id == "" || sec.Name != s.name(id) {
		return ""
	}
	return id
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
	name := s.name(spec.ID)
	existing, err := s.secrets.Get(ctx, name, metav1.GetOptions{})
	var prev []byte
	if err == nil {
		prev = existing.Data[keySpec]
	}
	raw, merr := MarshalSpecOver(spec, prev)
	if merr != nil {
		return merr
	}
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

// --- With a database (DB-64): the Secret holds the credential alone ---
//
// The connection's record is then a row of clusters (PGStore), and what
// follows reads and writes the credential's keys and nothing else. None
// of it is given the database.

// all lists the connection Secrets, by name.
func (s *SecretStore) all(ctx context.Context) ([]corev1.Secret, error) {
	list, err := s.list(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the connection Secrets in %s: %w", s.namespace, err)
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	return list.Items, nil
}

// AnnotationServer marks a connection's Secret, with a database, with the
// API server its credential was written for (its scheme, host and port).
// The Secret is written before the row commits: a read that finds it
// written for another server than the row's has met a save that did not
// finish, and pairs neither (Spec.Unsettled).
const AnnotationServer = "221b.ucesys.com/server"

// held is what a connection's Secret holds, as a store with a database
// reads it: the credential, and what says which server it is for.
type held struct {
	Credential
	// found says there is a Secret: none is an empty credential.
	found bool
	// server is the origin the credential was written for (the
	// annotation); "" in a Secret written before it, or by hand.
	server string
	// record is the connection's record ("spec.json") a console from
	// before the move left in it, or wrote since; nil when there is none.
	record []byte
}

// credential reads a connection's credential from its Secret, and says
// whether there is a Secret: none is an empty credential, and a Secret
// that could not be asked for is an error.
func (s *SecretStore) credential(ctx context.Context, id string) (held, error) {
	sec, err := s.secrets.Get(ctx, s.name(id), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return held{}, nil
	}
	if err != nil {
		return held{}, fmt.Errorf("reading the connection Secret %s: %w", s.name(id), err)
	}
	return held{Credential: credentialOf(sec.Data), found: true, server: sec.Annotations[AnnotationServer], record: sec.Data[keySpec]}, nil
}

// putCredential writes a connection's Secret to hold the credential and
// nothing else, marked with the API server it is for (AnnotationServer),
// making it when it is not there. A record a console without the
// database left in it ("spec.json") goes: the database keeps the
// connection now. One a newer console wrote is not saved over (P7-03).
func (s *SecretStore) putCredential(ctx context.Context, id, server string, c Credential) error {
	name := s.name(id)
	existing, err := s.secrets.Get(ctx, name, metav1.GetOptions{})
	switch {
	case err == nil:
		var old stored
		if prev := existing.Data[keySpec]; prev != nil && json.Unmarshal(prev, &old) == nil && old.Version > StorageVersion {
			return fmt.Errorf("the connection Secret %s was written by a newer console (format %d); upgrade this one to change it", name, old.Version)
		}
		existing.Data = dataOf(c)
		if existing.Labels == nil {
			existing.Labels = map[string]string{}
		}
		for k, v := range s.labels {
			existing.Labels[k] = v
		}
		existing.Labels[LabelCluster] = id
		if existing.Annotations == nil {
			existing.Annotations = map[string]string{}
		}
		existing.Annotations[AnnotationServer] = serverOrigin(server)
		if _, err := s.secrets.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("updating the connection Secret %s: %w", name, err)
		}
		return nil
	case apierrors.IsNotFound(err):
		labels := map[string]string{LabelCluster: id}
		for k, v := range s.labels {
			labels[k] = v
		}
		sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.namespace, Labels: labels, Annotations: map[string]string{AnnotationServer: serverOrigin(server)}}, Type: corev1.SecretTypeOpaque, Data: dataOf(c)}
		if _, err := s.secrets.Create(ctx, sec, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("creating the connection Secret %s: %w", name, err)
		}
		return nil
	default:
		return fmt.Errorf("reading the connection Secret %s: %w", name, err)
	}
}

// dropSpec takes "spec.json" out of a Secret as it was listed, in one
// update over the version listed: a Secret written meanwhile is refused
// by the API server and left for the next start. Every other key and the
// labels stay.
func (s *SecretStore) dropSpec(ctx context.Context, sec *corev1.Secret) error {
	next := sec.DeepCopy()
	delete(next.Data, keySpec)
	if _, err := s.secrets.Update(ctx, next, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("taking the record out of the connection Secret %s: %w", sec.Name, err)
	}
	return nil
}
