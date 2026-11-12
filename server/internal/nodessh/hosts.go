package nodessh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/nodes"
	"github.com/ucesys/221b/server/internal/toolbox"
)

// HostsKey is the key of the settings ConfigMap the hosts are kept under,
// beside the pairings, so every replica sees them.
const HostsKey = "ssh-hosts.json"

// Host states: registered and not yet reached with a certificate, or
// paired, when the console logs in with one as it does to a node.
const (
	HostPending = "pending"
	HostPaired  = "paired"
)

// Limits keep the record small: the settings ConfigMap gives the hosts
// 128 KiB, which this many typical hosts fit.
const (
	MaxHosts      = 150
	maxHostLabels = 10
)

// Host is a machine outside Kubernetes that Automation reaches by SSH
// from the console, and treats as a node once paired (Phase 12). Its name
// is its node name for fleets and runs; its host key is pinned once a
// SuperAdmin confirmed its fingerprint, before any credential is sent.
type Host struct {
	Cluster   string            `json:"cluster"`
	Name      string            `json:"name"`
	Address   string            `json:"address"`
	Port      int               `json:"port"`
	LoginUser string            `json:"loginUser"`
	Labels    map[string]string `json:"labels,omitempty"`
	State     string            `json:"state"`
	AddedBy   string            `json:"addedBy,omitempty"`
	AddedAt   time.Time         `json:"addedAt"`
	// HostKey is the pinned key, as known_hosts takes it (type and
	// base64), and Fingerprint its SHA256 fingerprint. The key never
	// leaves the server: the page gets the fingerprint.
	HostKey     string     `json:"hostKey,omitempty"`
	Fingerprint string     `json:"fingerprint,omitempty"`
	TrustedBy   string     `json:"trustedBy,omitempty"`
	TrustedAt   *time.Time `json:"trustedAt,omitempty"`
	// Offered is the key the host showed when last asked, waiting for a
	// SuperAdmin to confirm its fingerprint; only that key can be pinned.
	Offered string `json:"offered,omitempty"`
	// Mismatch is the fingerprint of a key the host presented that is not
	// the pinned one: every connection is refused until it is re-trusted.
	Mismatch string     `json:"mismatch,omitempty"`
	PairedBy string     `json:"pairedBy,omitempty"`
	PairedAt *time.Time `json:"pairedAt,omitempty"`
	// AutoName says the host is named after its address: once the console
	// logs in to it, it takes the host's own name (uname -n) when no node
	// or host has that (asked November 5, 2026).
	AutoName bool `json:"autoName,omitempty"`
}

// Hosts keeps the hosts in the key-value storage, with the last list read
// at hand for what cannot wait on a read: the view of the nodes, and the
// engine asking what a target is.
type Hosts struct {
	backend kv.Backend
	mu      sync.Mutex
	cacheMu sync.RWMutex
	// cache is nil until the hosts were first read.
	cache   map[string]Host
	changed func(map[string]Host)
}

func NewHosts(b kv.Backend) *Hosts { return &Hosts{backend: b} }

func (h *Hosts) remember(m map[string]Host) {
	c := make(map[string]Host, len(m))
	for k, v := range m {
		c[k] = v
	}
	h.cacheMu.Lock()
	h.cache = c
	fn := h.changed
	h.cacheMu.Unlock()
	if fn != nil {
		fn(c)
	}
}

// OnRead is called with every list of hosts read or written: main shows
// them on the clusters' Nodes tabs.
func (h *Hosts) OnRead(fn func(map[string]Host)) {
	h.cacheMu.Lock()
	h.changed = fn
	h.cacheMu.Unlock()
}

// HostRows are a cluster's hosts as rows of its Nodes tab.
func HostRows(list []Host) []nodes.Row {
	out := make([]nodes.Row, 0, len(list))
	for _, x := range list {
		out = append(out, nodes.HostRow(x.Name, x.Labels, x.AddedAt, nodes.Host{Address: x.Address, Port: x.Port, LoginUser: x.LoginUser, State: x.State,
			Fingerprint: x.Fingerprint, KeyChanged: x.Mismatch != "", AddedBy: x.AddedBy, TrustedBy: x.TrustedBy, TrustedAt: x.TrustedAt, PairedBy: x.PairedBy, PairedAt: x.PairedAt}))
	}
	return out
}

// ByCluster splits hosts by cluster, each by name.
func ByCluster(m map[string]Host) map[string][]Host {
	out := map[string][]Host{}
	for _, x := range m {
		out[x.Cluster] = append(out[x.Cluster], x)
	}
	for _, l := range out {
		sort.Slice(l, func(i, j int) bool { return l[i].Name < l[j].Name })
	}
	return out
}

// Load reads the hosts again, for the list at hand too.
func (h *Hosts) Load(ctx context.Context) (map[string]Host, error) {
	m, _, err := loadKey[Host](ctx, h.backend, HostsKey)
	if err != nil {
		return nil, err
	}
	h.remember(m)
	return m, nil
}

// Get reads one host.
func (h *Hosts) Get(ctx context.Context, cluster, name string) (Host, bool, error) {
	m, err := h.Load(ctx)
	if err != nil {
		return Host{}, false, err
	}
	x, ok := m[key(cluster, name)]
	return x, ok, nil
}

// List reads a cluster's hosts, by name.
func (h *Hosts) List(ctx context.Context, cluster string) ([]Host, error) {
	m, err := h.Load(ctx)
	if err != nil {
		return nil, err
	}
	return inCluster(m, cluster), nil
}

// Known is a host from the list at hand, read at most a refresh ago;
// read says whether there is a list at hand at all.
func (h *Hosts) Known(cluster, name string) (x Host, ok, read bool) {
	h.cacheMu.RLock()
	defer h.cacheMu.RUnlock()
	x, ok = h.cache[key(cluster, name)]
	return x, ok, h.cache != nil
}

// Cached is a cluster's hosts from the list at hand.
func (h *Hosts) Cached(cluster string) []Host {
	h.cacheMu.RLock()
	defer h.cacheMu.RUnlock()
	return inCluster(h.cache, cluster)
}

func inCluster(m map[string]Host, cluster string) []Host {
	out := []Host{}
	for _, x := range m {
		if x.Cluster == cluster {
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// errNoHost is a host that is not, or no longer, registered.
var errNoHost = errors.New("no such host")

// change edits one cluster's host over the version it read; the edit
// sees the host and whether it exists.
func (h *Hosts) change(ctx context.Context, edit func(m map[string]Host) error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	var kept map[string]Host
	err := changeKey(ctx, h.backend, HostsKey, func(m map[string]Host) error {
		if err := edit(m); err != nil {
			return err
		}
		kept = m
		return nil
	})
	if err == nil {
		h.remember(kept)
	}
	return err
}

// update changes a registered host.
func (h *Hosts) update(ctx context.Context, cluster, name string, edit func(*Host) error) (Host, error) {
	var out Host
	err := h.change(ctx, func(m map[string]Host) error {
		x, ok := m[key(cluster, name)]
		if !ok {
			return fmt.Errorf("%w: %s", errNoHost, name)
		}
		if err := edit(&x); err != nil {
			return err
		}
		m[key(cluster, name)] = x
		out = x
		return nil
	})
	return out, err
}

// ErrInvalid is a host that cannot be registered as given; ErrConflict one
// whose name is taken.
var (
	ErrInvalid  = errors.New("invalid")
	ErrConflict = errors.New("taken")
	ErrNoHost   = errNoHost
)

// hostNameRE is a node's name: a DNS subdomain, as fleets take it.
var hostNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

// addressRE is a host name to resolve; an IP address is parsed instead.
var addressRE = regexp.MustCompile(`^[A-Za-z0-9]([-A-Za-z0-9]*[A-Za-z0-9])?(\.[A-Za-z0-9]([-A-Za-z0-9]*[A-Za-z0-9])?)*$`)

// Validate checks a host as it is registered, trimming what it can.
func (x *Host) Validate() error {
	x.Name = strings.TrimSpace(x.Name)
	x.Address = strings.TrimSpace(x.Address)
	x.LoginUser = strings.TrimSpace(x.LoginUser)
	if x.Port == 0 {
		x.Port = 22
	}
	switch {
	case x.Name == "":
		return fmt.Errorf("%w: name the host", ErrInvalid)
	case len(x.Name) > 253 || !hostNameRE.MatchString(x.Name):
		return fmt.Errorf("%w: a host's name is a node name: lower-case letters, digits, dashes and dots", ErrInvalid)
	case x.Address == "":
		return fmt.Errorf("%w: give the host's address", ErrInvalid)
	case net.ParseIP(x.Address) == nil && (len(x.Address) > 253 || !addressRE.MatchString(x.Address)):
		return fmt.Errorf("%w: the address is an IP address or a DNS name, with no port or scheme", ErrInvalid)
	case x.Port < 1 || x.Port > 65535:
		return fmt.Errorf("%w: the SSH port is 1 to 65535", ErrInvalid)
	case !toolbox.LoginUserRE.MatchString(x.LoginUser):
		return fmt.Errorf("%w: the login user is not a Unix user name", ErrInvalid)
	case len(x.Labels) > maxHostLabels:
		return fmt.Errorf("%w: at most %d labels", ErrInvalid, maxHostLabels)
	}
	for k, v := range x.Labels {
		if errs := validation.IsQualifiedName(k); len(errs) > 0 {
			return fmt.Errorf("%w: label key %q: %s", ErrInvalid, k, errs[0])
		}
		if errs := validation.IsValidLabelValue(v); len(errs) > 0 {
			return fmt.Errorf("%w: label value %q: %s", ErrInvalid, v, errs[0])
		}
	}
	return nil
}

// Add registers a host as pending. A host may not take the name of a node
// of its cluster, nor of another host: the name is its key for pairings,
// fleets and runs. A pending host at the same address is taken up again
// with what is given now, its trusted key kept: a dialog closed before it
// paired is no reason to remove it first (asked November 5, 2026).
func (h *Hosts) Add(ctx context.Context, x Host, isNode func(cluster, name string) bool) (Host, error) {
	if err := x.Validate(); err != nil {
		return Host{}, err
	}
	if isNode != nil && isNode(x.Cluster, x.Name) {
		return Host{}, fmt.Errorf("%w: %s is the name of a Kubernetes node of this cluster", ErrConflict, x.Name)
	}
	x.State = HostPending
	x.AutoName = x.Name == strings.ToLower(x.Address)
	x.HostKey, x.Fingerprint, x.Offered, x.Mismatch, x.TrustedBy, x.TrustedAt, x.PairedBy, x.PairedAt = "", "", "", "", "", nil, "", nil
	err := h.change(ctx, func(m map[string]Host) error {
		if old, ok := m[key(x.Cluster, x.Name)]; ok {
			switch {
			case old.State == HostPending && old.Address == x.Address:
				x.HostKey, x.Fingerprint, x.TrustedBy, x.TrustedAt, x.Mismatch = old.HostKey, old.Fingerprint, old.TrustedBy, old.TrustedAt, old.Mismatch
			case old.State == HostPaired:
				return fmt.Errorf("%w: a host is already called %s, and it is paired: open its row on the Nodes tab", ErrConflict, x.Name)
			default:
				return fmt.Errorf("%w: a host is already called %s, at %s: remove it from its row on the Nodes tab, or give this one another name", ErrConflict, x.Name, old.Address)
			}
		} else if len(m) >= MaxHosts {
			return fmt.Errorf("%w: at most %d hosts", ErrInvalid, MaxHosts)
		}
		m[key(x.Cluster, x.Name)] = x
		return nil
	})
	return x, err
}

// Restore brings a host back from a backup as it was registered: its
// pinned key comes with it (a public key, confirmed when it was pinned)
// and its fingerprint is worked out again; it stays paired only with a
// pinned key. A host or a node of that name here keeps its place.
func (h *Hosts) Restore(ctx context.Context, x Host, isNode func(cluster, name string) bool) (Host, error) {
	if err := x.Validate(); err != nil {
		return Host{}, err
	}
	if isNode != nil && isNode(x.Cluster, x.Name) {
		return Host{}, fmt.Errorf("%w: %s is the name of a Kubernetes node of this cluster", ErrConflict, x.Name)
	}
	x.Offered, x.Mismatch = "", ""
	if x.HostKey != "" {
		if x.Fingerprint = fingerprintOf(x.HostKey); x.Fingerprint == "" {
			return Host{}, fmt.Errorf("%w: the pinned host key is not a public key", ErrInvalid)
		}
	} else {
		x.Fingerprint, x.TrustedBy, x.TrustedAt = "", "", nil
	}
	if x.HostKey == "" || x.State != HostPaired {
		x.State, x.PairedBy, x.PairedAt = HostPending, "", nil
	}
	err := h.change(ctx, func(m map[string]Host) error {
		if _, ok := m[key(x.Cluster, x.Name)]; ok {
			return fmt.Errorf("%w: a host is already called %s", ErrConflict, x.Name)
		}
		if len(m) >= MaxHosts {
			return fmt.Errorf("%w: at most %d hosts", ErrInvalid, MaxHosts)
		}
		m[key(x.Cluster, x.Name)] = x
		return nil
	})
	return x, err
}

// rename gives a host another name, all else kept, and stops it taking
// its hostname again.
func (h *Hosts) rename(ctx context.Context, cluster, from, to string) (Host, error) {
	var out Host
	err := h.change(ctx, func(m map[string]Host) error {
		x, ok := m[key(cluster, from)]
		if !ok {
			return fmt.Errorf("%w: %s", errNoHost, from)
		}
		if _, taken := m[key(cluster, to)]; taken {
			return fmt.Errorf("%w: a host is already called %s", ErrConflict, to)
		}
		delete(m, key(cluster, from))
		x.Name, x.AutoName = to, false
		m[key(cluster, to)] = x
		out = x
		return nil
	})
	return out, err
}

// Delete forgets a host, with its pinned key.
func (h *Hosts) Delete(ctx context.Context, cluster, name string) error {
	return h.change(ctx, func(m map[string]Host) error {
		if _, ok := m[key(cluster, name)]; !ok {
			return fmt.Errorf("%w: %s", errNoHost, name)
		}
		delete(m, key(cluster, name))
		return nil
	})
}

// fingerprintOf is a key line's SHA256 fingerprint, or "".
func fingerprintOf(line string) string {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return ""
	}
	return ssh.FingerprintSHA256(k)
}

// SetLabels replaces a host's labels (Phase 12, a Watson card's
// host.label): checked as a host's labels are when it is added.
func (h *Hosts) SetLabels(ctx context.Context, cluster, name string, labels map[string]string) (Host, error) {
	return h.update(ctx, cluster, name, func(x *Host) error {
		next := *x
		next.Labels = labels
		if err := next.Validate(); err != nil {
			return err
		}
		x.Labels = next.Labels
		return nil
	})
}
