package nodes

import (
	"fmt"
	"net"
	"strconv"
	"time"
)

// RoleHost is the role of a host outside Kubernetes.
const RoleHost = "host"

// Host is what the Nodes tab shows of a host outside Kubernetes (Phase
// 12): never its host key, only the fingerprint a SuperAdmin confirmed.
type Host struct {
	Address     string     `json:"address"`
	Port        int        `json:"port"`
	LoginUser   string     `json:"loginUser"`
	State       string     `json:"state"`
	Fingerprint string     `json:"fingerprint,omitempty"`
	KeyChanged  bool       `json:"keyChanged,omitempty"`
	AddedBy     string     `json:"addedBy,omitempty"`
	TrustedBy   string     `json:"trustedBy,omitempty"`
	TrustedAt   *time.Time `json:"trustedAt,omitempty"`
	PairedBy    string     `json:"pairedBy,omitempty"`
	PairedAt    *time.Time `json:"pairedAt,omitempty"`
}

// HostRow is a host as a row of the Nodes tab: a name, labels, an address
// and where it stands, with the columns only Kubernetes fills left empty.
func HostRow(name string, labels map[string]string, added time.Time, h Host) Row {
	r := Row{
		Name: name, Role: RoleHost, Roles: []string{}, Labels: labels, Created: added.UTC(), InternalIP: h.Address,
		Taints: []Taint{}, Conditions: []Condition{}, Problems: []Problem{}, Host: &h,
	}
	if r.Labels == nil {
		r.Labels = map[string]string{}
	}
	at := net.JoinHostPort(h.Address, strconv.Itoa(h.Port))
	switch {
	case h.KeyChanged:
		r.Status, r.Tone, r.StatusTip = "Key changed", "err", "The host presented a host key other than the pinned one. Nothing connects to it until a SuperAdmin checks the key and trusts it again."
		r.Problems = append(r.Problems, Problem{Title: "Host key changed", Severity: SeverityError, Entries: []Entry{},
			Summary: "The host was reinstalled, or something sits between. Check its key on the host and trust it again from its details."})
	case h.State == "paired":
		r.Status, r.Tone = "Paired", "ok"
		r.StatusTip = fmt.Sprintf("Reached over SSH at %s as %s.", at, h.LoginUser)
		if h.PairedAt != nil {
			r.StatusTip = fmt.Sprintf("Paired %s by %s. Reached over SSH at %s as %s.", stamp(*h.PairedAt), h.PairedBy, at, h.LoginUser)
		}
	case h.Fingerprint == "":
		r.Status, r.Tone, r.StatusTip = "Pending", "warn", "Registered at "+at+". Confirm its host key, then pair it."
		r.Problems = append(r.Problems, Problem{Title: "Not paired", Severity: SeverityWarning, Entries: []Entry{}, Summary: "Confirm the host's key, then pair it, before Automation can reach it."})
	default:
		r.Status, r.Tone, r.StatusTip = "Pending", "warn", "Its host key is pinned. Pair it with a credential, or run the setup command on it and check the connection."
		r.Problems = append(r.Problems, Problem{Title: "Not paired", Severity: SeverityWarning, Entries: []Entry{}, Summary: "Pair the host before Automation can reach it."})
	}
	for _, p := range r.Problems {
		if p.Severity == SeverityError {
			r.Errors++
		} else {
			r.Warnings++
		}
	}
	return r
}
