package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ucesys/221b/server/internal/access"
	"github.com/ucesys/221b/server/internal/acks"
	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/views"
)

// ackRequest acknowledges an alert, or takes the acknowledgement back.
type ackRequest struct {
	Key         string `json:"key"`
	Acknowledge bool   `json:"acknowledge"`
}

// postAck acknowledges an alert for everyone, or takes that back (asked at
// the Phase 4 review). Anyone signed in who may see the alert's object may;
// it changes nothing in the cluster, only what the banners show, and is
// audited. The acknowledgement holds while the alert stays as it is now.
func (s *Server) postAck(w http.ResponseWriter, r *http.Request) {
	sess, signedIn := auth.SessionFrom(r.Context())
	if s.opts.Auth != nil && !signedIn {
		jsonError(w, http.StatusUnauthorized, "sign in to acknowledge alerts")
		return
	}
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(strings.ToLower(ct)) != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "send the acknowledgement as application/json")
		return
	}
	_, cl, ok := s.lookup(w, r)
	if !ok {
		return
	}
	if cl == nil {
		jsonError(w, http.StatusServiceUnavailable, "this cluster's alerts are not served")
		return
	}
	var in ackRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || in.Key == "" {
		jsonError(w, http.StatusBadRequest, "send the alert's key and whether to acknowledge it")
		return
	}
	resource, ns, cluster := live.Scope(live.ViewAlerts, in.Key)
	if resource == "" {
		jsonError(w, http.StatusBadRequest, "no alert "+in.Key)
		return
	}
	if !s.mayListScope(w, r, cl, resource, ns, cluster) {
		return
	}
	now := s.opts.Now().UTC()
	row, exists := views.BuildAlert(cl, in.Key, now)
	if in.Acknowledge && (!exists || row.Severity == views.SevRecovered) {
		jsonError(w, http.StatusConflict, "this alert has cleared: there is nothing to acknowledge")
		return
	}
	who := "anonymous"
	if signedIn {
		for _, v := range []string{sess.User.Email, sess.User.Name, sess.User.Username, sess.User.Subject} {
			if v != "" {
				who = v
				break
			}
		}
	}
	id := clusterID(r)
	// Acknowledgements of alerts that changed or cleared go as this one is
	// saved, so the list keeps only what still holds.
	current := func(key string) (string, bool) {
		a, ok := views.BuildAlert(cl, key, now)
		if !ok || a.Severity == views.SevRecovered {
			return "", false
		}
		return views.AlertFingerprint(a), true
	}
	_, err := s.opts.Settings.UpdateAcks(r.Context(), id, func(list []acks.Ack) []acks.Ack {
		list = acks.Prune(list, current)
		if in.Acknowledge {
			return acks.Set(list, acks.Ack{Key: in.Key, Fingerprint: views.AlertFingerprint(row), By: who, At: now})
		}
		return acks.Remove(list, in.Key)
	})
	if err != nil {
		jsonError(w, http.StatusBadRequest, "The acknowledgement was not saved: "+err.Error())
		return
	}
	action, verb := audit.ActionAlertAck, "acknowledged"
	if !in.Acknowledge {
		action, verb = audit.ActionAlertUnack, "no longer acknowledged"
	}
	s.opts.Logger.Info("alert "+verb, "cluster", id, "alert", in.Key, "user", who)
	if s.opts.Audit != nil {
		ev := audit.Event{Action: action, Cluster: id, Target: in.Key, Outcome: "ok", Detail: row.Status, From: fromAddr(r)}
		if signedIn {
			ev.Who = audit.Who{Subject: sess.User.Subject, Email: sess.User.Email, Name: sess.User.Name}
		}
		s.opts.Audit.Record(ev)
	}
	out := map[string]any{"key": in.Key, "acknowledged": in.Acknowledge}
	if in.Acknowledge {
		out["ack"] = views.AlertAck{By: who, At: now}
	}
	b, _ := json.Marshal(out)
	writeJSON(w, r, b)
}

// mayListScope is mayList for a namespaced or a cluster-scoped resource.
func (s *Server) mayListScope(w http.ResponseWriter, r *http.Request, cl Cluster, resource, namespace string, cluster bool) bool {
	if !cluster {
		return s.mayList(w, r, cl, resource, namespace)
	}
	a := s.access(r, cl)
	if a == nil {
		return true
	}
	pairs := []access.Pair{{Group: groupOf(resource), Resource: resource}}
	if a.Stale(pairs) {
		ctx, cancel := context.WithTimeout(r.Context(), prepareTimeout)
		defer cancel()
		if _, err := a.Prepare(ctx, pairs, 0); err != nil {
			jsonError(w, http.StatusBadGateway, "asking Kubernetes what you may see failed: "+err.Error())
			return false
		}
	}
	if !a.Cluster(resource) {
		jsonError(w, http.StatusForbidden, fmt.Sprintf("Kubernetes does not let you list %s", resource))
		return false
	}
	return true
}
