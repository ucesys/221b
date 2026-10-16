package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/nodes"
)

// homeCluster is one cluster on the home page: its connection, and its
// health and use as the person may see them.
type homeCluster struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Env         string `json:"env"`
	Description string `json:"description,omitempty"`
	Local       bool   `json:"local,omitempty"`
	State       string `json:"state"`
	Message     string `json:"message,omitempty"`
	Version     string `json:"version,omitempty"`
	Expiring    bool   `json:"expiring,omitempty"`
	Expired     bool   `json:"expired,omitempty"`
	Synced      bool   `json:"synced"`
	// Metrics is metrics-server's state: available, unavailable, unknown.
	Metrics     string     `json:"metrics,omitempty"`
	RefreshedAt *time.Time `json:"refreshedAt,omitempty"`

	// Nodes counts the nodes the person may list.
	Nodes homeNodes `json:"nodes"`
	CPU   homeUse   `json:"cpu"`
	// Memory is in bytes; CPU in millicores.
	Memory     homeUse    `json:"memory"`
	Pods       homePods   `json:"pods"`
	Namespaces int        `json:"namespaces"`
	Services   int        `json:"services"`
	Alerts     homeAlerts `json:"alerts"`
	// Sherlock says whether Sherlock can answer about the cluster now, for
	// the card's Find deficiencies (Phase 6 review).
	Sherlock bool `json:"sherlock,omitempty"`
}

type homeNodes struct {
	Total        int `json:"total"`
	Ready        int `json:"ready"`
	ControlPlane int `json:"controlPlane"`
	Cordoned     int `json:"cordoned"`
	Errors       int `json:"errors"`
	Warnings     int `json:"warnings"`
}

type homeUse struct {
	Allocatable int64 `json:"allocatable"`
	Requested   int64 `json:"requested"`
	// Used is what metrics-server reports, over the nodes it has a reading
	// for; UsedNodes is how many.
	Used      int64 `json:"used"`
	UsedNodes int   `json:"usedNodes"`
}

type homePods struct {
	// Total is every pod the person may list.
	Total    int `json:"total"`
	Running  int `json:"running"`
	Capacity int `json:"capacity"`
	NotReady int `json:"notReady"`
	Failing  int `json:"failing"`
}

type homeAlerts struct {
	Failed       int `json:"failed"`
	Pending      int `json:"pending"`
	NodeErrors   int `json:"nodeErrors"`
	NodeWarnings int `json:"nodeWarnings"`
	Volumes      int `json:"volumes"`
	Acknowledged int `json:"acknowledged"`
	Recovered    int `json:"recovered"`
}

// home is every cluster at a glance (asked at the Phase 4 review): each
// connection's state, its nodes, what is requested and used of their CPU
// and memory, its pods and its alerts, worked out from the views the
// console already keeps and filtered, like everything else, by what the
// person may see.
func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	out := []homeCluster{}
	for _, sum := range s.opts.Clusters.List() {
		h := homeOf(sum)
		if s.opts.Sherlock != nil {
			h.Sherlock = s.sherlockStatusFor(r, sum.ID).Available
		}
		st, client, ok := s.opts.Clusters.Lookup(sum.ID)
		cl, _ := client.(Cluster)
		if ok && st != nil && cl != nil {
			s.fillHome(r, &h, st, cl)
		}
		out = append(out, h)
	}
	b, _ := json.Marshal(map[string]any{"clusters": out, "at": s.opts.Now().UTC()})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r, b)
}

func homeOf(sum clusters.Summary) homeCluster {
	return homeCluster{ID: sum.ID, Name: sum.Name, Env: string(sum.Env), Description: sum.Description, Local: sum.ID == clusters.LocalID, State: sum.State, Message: sum.Message, Version: sum.Version, Expiring: sum.Expiring, Expired: sum.Expired, Synced: sum.Synced}
}

// fillHome reads the cluster's nodes and alerts views as the person sees
// them and sums them up.
func (s *Server) fillHome(r *http.Request, h *homeCluster, st *live.Store, cl Cluster) {
	var a = s.opts.Access
	var f live.Filter
	if a != nil {
		if p := s.person(r); !p.Nobody() {
			acc := a.For(h.ID, cl, p)
			if _, err := s.prepare(r.Context(), acc, cl, []string{live.ViewNodes, live.ViewAlerts}); err != nil {
				s.opts.Logger.Warn("asking what a person may see failed", "cluster", h.ID, "err", err)
			}
			f = filterOf(acc)
		}
	}
	var snap struct {
		RefreshedAt *time.Time `json:"refreshedAt"`
		Cluster     *struct {
			Metrics string `json:"metrics"`
			Synced  bool   `json:"synced"`
		} `json:"cluster"`
		Counts map[string]int `json:"counts"`
		Views  map[string]struct {
			Rows []json.RawMessage `json:"rows"`
		} `json:"views"`
	}
	if err := json.Unmarshal(st.Snapshot([]string{live.ViewNodes, live.ViewAlerts}, f), &snap); err != nil {
		return
	}
	h.RefreshedAt = snap.RefreshedAt
	if snap.Cluster != nil {
		h.Metrics, h.Synced = snap.Cluster.Metrics, snap.Cluster.Synced
	}
	h.Pods.Total = snap.Counts[live.ViewPods]
	h.Namespaces = snap.Counts[live.ViewNamespaces]
	h.Services = snap.Counts[live.ViewServices]

	nv := snap.Views[live.ViewNodes]
	for _, raw := range nv.Rows {
		var n struct {
			Name     string          `json:"name"`
			Role     string          `json:"role"`
			Status   string          `json:"status"`
			Cordoned bool            `json:"cordoned"`
			Errors   int             `json:"errors"`
			Warnings int             `json:"warnings"`
			CPU      nodes.Resource  `json:"cpu"`
			Memory   nodes.Resource  `json:"memory"`
			Pods     nodes.PodCounts `json:"pods"`
		}
		if json.Unmarshal(raw, &n) != nil {
			continue
		}
		h.Nodes.Total++
		if strings.HasPrefix(n.Status, "Ready") {
			h.Nodes.Ready++
		}
		if n.Role == "control-plane" {
			h.Nodes.ControlPlane++
		}
		if n.Cordoned {
			h.Nodes.Cordoned++
		}
		if n.Errors > 0 {
			h.Nodes.Errors++
		} else if n.Warnings > 0 {
			h.Nodes.Warnings++
		}
		h.CPU.Allocatable += n.CPU.Allocatable
		h.CPU.Requested += n.CPU.Requested
		h.Memory.Allocatable += n.Memory.Allocatable
		h.Memory.Requested += n.Memory.Requested
		if n.CPU.Used != nil {
			h.CPU.Used += *n.CPU.Used
			h.CPU.UsedNodes++
		}
		if n.Memory.Used != nil {
			h.Memory.Used += *n.Memory.Used
			h.Memory.UsedNodes++
		}
		h.Pods.Running += n.Pods.Running
		h.Pods.Capacity += n.Pods.Capacity
		h.Pods.NotReady += n.Pods.NotReady
		h.Pods.Failing += n.Pods.Failing
	}
	for _, raw := range snap.Views[live.ViewAlerts].Rows {
		var al struct {
			Kind     string          `json:"kind"`
			Severity string          `json:"severity"`
			Ack      json.RawMessage `json:"ack"`
		}
		if json.Unmarshal(raw, &al) != nil {
			continue
		}
		if al.Severity == "recovered" {
			h.Alerts.Recovered++
			continue
		}
		if len(al.Ack) > 0 && string(al.Ack) != "null" {
			h.Alerts.Acknowledged++
			continue
		}
		switch {
		case al.Kind == "pod" && al.Severity == "failed":
			h.Alerts.Failed++
		case al.Kind == "pod":
			h.Alerts.Pending++
		case al.Kind == "node" && al.Severity == "error":
			h.Alerts.NodeErrors++
		case al.Kind == "node":
			h.Alerts.NodeWarnings++
		default:
			h.Alerts.Volumes++
		}
	}
}
