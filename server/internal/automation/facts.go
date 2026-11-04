package automation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// FactsKey holds what is known of each node (Auto-1, second step).
const FactsKey = "facts.json"

const (
	maxFactsBytes = 384 << 10
	maxFacts      = 60
	maxFactValue  = 200
	factsTimeout  = 45 * time.Second
)

// FactsScript is the one built-in script: read-only, it needs no approval,
// and always runs as nobody. It prints key=value lines, and whatever a
// node lacks it leaves out.
const FactsScript = `export PATH=/usr/sbin:/sbin:/usr/bin:/bin
one() { [ -n "$2" ] && printf '%s=%s\n' "$1" "$2"; return 0; }
if [ -r /etc/os-release ]; then . /etc/os-release; fi
one os.name "${PRETTY_NAME:-}"
one os.id "${ID:-}"
one os.version "${VERSION_ID:-}"
family=
case " ${ID:-} ${ID_LIKE:-} " in
*" debian "*|*" ubuntu "*) family=debian ;;
*" rhel "*|*" fedora "*|*" centos "*|*" rocky "*|*" almalinux "*) family=redhat ;;
*" suse "*|*" sles "*|*" opensuse "*) family=suse ;;
esac
one os.family "$family"
one os.kernel "$(uname -r)"
one os.arch "$(uname -m)"
for m in dnf yum apt-get zypper apk; do
  if command -v "$m" >/dev/null 2>&1; then one pkg.manager "$m"; break; fi
done
one cpu.cores "$(nproc 2>/dev/null)"
one cpu.model "$(sed -n 's/^model name[[:space:]]*: *//p' /proc/cpuinfo 2>/dev/null | head -1)"
one memory.bytes "$(awk '/^MemTotal:/ { printf "%.0f", $2 * 1024 }' /proc/meminfo 2>/dev/null)"
one uptime.seconds "$(awk '{ printf "%.0f", $1 }' /proc/uptime 2>/dev/null)"
one timezone "$(cat /etc/timezone 2>/dev/null || readlink /etc/localtime 2>/dev/null | sed 's|.*/zoneinfo/||')"
one net.hostname "$(hostname 2>/dev/null)"
one net.ips "$(hostname -I 2>/dev/null | tr ' ' ',' | sed 's/,*$//')"
one disk.root.percent "$(df -P / 2>/dev/null | awk 'NR==2 { gsub("%", "", $5); print $5 }')"
one disk.var.percent "$(df -P /var 2>/dev/null | awk 'NR==2 { gsub("%", "", $5); print $5 }')"
one disk.devices "$(lsblk -dn -o NAME,SIZE 2>/dev/null | awk '{ printf "%s%s %s", s, $1, $2; s="," }')"
if command -v getenforce >/dev/null 2>&1; then one security.selinux "$(getenforce 2>/dev/null)"; fi
if [ -r /sys/module/apparmor/parameters/enabled ]; then one security.apparmor "$(cat /sys/module/apparmor/parameters/enabled)"; fi
gpus=$(lspci 2>/dev/null | grep -Ei 'vga|3d controller' | grep -Ei 'nvidia|amd|advanced micro' | sed 's/^[^:]*: *//')
if [ -n "$gpus" ]; then
  one gpu.count "$(printf '%s\n' "$gpus" | wc -l | tr -d ' ')"
  one gpu.model "$(printf '%s\n' "$gpus" | head -1)"
fi
one nic.model "$(lspci 2>/dev/null | grep -i 'ethernet controller' | head -1 | sed 's/^[^:]*: *[^:]*: *//')"
one raid.controller "$(lspci 2>/dev/null | grep -i 'raid bus controller' | head -1 | sed 's/^[^:]*: *[^:]*: *//')"
for s in rke2-server rke2-agent k3s k3s-agent kubelet containerd docker sshd ssh chronyd; do
  if systemctl is-active --quiet "$s" 2>/dev/null; then up="${up:-}${up:+,}$s"; fi
done
one service.running "${up:-}"
if command -v openssl >/dev/null 2>&1; then one version.openssl "$(openssl version 2>/dev/null | awk '{ print $2 }')"; fi
if command -v java >/dev/null 2>&1; then one version.java "$(java -version 2>&1 | awk -F'"' 'NR==1 { print $2 }')"; fi
`

// FactKeys are the facts a script can declare.
var FactKeys = []string{
	"os.name", "os.id", "os.version", "os.family", "os.kernel", "os.arch", "pkg.manager", "cpu.cores", "cpu.model", "memory.bytes",
	"uptime.seconds", "timezone", "net.hostname", "net.ips", "disk.root.percent", "disk.var.percent", "disk.devices",
	"security.selinux", "security.apparmor", "gpu.count", "gpu.model", "nic.model", "raid.controller", "service.running",
	"version.openssl", "version.java",
}

// KnownFact says whether a script may declare a fact.
func KnownFact(k string) bool {
	for _, x := range FactKeys {
		if x == k {
			return true
		}
	}
	return false
}

// NodeFacts are what a node said when it was last asked.
type NodeFacts struct {
	Cluster string            `json:"cluster"`
	Node    string            `json:"node"`
	At      time.Time         `json:"at"`
	Facts   map[string]string `json:"facts,omitempty"`
	Error   string            `json:"error,omitempty"`
}

var factLineRE = regexp.MustCompile(`^([a-z][a-z0-9.]*)=(.*)$`)

// ParseFacts reads the built-in script's output. Only known keys are kept,
// so what a node prints cannot fill the record or add names of its own.
func ParseFacts(out string) map[string]string {
	facts := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		m := factLineRE.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil || !KnownFact(m[1]) || len(facts) >= maxFacts {
			continue
		}
		v := strings.TrimSpace(m[2])
		if len(v) > maxFactValue {
			v = v[:maxFactValue]
		}
		if v != "" {
			facts[m[1]] = v
		}
	}
	return facts
}

// FactEnv is the environment name of a fact: os.family is FACT_OS_FAMILY.
func FactEnv(k string) string {
	return "FACT_" + strings.ToUpper(strings.ReplaceAll(k, ".", "_"))
}

func factKey(cluster, node string) string { return cluster + "/" + node }

// Facts lists a cluster's nodes' facts, by node name.
func (st *Store) Facts(ctx context.Context, cluster string) ([]NodeFacts, error) {
	rec := map[string]NodeFacts{}
	if _, err := st.load(ctx, FactsKey, &rec); err != nil {
		return nil, err
	}
	out := []NodeFacts{}
	for _, f := range rec {
		if f.Cluster == cluster {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out, nil
}

// saveFacts keeps what nodes said; when the record is full, the oldest go.
func (st *Store) saveFacts(ctx context.Context, list []NodeFacts) error {
	return change(ctx, st, FactsKey, maxFactsBytes, func(rec *map[string]NodeFacts) error {
		if *rec == nil {
			*rec = map[string]NodeFacts{}
		}
		for _, f := range list {
			(*rec)[factKey(f.Cluster, f.Node)] = f
		}
		for size := approximate(*rec); size > maxFactsBytes*9/10 && len(*rec) > 1; size = approximate(*rec) {
			oldest := ""
			for k, f := range *rec {
				if oldest == "" || f.At.Before((*rec)[oldest].At) {
					oldest = k
				}
			}
			delete(*rec, oldest)
		}
		return nil
	})
}

func approximate(m map[string]NodeFacts) int {
	n := 0
	for _, f := range m {
		n += len(f.Node) + len(f.Cluster) + 80
		for k, v := range f.Facts {
			n += len(k) + len(v) + 6
		}
	}
	return n
}

// asked is one node's answer to the built-in script.
func (e *Engine) askFacts(ctx context.Context, cluster, node, keyID string) NodeFacts {
	ctx, cancel := context.WithTimeout(ctx, factsTimeout)
	defer cancel()
	var out strings.Builder
	exit, err := e.opts.Exec.Exec(ctx, cluster, node, keyID, "sh -s", true, strings.NewReader(FactsScript), limited{&out, 16 << 10})
	f := NodeFacts{Cluster: cluster, Node: node, At: e.opts.Now().UTC()}
	switch {
	case err != nil:
		f.Error = err.Error()
	case exit != 0:
		f.Error = fmt.Sprintf("the facts script exited with %d", exit)
	default:
		f.Facts = ParseFacts(out.String())
		if len(f.Facts) == 0 {
			f.Error = "the node said nothing"
		}
	}
	return f
}

// limited drops what passes n bytes.
type limited struct {
	w io.Writer
	n int
}

func (l limited) Write(p []byte) (int, error) {
	if b, ok := l.w.(*strings.Builder); ok && b.Len() >= l.n {
		return len(p), nil
	}
	return l.w.Write(p)
}

// CollectFacts asks each node and keeps the answers, an error included: a
// node that stopped answering shows that, and keeps nothing old as if new.
func (e *Engine) CollectFacts(ctx context.Context, cluster string, nodes []string, who Who) ([]NodeFacts, error) {
	if len(nodes) == 0 {
		return nil, fmt.Errorf("%w: no paired node to ask", ErrInvalid)
	}
	keyID := fmt.Sprintf("%s via 221B automation facts", whoName(who))
	out := make([]NodeFacts, len(nodes))
	sem := make(chan struct{}, e.opts.MaxParallel)
	var wg sync.WaitGroup
	for i, node := range nodes {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, node string) {
			defer wg.Done()
			defer func() { <-sem }()
			out[i] = e.askFacts(ctx, cluster, node, keyID)
		}(i, node)
	}
	wg.Wait()
	if err := e.opts.Store.saveFacts(context.WithoutCancel(ctx), out); err != nil {
		return out, errors.Join(errors.New("the facts could not be kept"), err)
	}
	return out, nil
}

func whoName(w Who) string {
	switch {
	case w.User != "":
		return w.User
	case w.Name != "":
		return w.Name
	}
	return "console"
}
