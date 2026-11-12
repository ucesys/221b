package nodessh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"github.com/ucesys/221b/server/internal/kv"
)

// changeTries is how often a save is tried against other writers.
const changeTries = 12

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
	// when it was paired.
	HostKeys []string  `json:"hostKeys,omitempty"`
	PairedAt time.Time `json:"pairedAt"`
	PairedBy string    `json:"pairedBy,omitempty"`
	CAFinger string    `json:"caFingerprint"`
	// Principal is the one this pairing put on the node, which its
	// sessions' certificates carry: a pairing made before consoles had
	// their own keeps the old one until the node is paired again.
	Principal string `json:"principal"`
}

// Pairings keeps the pairings in the key-value storage.
type Pairings struct {
	backend kv.Backend
	mu      sync.Mutex
}

func NewPairings(b kv.Backend) *Pairings { return &Pairings{backend: b} }

func key(cluster, node string) string { return cluster + "/" + node }

func (p *Pairings) load(ctx context.Context) (map[string]Pairing, string, error) {
	return loadKey[Pairing](ctx, p.backend, PairingsKey)
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
	return changeKey(ctx, p.backend, PairingsKey, func(m map[string]Pairing) error {
		edit(m)
		return nil
	})
}

// changeKey edits a map kept under a key over the version it read, again
// on a conflict; an edit that fails saves nothing. The ConfigMap has one
// version for all its keys, and a card of many nodes ends its sessions
// together, each writing its history, so the tries wait a little longer
// each time, at random, to get out of each other's way.
func changeKey[T any](ctx context.Context, backend kv.Backend, key string, edit func(map[string]T) error) error {
	var err error
	for try := 0; try < changeTries; try++ {
		if try > 0 {
			select {
			case <-time.After(rand.N(min(50*time.Millisecond<<try, time.Second))):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		var m map[string]T
		var version string
		m, version, err = loadKey[T](ctx, backend, key)
		if err != nil {
			return err
		}
		if err := edit(m); err != nil {
			return err
		}
		b, _ := json.Marshal(m)
		if _, err = backend.SaveKey(ctx, key, b, version); errors.Is(err, kv.ErrConflict) {
			continue
		}
		return err
	}
	return err
}

// loadKey reads a map kept under a key: empty when never saved, an error
// when not readable, since read as empty the next save would wipe it.
func loadKey[T any](ctx context.Context, backend kv.Backend, key string) (map[string]T, string, error) {
	b, version, err := backend.LoadKey(ctx, key)
	if errors.Is(err, kv.ErrNotFound) {
		return map[string]T{}, version, nil
	}
	if err != nil {
		return nil, "", err
	}
	m := map[string]T{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, "", fmt.Errorf("%s in the settings ConfigMap is not readable: %w", key, err)
	}
	return m, version, nil
}

// Set records a pairing, replacing the node's earlier one.
func (p *Pairings) Set(ctx context.Context, pr Pairing) error {
	return p.change(ctx, func(m map[string]Pairing) { m[key(pr.Cluster, pr.Node)] = pr })
}

// Delete forgets a node's pairing.
func (p *Pairings) Delete(ctx context.Context, cluster, node string) error {
	return p.change(ctx, func(m map[string]Pairing) { delete(m, key(cluster, node)) })
}
