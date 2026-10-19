package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/auth"
	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/sherlock"
)

// Sherlock (Phase 5) answers questions about a cluster. Each person has a
// conversation per cluster, kept on the server for their session, which
// every window of the session shows and asks into (P5-08).

// sherlockStatus is what a person is told about Sherlock in a cluster.
type sherlockStatus struct {
	// Available says whether questions can be asked now.
	Available bool `json:"available"`
	// Enabled says whether an administrator turned it on for the cluster.
	Enabled bool `json:"enabled"`
	// Key says whether an API key is set.
	Key bool `json:"key"`
	// Reason says why it is not available.
	Reason string `json:"reason,omitempty"`
	// Capped is the message when a spending cap is reached.
	Capped string `json:"capped,omitempty"`
	Model  string `json:"model"`
	// Admin says whether the person may change the settings.
	Admin bool `json:"admin"`
}

func (s *Server) sherlockSettings() settings.Sherlock {
	if s.opts.Settings == nil {
		return settings.DefaultSherlock
	}
	return s.opts.Settings.Current().SherlockOrDefault()
}

func (s *Server) sherlockStatusFor(r *http.Request, cluster string) sherlockStatus {
	sh := s.sherlockSettings()
	sess, _ := auth.SessionFrom(r.Context())
	st := sherlockStatus{Enabled: sh.Clusters[cluster], Key: s.opts.SherlockKeys != nil && s.opts.SherlockKeys.Key() != "", Model: sh.Model, Admin: s.opts.Auth == nil || sess.Admin}
	p := s.sherlockProvider(sh)
	switch {
	case !st.Enabled:
		st.Reason = "Sherlock is off for this cluster. An administrator turns it on in Settings."
	case !st.Key && p.NeedsKey():
		st.Reason = "Sherlock has no API key. An administrator adds one in Settings."
	}
	if err := (sherlock.Caps{DailyUSD: sh.DailyUSD, MonthlyUSD: sh.MonthlyUSD}).Check(s.opts.Sherlock.Ledger().Now(r.Context())); err != nil {
		st.Capped = err.Error()
	}
	st.Available = st.Reason == "" && st.Capped == ""
	return st
}

// sherlockProvider is the provider in force: the settings', or the
// Claude API at the server's default address.
func (s *Server) sherlockProvider(sh settings.Sherlock) sherlock.Provider {
	p := s.opts.SherlockDefaultProvider
	if sh.Provider != nil && !s.opts.SherlockProviderLocked {
		p = *sh.Provider
	}
	return p.Normalized(s.opts.SherlockDefaultURL)
}

// convKey is the conversation a request is about.
func convKey(r *http.Request) sherlock.Key {
	sess, _ := auth.SessionFrom(r.Context())
	return sherlock.Key{Session: sess.ID, Cluster: clusterID(r)}
}

func (s *Server) getSherlock(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.lookup(w, r); !ok {
		return
	}
	b, _ := json.Marshal(s.sherlockStatusFor(r, clusterID(r)))
	writeJSON(w, r, b)
}

var windowRE = regexp.MustCompile(`^[a-z0-9]{8,32}$`)

// windowID is a console window's id as the browser makes it, or empty.
func windowID(s string) string {
	if windowRE.MatchString(s) {
		return s
	}
	return ""
}

// postSherlockUI takes a console window's result of a UI action (P6U-17).
// It must come from the session that asked: the conversation is looked
// up by the poster's own session, so another person's result, or one that
// comes after the action gave up, finds nothing.
func (s *Server) postSherlockUI(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.lookup(w, r); !ok {
		return
	}
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(strings.ToLower(ct)) != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "send the result as application/json")
		return
	}
	var in sherlock.UIResult
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "the result is not valid: "+err.Error())
		return
	}
	if err := s.opts.Sherlock.DeliverUI(convKey(r), r.PathValue("id"), in); err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// maxQuestionBody bounds a question.
const maxQuestionBody = 16 << 10

func (s *Server) postSherlockAsk(w http.ResponseWriter, r *http.Request) {
	_, cl, ok := s.lookup(w, r)
	if !ok {
		return
	}
	id := clusterID(r)
	status := s.sherlockStatusFor(r, id)
	if status.Reason != "" {
		jsonError(w, http.StatusConflict, status.Reason)
		return
	}
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(strings.ToLower(ct)) != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "send the question as application/json")
		return
	}
	var in struct {
		Question string `json:"question"`
		// Window and View are the console window whose view Sherlock may
		// change and its view state (P6U-03, P6U-08).
		Window string `json:"window"`
		View   string `json:"view"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxQuestionBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "the question is not valid: "+err.Error())
		return
	}
	name, version := id, ""
	if s.opts.Clusters != nil {
		if sum, ok := s.opts.Clusters.Summary(id); ok {
			name, version = sum.Name, sum.Version
		}
	}
	sess, _ := auth.SessionFrom(r.Context())
	conv, err := s.opts.Sherlock.Ask(sherlock.Question{
		Key:     convKey(r),
		Text:    in.Question,
		Cluster: name,
		Version: version,
		Tools:   s.toolboxFor(r, id, cl),
		Who:     audit.Who{Subject: sess.User.Subject, Email: sess.User.Email, Name: sess.User.Name},
		From:    fromAddr(r),
		Window:  windowID(in.Window),
		View:    in.View,
		// Read once per question, so a change applies to the next one.
		ViewChanges: s.sherlockSettings().ChangesView(),
	})
	var capErr *sherlock.ErrCap
	switch {
	case errors.As(err, &capErr):
		jsonError(w, http.StatusTooManyRequests, err.Error())
		return
	case errors.Is(err, sherlock.ErrBusy):
		jsonError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	b, _ := json.Marshal(conv.View())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	w.Write(b)
}

// getSherlockConversation returns the conversation as it is now, for
// scripts such as the evaluation; the browser reads the events stream.
func (s *Server) getSherlockConversation(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.lookup(w, r); !ok {
		return
	}
	b, _ := json.Marshal(s.opts.Sherlock.Conversation(convKey(r)).View())
	writeJSON(w, r, b)
}

func (s *Server) postSherlockStop(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.lookup(w, r); !ok {
		return
	}
	stopped := s.opts.Sherlock.Stop(convKey(r))
	b, _ := json.Marshal(map[string]bool{"stopped": stopped})
	writeJSON(w, r, b)
}

func (s *Server) postSherlockNew(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.lookup(w, r); !ok {
		return
	}
	s.opts.Sherlock.Reset(convKey(r))
	b, _ := json.Marshal(s.opts.Sherlock.Conversation(convKey(r)).View())
	writeJSON(w, r, b)
}

// sherlockEvents streams the conversation as Server-Sent Events: a
// "conversation" event with all of it, then one per change, an "answer"
// event when only the last message changed (an answer arriving), and a
// "status" event when Sherlock's availability changes.
func (s *Server) sherlockEvents(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.lookup(w, r); !ok {
		return
	}
	s.serveStream(w, r, map[string]streamPart{"": s.sherlockPart(r)})
}

// sherlockPart streams the person's conversation about the request's
// cluster: its status, the conversation, then each change.
func (s *Server) sherlockPart(r *http.Request) streamPart {
	return func(ctx context.Context, write func(string, any) error) {
		conv := s.opts.Sherlock.Conversation(convKey(r))
		ch := conv.Subscribe()
		defer conv.Unsubscribe(ch)
		id := clusterID(r)
		status := s.sherlockStatusFor(r, id)
		if write("status", status) != nil {
			return
		}
		last := conv.View()
		if write("conversation", last) != nil {
			return
		}
		// UI actions go to the window named in each; a window that connects
		// later does not get earlier ones.
		_, uiSeq := conv.UIRequestsAfter(1 << 62)
		statusTick := time.NewTicker(5 * time.Second)
		defer statusTick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-statusTick.C:
				if now := s.sherlockStatusFor(r, id); now != status {
					status = now
					if write("status", status) != nil {
						return
					}
				}
			case <-ch:
				// Changes come faster than a browser needs them while an
				// answer streams; one update per 50 ms is plenty.
				select {
				case <-ctx.Done():
					return
				case <-time.After(50 * time.Millisecond):
				}
				select {
				case <-ch:
				default:
				}
				var reqs []sherlock.UIRequest
				reqs, uiSeq = conv.UIRequestsAfter(uiSeq)
				for _, q := range reqs {
					if write("ui", q) != nil {
						return
					}
				}
				v := conv.View()
				if v.Rev == last.Rev {
					continue
				}
				var err error
				if len(v.Messages) == len(last.Messages) && len(v.Messages) > 0 && v.ID == last.ID && v.Busy == last.Busy {
					err = write("answer", map[string]any{"rev": v.Rev, "message": v.Messages[len(v.Messages)-1]})
				} else {
					err = write("conversation", v)
				}
				if err != nil {
					return
				}
				last = v
			}
		}
	}
}

// sherlockConfig is the administrator's view of Sherlock's settings.
type sherlockConfig struct {
	Settings settings.Sherlock `json:"settings"`
	// Provider is the provider in force, with its defaults filled in.
	Provider sherlock.Provider `json:"provider"`
	// ProviderLocked says the chart fixed the provider
	// (sherlock.lockProvider): the settings cannot change it.
	ProviderLocked bool `json:"providerLocked"`
	// NeedsKey says whether the provider needs a stored credential.
	NeedsKey bool              `json:"needsKey"`
	Key      sherlock.KeyInfo  `json:"key"`
	Spend    sherlock.Spend    `json:"spend"`
	Models   []sherlock.Model  `json:"models"`
	Clusters []sherlockCluster `json:"clusters"`
	Running  int               `json:"running"`
}

type sherlockCluster struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Env  string `json:"env"`
}

func (s *Server) sherlockAdmin(w http.ResponseWriter, r *http.Request) (auth.Session, bool) {
	sess, ok := auth.SessionFrom(r.Context())
	if s.opts.Auth != nil && (!ok || !sess.Admin) {
		jsonError(w, http.StatusForbidden, "only an administrator can change Sherlock's settings")
		return sess, false
	}
	return sess, true
}

func (s *Server) getSherlockConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.sherlockAdmin(w, r); !ok {
		return
	}
	sh := s.sherlockSettings()
	p := s.sherlockProvider(sh)
	cfg := sherlockConfig{Settings: sh, Provider: p, ProviderLocked: s.opts.SherlockProviderLocked, NeedsKey: p.NeedsKey(), Spend: s.opts.Sherlock.Ledger().Now(r.Context()), Models: sherlock.Models, Running: s.opts.Sherlock.Running()}
	if s.opts.SherlockKeys != nil {
		cfg.Key = s.opts.SherlockKeys.Info()
	}
	if s.opts.Clusters != nil {
		for _, c := range s.opts.Clusters.List() {
			cfg.Clusters = append(cfg.Clusters, sherlockCluster{ID: c.ID, Name: c.Name, Env: string(c.Env)})
		}
	} else {
		cfg.Clusters = []sherlockCluster{{ID: clusters.LocalID, Name: clusters.LocalID}}
	}
	sort.SliceStable(cfg.Clusters, func(i, j int) bool {
		return cfg.Clusters[i].ID == clusters.LocalID && cfg.Clusters[j].ID != clusters.LocalID
	})
	b, _ := json.Marshal(cfg)
	writeJSON(w, r, b)
}

func (s *Server) putSherlockConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.sherlockAdmin(w, r); !ok {
		return
	}
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(strings.ToLower(ct)) != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "send the settings as application/json")
		return
	}
	var in settings.Sherlock
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "the settings are not valid: "+err.Error())
		return
	}
	// Only clusters that exist are kept, and only those turned on.
	on := map[string]bool{}
	for id, v := range in.Clusters {
		if !v {
			continue
		}
		if s.opts.Clusters != nil {
			if _, ok := s.opts.Clusters.Summary(id); !ok {
				continue
			}
		}
		on[id] = true
	}
	in.Clusters = on
	if err := in.Validate(); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	before := s.sherlockSettings()
	if s.opts.SherlockProviderLocked && !reflect.DeepEqual(before.Provider, in.Provider) {
		jsonError(w, http.StatusForbidden, "the chart fixes Sherlock's provider (sherlock.lockProvider); change it there")
		return
	}
	if _, err := s.opts.Settings.UpdateSherlock(r.Context(), in); err != nil {
		s.opts.Logger.Warn("Sherlock settings not saved", "err", err)
		jsonError(w, settingsStatus(err, http.StatusInternalServerError), "The settings were not saved: "+err.Error())
		return
	}
	s.record(r, audit.ActionSherlockSettings, "sherlock", sherlockChange(before, in))
	s.getSherlockConfig(w, r)
}

// sherlockChange says what changed, for the audit record.
func sherlockChange(a, b settings.Sherlock) string {
	var parts []string
	if a.Model != b.Model {
		parts = append(parts, fmt.Sprintf("model %s → %s", a.Model, b.Model))
	}
	if a.DailyUSD != b.DailyUSD {
		parts = append(parts, fmt.Sprintf("daily cap $%.2f → $%.2f", a.DailyUSD, b.DailyUSD))
	}
	if a.MonthlyUSD != b.MonthlyUSD {
		parts = append(parts, fmt.Sprintf("monthly cap $%.2f → $%.2f", a.MonthlyUSD, b.MonthlyUSD))
	}
	if pa, pb := providerText(a.Provider), providerText(b.Provider); pa != pb {
		parts = append(parts, fmt.Sprintf("provider %s → %s", pa, pb))
	}
	if a.PriceIn != b.PriceIn || a.PriceOut != b.PriceOut {
		parts = append(parts, fmt.Sprintf("prices $%g/$%g → $%g/$%g per million tokens", a.PriceIn, a.PriceOut, b.PriceIn, b.PriceOut))
	}
	if a.ChangesView() != b.ChangesView() {
		if b.ChangesView() {
			parts = append(parts, "view changes on")
		} else {
			parts = append(parts, "view changes off")
		}
	}
	ids := map[string]bool{}
	for id := range a.Clusters {
		ids[id] = true
	}
	for id := range b.Clusters {
		ids[id] = true
	}
	var list []string
	for id := range ids {
		list = append(list, id)
	}
	sort.Strings(list)
	for _, id := range list {
		if a.Clusters[id] != b.Clusters[id] {
			if b.Clusters[id] {
				parts = append(parts, "on for "+id)
			} else {
				parts = append(parts, "off for "+id)
			}
		}
	}
	if len(parts) == 0 {
		return "no change"
	}
	return strings.Join(parts, "; ")
}

// providerText says what a provider is, for the audit record: never a
// credential, which the settings do not hold.
func providerText(p *sherlock.Provider) string {
	if p == nil {
		return "Claude API (default)"
	}
	parts := []string{p.Kind}
	if p.Endpoint != "" {
		parts = append(parts, p.Endpoint)
	}
	if p.Auth != "" {
		parts = append(parts, "auth "+p.Auth)
	}
	if p.Region != "" {
		parts = append(parts, p.Region)
	}
	if p.Project != "" {
		parts = append(parts, p.Project)
	}
	if len(p.Headers) > 0 {
		names := make([]string, 0, len(p.Headers))
		for k := range p.Headers {
			names = append(names, k)
		}
		sort.Strings(names)
		parts = append(parts, "headers "+strings.Join(names, ","))
	}
	if p.CACert != "" {
		parts = append(parts, "own CA")
	}
	return strings.Join(parts, " ")
}

func (s *Server) putSherlockKey(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sherlockAdmin(w, r)
	if !ok {
		return
	}
	if s.opts.SherlockKeys == nil {
		jsonError(w, http.StatusNotFound, "Sherlock is not part of this console")
		return
	}
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(strings.ToLower(ct)) != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "send the key as application/json")
		return
	}
	var in struct {
		Key string `json:"key"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		jsonError(w, http.StatusBadRequest, "the key is not valid: "+err.Error())
		return
	}
	auth := s.sherlockProvider(s.sherlockSettings()).Auth
	by := firstOf(sess.User.Email, sess.User.Username, sess.User.Name)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.opts.SherlockKeys.SetFor(ctx, auth, in.Key, by); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, sherlock.ErrFixed) {
			code = http.StatusConflict
		} else if !strings.Contains(err.Error(), "does not look like") && !strings.Contains(err.Error(), "paste") && !strings.Contains(err.Error(), "service account") {
			code = http.StatusInternalServerError
			s.opts.Logger.Warn("Sherlock key not saved", "err", err)
		}
		s.recordOutcome(r, audit.ActionSherlockKey, "sherlock key", "the key was not saved: "+err.Error(), audit.OutcomeFailed)
		jsonError(w, code, err.Error())
		return
	}
	s.record(r, audit.ActionSherlockKey, "sherlock key", "a new credential was saved for the "+auth+" authentication")
	s.getSherlockConfig(w, r)
}

func (s *Server) deleteSherlockKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.sherlockAdmin(w, r); !ok {
		return
	}
	if s.opts.SherlockKeys == nil {
		jsonError(w, http.StatusNotFound, "Sherlock is not part of this console")
		return
	}
	if err := s.opts.SherlockKeys.Delete(r.Context()); err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, sherlock.ErrFixed) {
			code = http.StatusConflict
		}
		jsonError(w, code, err.Error())
		return
	}
	s.record(r, audit.ActionSherlockKey, "sherlock key", "the Claude API key was removed")
	s.getSherlockConfig(w, r)
}

// recordOutcome writes an audit event with an outcome.
func (s *Server) recordOutcome(r *http.Request, action, target, detail, outcome string) {
	if s.opts.Audit == nil {
		return
	}
	sess, _ := auth.SessionFrom(r.Context())
	s.opts.Audit.Record(audit.Event{Action: action, Who: audit.Who{Subject: sess.User.Subject, Email: sess.User.Email, Name: sess.User.Name}, Target: target, Detail: detail, Outcome: outcome, From: fromAddr(r)})
}
