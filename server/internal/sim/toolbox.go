package sim

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
)

// The Toolbox (Phase 9) against the simulator: a pod's exec over the
// WebSocket protocol (v5.channel.k8s.io) with a small pretend shell, the
// ephemeralcontainers subresource, and the controllers that run what a
// session creates: a Job's pod, a node shell's pod. Port forwards are not
// simulated.

func (s *Sim) toolboxRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/v1/namespaces/{ns}/pods/{name}/exec", s.api(s.exec))
	m.HandleFunc("POST /api/v1/namespaces/{ns}/pods/{name}/exec", s.api(s.exec))
	m.HandleFunc("PUT /api/v1/namespaces/{ns}/pods/{name}/ephemeralcontainers", s.api(s.ephemeral))
	m.HandleFunc("PATCH /api/v1/namespaces/{ns}/pods/{name}/ephemeralcontainers", s.api(s.ephemeral))
}

// ephemeral sets a pod's ephemeral containers, which then run.
func (s *Sim) ephemeral(w http.ResponseWriter, r *http.Request) {
	var in corev1.Pod
	if err := decodeBody(r, &in); err != nil {
		status(w, http.StatusBadRequest, metav1.StatusReasonBadRequest, err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := r.PathValue("ns") + "/" + r.PathValue("name")
	cur, ok := s.pods[key]
	if !ok {
		status(w, http.StatusNotFound, metav1.StatusReasonNotFound, fmt.Sprintf("pods %q not found", r.PathValue("name")))
		return
	}
	p := cur.DeepCopy()
	p.Spec.EphemeralContainers = in.Spec.EphemeralContainers
	have := map[string]bool{}
	for _, st := range p.Status.EphemeralContainerStatuses {
		have[st.Name] = true
	}
	now := metav1.NewTime(s.opts.Now().UTC())
	for _, ec := range p.Spec.EphemeralContainers {
		if !have[ec.Name] {
			p.Status.EphemeralContainerStatuses = append(p.Status.EphemeralContainerStatuses, corev1.ContainerStatus{Name: ec.Name, Image: ec.Image, Ready: false, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: now}}})
		}
	}
	if dryRun(r) {
		respond(w, http.StatusOK, p)
		return
	}
	s.putLocked(mustKind("pods"), p, watch.Modified)
	respond(w, http.StatusOK, p)
}

// toolboxReactLocked runs what a session creates: a bare pod starts
// running, a Job gets its pod, and a deleted Job takes its pods.
func (s *Sim) toolboxReactLocked(kindName string, old, next runtime.Object) {
	switch kindName {
	case "pods":
		if p, ok := next.(*corev1.Pod); ok && old == nil && p.Status.Phase == "" {
			cp := p.DeepCopy()
			s.runLocked(cp)
			s.putLocked(mustKind("pods"), cp, watch.Modified)
		}
	case "jobs":
		if j, ok := next.(*batchv1.Job); ok && old == nil {
			p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: j.Namespace, Name: j.Name + "-" + s.suffixLocked(), Labels: map[string]string{}, Annotations: j.Spec.Template.Annotations,
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: j.Name, UID: j.UID, Controller: ptrBool(true)}}},
				Spec: *j.Spec.Template.Spec.DeepCopy()}
			for k, v := range j.Spec.Template.Labels {
				p.Labels[k] = v
			}
			p.Labels["job-name"] = j.Name
			p.Labels["batch.kubernetes.io/job-name"] = j.Name
			p.UID = uid("pods/" + p.Namespace + "/" + p.Name)
			p.CreationTimestamp = metav1.NewTime(s.opts.Now().UTC().Truncate(time.Second))
			s.runLocked(p)
			s.putLocked(mustKind("pods"), p, watch.Added)
			cp := j.DeepCopy()
			cp.Status.Active = 1
			s.putLocked(mustKind("jobs"), cp, watch.Modified)
		}
		if j, ok := old.(*batchv1.Job); ok && next == nil {
			for _, p := range s.ownedPodsLocked(j.Namespace, j.UID) {
				s.putLocked(mustKind("pods"), p, watch.Deleted)
			}
		}
	}
}

// runLocked schedules a pod the simulator did not make and starts it.
func (s *Sim) runLocked(p *corev1.Pod) {
	if p.Spec.NodeName == "" {
		var names []string
		for n, node := range s.nodes {
			if !node.Spec.Unschedulable {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		if len(names) > 0 {
			p.Spec.NodeName = names[len(p.Name)%len(names)]
		}
	}
	now := metav1.NewTime(s.opts.Now().UTC())
	p.Status.Phase = corev1.PodRunning
	p.Status.StartTime = &now
	p.Status.PodIP = fmt.Sprintf("10.244.%d.%d", len(p.Name)%200, 10+len(p.Namespace)%200)
	p.Status.ContainerStatuses = nil
	for _, c := range p.Spec.Containers {
		p.Status.ContainerStatuses = append(p.Status.ContainerStatuses, corev1.ContainerStatus{Name: c.Name, Image: c.Image, Ready: true, Started: ptrBool(true), State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: now}}})
	}
	// A readiness gate the pod never meets keeps it not Ready, as a
	// debug pod asks.
	setPodReady(p, len(p.Spec.ReadinessGates) == 0, now.Time)
}

var execUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	Subprotocols:    []string{"v5.channel.k8s.io", "v4.channel.k8s.io"},
	CheckOrigin:     func(*http.Request) bool { return true },
}

// exec runs a command in a pod: a pretend shell with a prompt for a
// terminal, canned output for one command.
func (s *Sim) exec(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	s.mu.Lock()
	p, ok := s.pods[ns+"/"+name]
	var pod *corev1.Pod
	if ok {
		pod = p.DeepCopy()
	}
	s.mu.Unlock()
	if pod == nil {
		status(w, http.StatusNotFound, metav1.StatusReasonNotFound, fmt.Sprintf("pods %q not found", name))
		return
	}
	if !websocket.IsWebSocketUpgrade(r) {
		// SPDY is not simulated: the fallback executor tries the WebSocket first.
		status(w, http.StatusBadRequest, metav1.StatusReasonBadRequest, "the simulator speaks exec over WebSocket only")
		return
	}
	q := r.URL.Query()
	cmd := q["command"]
	tty := q.Get("tty") == "true"
	container := q.Get("container")
	conn, err := execUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	var wmu sync.Mutex
	write := func(stream byte, b []byte) {
		wmu.Lock()
		defer wmu.Unlock()
		_ = conn.WriteMessage(websocket.BinaryMessage, append([]byte{stream}, b...))
	}
	finish := func(code int) {
		st := metav1.Status{Status: metav1.StatusSuccess}
		if code != 0 {
			st = metav1.Status{Status: metav1.StatusFailure, Reason: "NonZeroExitCode", Message: fmt.Sprintf("command terminated with non-zero exit code: exit status %d", code),
				Details: &metav1.StatusDetails{Causes: []metav1.StatusCause{{Type: "ExitCode", Message: fmt.Sprint(code)}}}}
		}
		b, _ := json.Marshal(st)
		write(3, b)
		wmu.Lock()
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		wmu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	host := name
	nodeShell := false
	if len(cmd) >= 2 && cmd[0] == "chroot" && cmd[1] == "/host" {
		host, nodeShell = pod.Spec.NodeName, true
		cmd = cmd[2:]
	}
	if !tty {
		line := strings.Join(cmd, " ")
		if len(cmd) >= 3 && cmd[0] == "sh" && cmd[1] == "-c" {
			line = cmd[2]
		}
		out, code := cannedOutput(line, pod, host, container, nodeShell)
		if code == 0 {
			write(1, []byte(out))
		} else {
			write(2, []byte(out))
		}
		finish(code)
		return
	}
	user := "root"
	if container != "" && strings.HasPrefix(container, "221b-") {
		host = name
	}
	if pod.Namespace == "221b-toolbox" || nodeShell {
		host = pod.Spec.NodeName
	}
	prompt := fmt.Sprintf("%s@%s:/# ", user, host)
	write(1, []byte(prompt))
	var line []byte
	for {
		kind, b, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if kind != websocket.BinaryMessage || len(b) == 0 {
			continue
		}
		switch b[0] {
		case 0:
			for _, c := range b[1:] {
				switch c {
				case '\r', '\n':
					write(1, []byte("\r\n"))
					cmdline := strings.TrimSpace(string(line))
					line = line[:0]
					if cmdline == "exit" || cmdline == "logout" {
						write(1, []byte("exit\r\n"))
						finish(0)
						return
					}
					if cmdline != "" {
						out, _ := cannedOutput(cmdline, pod, host, container, pod.Namespace == "221b-toolbox")
						write(1, []byte(strings.ReplaceAll(out, "\n", "\r\n")))
					}
					write(1, []byte(prompt))
				case 0x7f, 0x08:
					if len(line) > 0 {
						line = line[:len(line)-1]
						write(1, []byte("\b \b"))
					}
				case 0x03:
					line = line[:0]
					write(1, []byte("^C\r\n"+prompt))
				case 0x04:
					if len(line) == 0 {
						write(1, []byte("exit\r\n"))
						finish(0)
						return
					}
				default:
					if c >= 0x20 {
						line = append(line, c)
						write(1, []byte{c})
					}
				}
			}
		case 4, 255:
			// A resize, or v5's close of stdin.
		}
	}
}

// cannedOutput is what the pretend shell answers to a command.
func cannedOutput(line string, pod *corev1.Pod, host, container string, node bool) (string, int) {
	word := strings.Fields(line)
	if len(word) == 0 {
		return "", 0
	}
	switch {
	case word[0] == "hostname":
		return host + "\n", 0
	case word[0] == "id":
		return "uid=0(root) gid=0(root) groups=0(root)\n", 0
	case word[0] == "pwd":
		return "/\n", 0
	case word[0] == "whoami":
		return "root\n", 0
	case word[0] == "ls":
		return "bin   dev  etc   home  lib   proc  root  run   sys  tmp  usr  var\n", 0
	case word[0] == "env":
		return fmt.Sprintf("HOSTNAME=%s\nKUBERNETES_SERVICE_HOST=10.96.0.1\nKUBERNETES_SERVICE_PORT=443\nPATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\nHOME=/root\n", host), 0
	case strings.HasPrefix(line, "cat /etc/resolv.conf"):
		return fmt.Sprintf("search %s.svc.cluster.local svc.cluster.local cluster.local\nnameserver 10.96.0.10\noptions ndots:5\n", pod.Namespace), 0
	case strings.HasPrefix(line, "cat /proc/1/limits"):
		return "Limit                     Soft Limit           Hard Limit           Units\nMax open files            1048576              1048576              files\nMax processes             unlimited            unlimited            processes\n", 0
	case word[0] == "ss" || word[0] == "netstat":
		return "State   Recv-Q  Send-Q   Local Address:Port    Peer Address:Port\nLISTEN  0       4096           0.0.0.0:8080         0.0.0.0:*\n", 0
	case word[0] == "ps":
		return "PID   USER     TIME  COMMAND\n    1 root      0:42 /app/server\n   23 root      0:00 sh\n", 0
	case word[0] == "df":
		if node {
			return "Filesystem      Size  Used Avail Use% Mounted on\n/dev/nvme0n1p1  100G   84G   16G  84% /\n/dev/nvme1n1    200G   61G  139G  31% /var/lib/containerd\n", 0
		}
		return "Filesystem      Size  Used Avail Use% Mounted on\noverlay         100G   42G   58G  42% /\n", 0
	case word[0] == "journalctl":
		return fmt.Sprintf("Oct 26 09:41:02 %s kubelet[1123]: I1026 09:41:02.118 kubelet.go:2412] \"SyncLoop (PLEG): event for pod\"\nOct 26 09:41:07 %s kubelet[1123]: I1026 09:41:07.501 eviction_manager.go:366] \"Eviction manager: attempting to reclaim\" resourceName=\"ephemeral-storage\"\n", host, host), 0
	case word[0] == "crictl":
		return "CONTAINER      IMAGE          CREATED        STATE    NAME     ATTEMPT  POD ID\n3f1c0a9e2b7d   8c811b4aec35   2 hours ago    Exited   migrate  0        9d2e41f0c1aa\n", 0
	case word[0] == "dmesg":
		return "[Mon Oct 26 08:12:44 2026] EXT4-fs (nvme1n1): mounted filesystem with ordered data mode\n", 0
	case word[0] == "echo":
		return strings.Join(word[1:], " ") + "\n", 0
	case word[0] == "nslookup" || word[0] == "dig" || word[0] == "getent":
		return "Server:\t\t10.96.0.10\nAddress:\t10.96.0.10#53\n\nName:\t" + word[len(word)-1] + "\nAddress: 10.96.142.7\n", 0
	case word[0] == "curl" || word[0] == "wget":
		return "{\"status\":\"ok\"}\n", 0
	case word[0] == "false":
		return "", 1
	case word[0] == "true":
		return "", 0
	}
	return fmt.Sprintf("sh: %s: not found in the simulator\n", word[0]), 127
}
