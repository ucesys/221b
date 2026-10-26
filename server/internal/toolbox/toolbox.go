// Package toolbox holds the chart's toolbox values (Phase 9, TB-48): which
// tools the console offers, which images they may use, and the limits of
// every session. The values are a ceiling: nothing in the console widens
// them, whatever a person or an assistant asks.
package toolbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
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
)

// Tools lists every tool, in menu order.
var Tools = []string{ToolShell, ToolCommand, ToolForward, ToolDebugContainer, ToolDebugPod, ToolNodeShell, ToolNodeCommand}

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

// UnmarshalJSON reads "10m", or a number of seconds.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
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
	c.NodeShell.Namespace = "221b-toolbox"
	c.NodeShell.Image = "node"
	c.NodeShell.Profiles = []string{ProfileGeneral, ProfileNetAdmin, ProfileSysAdmin}
	c.NodeShell.Approval = ApprovalAlways
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
	ProfileBaseline   = "baseline"
	ProfileRestricted = "restricted"
)

// Node shell approval rules.
const (
	// ApprovalAlways needs a second SuperAdmin on every cluster (TB-32).
	ApprovalAlways = "always"
	// ApprovalCluster follows the cluster's two-person rule, like the
	// other tools.
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
	c := Default()
	// Lists and the image map are replaced, not merged, when given.
	c.Images = nil
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("the toolbox values are not valid: %w", err)
	}
	if c.Images == nil {
		c.Images = Default().Images
	}
	return c, c.Check()
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
	default:
		errs = append(errs, fmt.Sprintf("toolbox.transcripts is none or pvc, not %q", c.Transcripts))
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
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

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
	case ToolNodeShell, ToolNodeCommand:
		return c.NodeShell.Enabled
	}
	return false
}

// ImageNames are the images a tool may use, sorted.
func (c Config) ImageNames(tool string) []string {
	var names []string
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
