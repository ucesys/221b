// Package toolbox holds the chart's toolbox values (Phase 9, TB-48): which
// tools the console offers, which images they may use, and the limits of
// every session. The values are a ceiling: nothing in the console widens
// them, whatever a person or an assistant asks.
package toolbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The tools, as the menus, the dialogs and the API name them.
const (
	ToolShell          = "shell"
	ToolCommand        = "command"
	ToolForward        = "forward"
	ToolDebugContainer = "debug-container"
	ToolDebugPod       = "debug-pod"
	ToolNodeShell      = "node-shell"
	ToolNodeCommand    = "node-command"
	// SSH to a node (Phase 11, ADR-72): a terminal over SSH with a
	// certificate the console signs; pairing installs the console's CA on
	// the node through a node command, and unpairing removes it.
	ToolSSH       = "ssh"
	ToolSSHPair   = "ssh-pair"
	ToolSSHUnpair = "ssh-unpair"
)

// Tools lists every tool, in menu order.
var Tools = []string{ToolShell, ToolCommand, ToolForward, ToolDebugContainer, ToolDebugPod, ToolNodeShell, ToolNodeCommand, ToolSSH, ToolSSHPair, ToolSSHUnpair}

// Transcript stores.
const (
	TranscriptsNone = "none"
	TranscriptsPVC  = "pvc"
)

// Image is one allowlisted image (TB-48): the reference, pinned by digest
// in the chart, and the capabilities a container from it may add, where
// the namespace's Pod Security level allows them.
type Image struct {
	Image string   `json:"image"`
	Caps  []string `json:"caps,omitempty"`
	// Tools is a short description of what is in it, for the dialogs.
	Tools string `json:"tools,omitempty"`
}

// Duration is a time.Duration that reads "10m" in JSON.
type Duration time.Duration

// UnmarshalJSON reads "10m", "400d", or a number of seconds.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		if n, ok := strings.CutSuffix(s, "d"); ok {
			// Days, which Go's durations lack: retention: 400d.
			if days, err := strconv.Atoi(n); err == nil && days >= 0 {
				*d = Duration(time.Duration(days) * 24 * time.Hour)
				return nil
			}
		}
		v, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("%q is not a duration such as 10m: %w", s, err)
		}
		*d = Duration(v)
		return nil
	}
	var n float64
	if err := json.Unmarshal(b, &n); err != nil {
		return errors.New("a duration is a string such as 10m")
	}
	*d = Duration(time.Duration(n * float64(time.Second)))
	return nil
}

// MarshalJSON writes "10m0s".
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

// D is the time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// Config is the toolbox block of the chart's values.
type Config struct {
	Enabled bool `json:"enabled"`
	// IdleTimeout ends a session with no keystrokes, output or bytes for
	// that long (TB-13).
	IdleTimeout Duration `json:"idleTimeout"`
	// MaxDuration is the hard limit where no duration was approved.
	MaxDuration Duration `json:"maxDuration"`
	// Transcripts is none or pvc (TB-54); TranscriptDir is where pvc keeps
	// them.
	Transcripts   string   `json:"transcripts"`
	TranscriptDir string   `json:"transcriptDir,omitempty"`
	Retention     Duration `json:"retention"`
	History       struct {
		Keep int `json:"keep"`
	} `json:"history"`
	Shell struct {
		Enabled       bool     `json:"enabled"`
		OneCommand    bool     `json:"oneCommand"`
		FileTransfer  bool     `json:"fileTransfer"`
		QuickCommands []string `json:"quickCommands,omitempty"`
	} `json:"shell"`
	PortForward struct {
		Enabled       bool   `json:"enabled"`
		CLIMinVersion string `json:"cliMinVersion,omitempty"`
	} `json:"portForward"`
	DebugContainer struct {
		Enabled      bool     `json:"enabled"`
		MountVolumes bool     `json:"mountVolumes"`
		Images       []string `json:"images,omitempty"`
	} `json:"debugContainer"`
	DebugPod struct {
		Enabled          bool     `json:"enabled"`
		TTLAfterFinished Duration `json:"ttlAfterFinished"`
		Profiles         []string `json:"profiles,omitempty"`
	} `json:"debugPod"`
	NodeShell struct {
		Enabled       bool     `json:"enabled"`
		Namespace     string   `json:"namespace"`
		Image         string   `json:"image"`
		Profiles      []string `json:"profiles,omitempty"`
		Approval      string   `json:"approval"`
		QuickCommands []string `json:"quickCommands,omitempty"`
	} `json:"nodeShell"`
	// SSH is toolbox.ssh (Phase 11, ADR-72): SSH sessions to paired nodes.
	SSH struct {
		Enabled bool `json:"enabled"`
		// Port is sshd's port on the nodes.
		Port int `json:"port"`
		// LoginUser is the OS account offered by default when pairing a
		// node; it must exist on the node.
		LoginUser string `json:"loginUser"`
	} `json:"ssh"`
	Images map[string]Image `json:"images,omitempty"`
	// AdminExec is rbac.roles.admin.exec: the full Admin role may open
	// shells in its namespaces (TB-50). The roles decide; the console only
	// shows it.
	AdminExec bool `json:"adminExec"`
}

// Default is what the chart's values say when nothing is changed, and
// what a console started without the file uses.
func Default() Config {
	var c Config
	c.Enabled = true
	c.IdleTimeout = Duration(10 * time.Minute)
	c.MaxDuration = Duration(60 * time.Minute)
	c.Transcripts = TranscriptsNone
	c.Retention = Duration(400 * 24 * time.Hour)
	c.History.Keep = 300
	c.Shell.Enabled, c.Shell.OneCommand, c.Shell.FileTransfer = true, true, true
	c.Shell.QuickCommands = []string{"env", "cat /proc/1/limits", "ss -tlnp"}
	c.PortForward.Enabled = true
	c.PortForward.CLIMinVersion = "0.8.0"
	c.DebugContainer.Enabled, c.DebugContainer.MountVolumes = true, true
	c.DebugContainer.Images = []string{"general", "network"}
	c.DebugPod.Enabled = true
	c.DebugPod.TTLAfterFinished = Duration(10 * time.Minute)
	c.DebugPod.Profiles = []string{"general", "network"}
	c.NodeShell.Enabled = true
	c.NodeShell.Namespace = "221b-privileged"
	c.NodeShell.Image = "node"
	c.NodeShell.Profiles = []string{ProfileGeneral, ProfileNetAdmin, ProfileSysAdmin}
	c.NodeShell.Approval = ApprovalCluster
	c.SSH.Enabled, c.SSH.Port, c.SSH.LoginUser = false, 22, "root"
	c.NodeShell.QuickCommands = []string{"journalctl -u kubelet -n 200 --no-pager", "df -h /var/lib/containerd /var/log /", "crictl ps -a --state exited", "dmesg -T | tail -n 100"}
	c.Images = map[string]Image{
		"general": {Image: "busybox:1.37", Tools: "sh, ls, cat, tar, wget, nc"},
		"network": {Image: "nicolaka/netshoot:v0.14", Caps: []string{"NET_RAW", "NET_ADMIN"}, Tools: "dig, curl, tcpdump, iperf, nmap, ss"},
		"node":    {Image: "ubuntu:24.04", Tools: "the node's own tools through /host"},
	}
	return c
}

// Node shell profiles (TB-31) and capability profiles for debug containers
// (TB-24).
const (
	ProfileGeneral    = "general"
	ProfileNetAdmin   = "netadmin"
	ProfileSysAdmin   = "sysadmin"
	ProfileRestricted = "restricted"
)

// Node shell approval rules.
const (
	// ApprovalAlways needs a second SuperAdmin on every cluster (TB-32).
	ApprovalAlways = "always"
	// ApprovalCluster follows the cluster's two-person rule, like the
	// other tools: the default since the Toolbox review.
	ApprovalCluster = "cluster"
)

// Read reads the chart's file; an empty path is Default.
func Read(path string) (Config, error) {
	if path == "" {
		return Default(), nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("reading the toolbox values: %w", err)
	}
	return Parse(b)
}

// Parse reads the values over the defaults, so a value left out keeps its
// default, and checks them.
func Parse(b []byte) (Config, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return Config{}, fmt.Errorf("the toolbox values are not valid: %w", err)
	}
	c, def := Default(), Default()
	// Lists and the image map are replaced, not merged, when given, so
	// they are decoded over nothing: decoded over the defaults, a null item
	// kept the default's item at its index (a commented-out netadmin came
	// back), where now it is an empty name that Check refuses.
	lists := []struct {
		v    *[]string
		def  []string
		path []string
	}{
		{&c.Shell.QuickCommands, def.Shell.QuickCommands, []string{"shell", "quickCommands"}},
		{&c.DebugContainer.Images, def.DebugContainer.Images, []string{"debugContainer", "images"}},
		{&c.DebugPod.Profiles, def.DebugPod.Profiles, []string{"debugPod", "profiles"}},
		{&c.NodeShell.Profiles, def.NodeShell.Profiles, []string{"nodeShell", "profiles"}},
		{&c.NodeShell.QuickCommands, def.NodeShell.QuickCommands, []string{"nodeShell", "quickCommands"}},
	}
	for _, l := range lists {
		*l.v = nil
	}
	c.Images = nil
	// A key the console does not know, or one in another case, is
	// refused: encoding/json alone drops the first and matches the second
	// whatever its case, so nodeShell.aproval: always was never applied.
	var tree any
	_ = json.Unmarshal(b, &tree)
	if bad := unknownKeys(tree, reflect.TypeOf(c), "toolbox"); len(bad) > 0 {
		sort.Strings(bad)
		return Config{}, fmt.Errorf("the toolbox values are not valid: %s", strings.Join(bad, "; "))
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("the toolbox values are not valid: %w", err)
	}
	// A list given as null (or empty) is none; one left out is the
	// default.
	for _, l := range lists {
		if *l.v == nil {
			if given(raw, l.path...) {
				*l.v = []string{}
			} else {
				*l.v = l.def
			}
		}
	}
	if c.Images == nil {
		if given(raw, "images") {
			c.Images = map[string]Image{}
		} else {
			c.Images = def.Images
		}
	}
	return c, c.Check()
}

// unknownKeys lists the keys of v that t does not name exactly, by their
// path: t's fields by their json names, a map's values by its element type.
func unknownKeys(v any, t reflect.Type, path string) []string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	var bad []string
	switch t.Kind() {
	case reflect.Map:
		for k, x := range m {
			bad = append(bad, unknownKeys(x, t.Elem(), path+"."+k)...)
		}
	case reflect.Struct:
		fields := map[string]reflect.Type{}
		var names []string
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				continue
			}
			fields[name] = f.Type
			names = append(names, name)
		}
		for k, x := range m {
			ft, ok := fields[k]
			if !ok {
				bad = append(bad, fmt.Sprintf("%s.%s is not a toolbox value (%s takes %s)", path, k, path, strings.Join(names, ", ")))
				continue
			}
			bad = append(bad, unknownKeys(x, ft, path+"."+k)...)
		}
	}
	return bad
}

// given says whether the values name a key, null included: path is the
// keys from the top.
func given(raw map[string]json.RawMessage, path ...string) bool {
	for i, k := range path {
		v, ok := raw[k]
		if !ok {
			return false
		}
		if i == len(path)-1 {
			return true
		}
		raw = nil
		if json.Unmarshal(v, &raw) != nil || raw == nil {
			return false
		}
	}
	return false
}

// Check reports values that cannot work.
func (c Config) Check() error {
	var errs []string
	if c.IdleTimeout.D() < time.Minute {
		errs = append(errs, "toolbox.idleTimeout must be at least 1m")
	}
	if c.MaxDuration.D() < c.IdleTimeout.D() {
		errs = append(errs, "toolbox.maxDuration must be at least toolbox.idleTimeout")
	}
	switch c.Transcripts {
	case TranscriptsNone, "":
	case TranscriptsPVC:
		if c.TranscriptDir == "" {
			errs = append(errs, "toolbox.transcripts pvc needs the directory it is mounted at")
		}
		// The prune runs hourly and takes every transcript not written
		// for the retention, a live session's too: a bare number is
		// seconds, and retention: 400 (meant as days) pruned them all
		// within the hour.
		switch {
		case c.Retention.D() < 24*time.Hour:
			errs = append(errs, fmt.Sprintf("toolbox.retention must be at least 1d with transcripts kept, not %s (a bare number is seconds: write days as 400d)", c.Retention.D()))
		case c.Retention.D() < c.MaxDuration.D():
			errs = append(errs, "toolbox.retention must be at least toolbox.maxDuration, or a live session's transcript is pruned")
		}
	default:
		errs = append(errs, fmt.Sprintf("toolbox.transcripts is none or pvc, not %q", c.Transcripts))
	}
	for _, l := range []struct {
		key  string
		list []string
	}{{"shell.quickCommands", c.Shell.QuickCommands}, {"nodeShell.quickCommands", c.NodeShell.QuickCommands}} {
		for _, q := range l.list {
			if strings.TrimSpace(q) == "" {
				errs = append(errs, fmt.Sprintf("toolbox.%s has an empty entry (a commented-out item is null: remove the line)", l.key))
				break
			}
		}
	}
	for name, im := range c.Images {
		for _, cp := range im.Caps {
			if !capRE.MatchString(cp) {
				errs = append(errs, fmt.Sprintf("toolbox.images.%s.caps: %q is not a capability such as NET_ADMIN", name, cp))
			}
		}
	}
	for _, name := range c.DebugContainer.Images {
		if _, ok := c.Images[name]; !ok {
			errs = append(errs, fmt.Sprintf("toolbox.debugContainer.images names %q, which toolbox.images does not define", name))
		}
	}
	for _, name := range c.DebugPod.Profiles {
		if _, ok := c.Images[name]; !ok {
			errs = append(errs, fmt.Sprintf("toolbox.debugPod.profiles names %q, which toolbox.images does not define", name))
		}
	}
	if c.NodeShell.Enabled {
		if c.NodeShell.Namespace == "" {
			errs = append(errs, "toolbox.nodeShell.namespace is empty")
		}
		if _, ok := c.Images[c.NodeShell.Image]; !ok {
			errs = append(errs, fmt.Sprintf("toolbox.nodeShell.image names %q, which toolbox.images does not define", c.NodeShell.Image))
		}
		for _, p := range c.NodeShell.Profiles {
			if p != ProfileGeneral && p != ProfileNetAdmin && p != ProfileSysAdmin {
				errs = append(errs, fmt.Sprintf("toolbox.nodeShell.profiles: %q is not general, netadmin or sysadmin", p))
			}
		}
		if c.NodeShell.Approval != ApprovalAlways && c.NodeShell.Approval != ApprovalCluster {
			errs = append(errs, "toolbox.nodeShell.approval is always or cluster")
		}
	}
	if c.SSH.Enabled {
		if c.SSH.Port < 1 || c.SSH.Port > 65535 {
			errs = append(errs, "toolbox.ssh.port is 1 to 65535")
		}
		if !LoginUserRE.MatchString(c.SSH.LoginUser) {
			errs = append(errs, "toolbox.ssh.loginUser is a Unix user name")
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// capRE is a Linux capability's name, as a container adds it.
var capRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// LoginUserRE is what a Unix user name looks like, as useradd takes it.
var LoginUserRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// On says whether the chart allows a tool at all.
func (c Config) On(tool string) bool {
	if !c.Enabled {
		return false
	}
	switch tool {
	case ToolShell:
		return c.Shell.Enabled
	case ToolCommand:
		return c.Shell.Enabled && c.Shell.OneCommand
	case ToolForward:
		return c.PortForward.Enabled
	case ToolDebugContainer:
		return c.DebugContainer.Enabled && len(c.DebugContainer.Images) > 0
	case ToolDebugPod:
		return c.DebugPod.Enabled && len(c.DebugPod.Profiles) > 0
	// A node tool is on only where a request for it can pass: a node shell
	// needs a profile, and a node command and a pairing run with the
	// general one (sessions' Request.Validate).
	case ToolNodeShell:
		return c.NodeShell.Enabled && len(c.NodeShell.Profiles) > 0
	case ToolNodeCommand:
		return c.NodeShell.Enabled && slices.Contains(c.NodeShell.Profiles, ProfileGeneral)
	case ToolSSH:
		return c.SSH.Enabled
	case ToolSSHPair:
		// Pairing runs a command on the node through a node pod.
		return c.SSH.Enabled && c.NodeShell.Enabled && slices.Contains(c.NodeShell.Profiles, ProfileGeneral)
	case ToolSSHUnpair:
		// Unpairing too, with the console's own script; it is not held to
		// the profiles, so a node paired before they changed can still be
		// unpaired, nor to toolbox.ssh, so one paired before SSH was
		// turned off can stop trusting the console's authority.
		return c.NodeShell.Enabled
	}
	return false
}

// ImageNames are the images a tool may use, sorted: never nil, so an
// emptied list reaches the page as [], not null.
func (c Config) ImageNames(tool string) []string {
	names := []string{}
	switch tool {
	case ToolDebugContainer:
		names = append(names, c.DebugContainer.Images...)
	case ToolDebugPod:
		names = append(names, c.DebugPod.Profiles...)
	case ToolNodeShell, ToolNodeCommand:
		names = []string{c.NodeShell.Image}
	}
	sort.Strings(names)
	return names
}

// Limit is the hard limit of a session: the duration asked for, capped by
// MaxDuration, or MaxDuration.
func (c Config) Limit(asked time.Duration) time.Duration {
	if asked <= 0 || asked > c.MaxDuration.D() {
		return c.MaxDuration.D()
	}
	return asked
}
