package nodessh

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/kv"
)

// PairingsKey is the key of the settings ConfigMap the pairings are kept
// under, so every replica sees them.
const PairingsKey = "ssh-pairings.json"

// Pairing is a node paired for SSH: who may log in as whom, and the host
// key the node showed when it was paired, which every session checks.
type Pairing struct {
	Cluster   string `json:"cluster"`
	Node      string `json:"node"`
	LoginUser string `json:"loginUser"`
	Port      int    `json:"port"`
	// HostKeys are the node's host keys, one per type sshd may present,
	// as known_hosts takes them (type and base64), read from the node
	// when it was paired. HostKey is the first, for older records.
	HostKey   string    `json:"hostKey"`
	HostKeys  []string  `json:"hostKeys,omitempty"`
	PairedAt  time.Time `json:"pairedAt"`
	PairedBy  string    `json:"pairedBy,omitempty"`
	CAFinger  string    `json:"caFingerprint"`
	Principal string    `json:"principal"`
}

// Pairings keeps the pairings in the key-value storage.
type Pairings struct {
	backend kv.Backend
	mu      sync.Mutex
}

func NewPairings(b kv.Backend) *Pairings { return &Pairings{backend: b} }

func key(cluster, node string) string { return cluster + "/" + node }

func (p *Pairings) load(ctx context.Context) (map[string]Pairing, string, error) {
	b, version, err := p.backend.LoadKey(ctx, PairingsKey)
	if errors.Is(err, kv.ErrNotFound) {
		return map[string]Pairing{}, version, nil
	}
	if err != nil {
		return nil, "", err
	}
	m := map[string]Pairing{}
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]Pairing{}, version, nil
	}
	return m, version, nil
}

// Get returns a node's pairing, if it has one.
func (p *Pairings) Get(ctx context.Context, cluster, node string) (Pairing, bool, error) {
	m, _, err := p.load(ctx)
	if err != nil {
		return Pairing{}, false, err
	}
	pr, ok := m[key(cluster, node)]
	return pr, ok, nil
}

// List returns a cluster's pairings, by node.
func (p *Pairings) List(ctx context.Context, cluster string) ([]Pairing, error) {
	m, _, err := p.load(ctx)
	if err != nil {
		return nil, err
	}
	var out []Pairing
	for _, pr := range m {
		if pr.Cluster == cluster {
			out = append(out, pr)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out, nil
}

// change edits the map over the version it read, again on a conflict.
func (p *Pairings) change(ctx context.Context, edit func(map[string]Pairing)) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var err error
	for try := 0; try < 5; try++ {
		var m map[string]Pairing
		var version string
		m, version, err = p.load(ctx)
		if err != nil {
			return err
		}
		edit(m)
		b, _ := json.Marshal(m)
		if _, err = p.backend.SaveKey(ctx, PairingsKey, b, version); errors.Is(err, kv.ErrConflict) {
			continue
		}
		return err
	}
	return err
}

// Keys are the pinned host keys: HostKeys, or the single HostKey of an
// older record.
func (p Pairing) Keys() []string {
	if len(p.HostKeys) > 0 {
		return p.HostKeys
	}
	if p.HostKey != "" {
		return []string{p.HostKey}
	}
	return nil
}

// Set records a pairing, replacing the node's earlier one.
func (p *Pairings) Set(ctx context.Context, pr Pairing) error {
	return p.change(ctx, func(m map[string]Pairing) { m[key(pr.Cluster, pr.Node)] = pr })
}

// Delete forgets a node's pairing.
func (p *Pairings) Delete(ctx context.Context, cluster, node string) error {
	return p.change(ctx, func(m map[string]Pairing) { delete(m, key(cluster, node)) })
}
