package knowledge

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
)

// View is Settings › Knowledge: the sources with whether each credential
// is set (never the credential), where the wiki is kept, the last syncs,
// the drafts waiting and the store's state.
type View struct {
	Settings Settings             `json:"settings"`
	Creds    map[string]CredState `json:"creds"`
	Store    CredState            `json:"storeCreds"`
	Hook     bool                 `json:"hook"`
	Status   Status               `json:"status"`
	Pages    int                  `json:"pages"`
	Excerpts int                  `json:"excerpts"`
	Pulled   time.Time            `json:"pulled,omitzero"`
	// Documents are the uploaded documents, by source.
	Documents map[string][]ItemState `json:"documents,omitempty"`
	Waiting   int                    `json:"waiting"`
	// Identity says whether the pod has an AWS identity of its own.
	Identity bool `json:"identity"`
	Model    bool `json:"model"`
	Console  bool `json:"console"`
	// ConfigMapStore says the temporary ConfigMap store can be chosen
	// (CF-57). While the settings name no store and it can, the view's
	// store is it.
	ConfigMapStore bool `json:"configMapStore"`
}

// GetView builds Settings › Knowledge's view.
func (s *Service) GetView(ctx context.Context, c Caller) (View, error) {
	if !c.SuperAdmin {
		return View{}, wrapf(ErrDenied, "only a SuperAdmin sees Mycroft's sources")
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return View{}, err
	}
	v := View{Settings: set, Creds: map[string]CredState{}, Documents: map[string][]ItemState{}, Identity: s.o.Identity.Available(), Model: s.o.Model != nil, Console: s.o.Console != nil, ConfigMapStore: s.cmStore != nil}
	v.Settings.Store.Kind = s.storeKind(set.Store)
	for _, x := range set.Sources {
		cr, err := s.creds(ctx, sourceKey(x.ID))
		if err != nil {
			return View{}, err
		}
		v.Creds[x.ID] = cr.state()
		if x.Kind == SourceUpload {
			v.Documents[x.ID] = s.Documents(x.ID)
		}
	}
	st, err := s.creds(ctx, storeKey)
	if err != nil {
		return View{}, err
	}
	v.Store = st.state()
	h, err := s.creds(ctx, hookKey)
	if err != nil {
		return View{}, err
	}
	v.Hook = h.Token != ""
	if v.Status, err = s.Status(ctx); err != nil {
		return View{}, err
	}
	s.mu.RLock()
	for p := range s.files {
		switch {
		case strings.HasPrefix(p, PagesDir):
			v.Pages++
		case strings.HasPrefix(p, ExcerptsDir):
			v.Excerpts++
		}
	}
	v.Pulled = s.pulled
	s.mu.RUnlock()
	runs, err := s.Runs(ctx, false)
	if err == nil {
		v.Waiting = len(runs)
	}
	return v, nil
}

// StoreInput is a store's settings and the keys typed with them.
type StoreInput struct {
	Store StoreSettings `json:"store"`
	Keys  CredsChange   `json:"keys"`
}

func (s *Service) storeWith(ctx context.Context, in StoreInput) (Store, StoreSettings, error) {
	st := in.Store.normalized()
	if err := st.Validate(); err != nil {
		return nil, st, err
	}
	if s.storeKind(st) == StoreConfigMap {
		// The temporary store (CF-57) needs no keys.
		store, err := s.openStore(StoreSettings{Kind: StoreConfigMap}, Creds{})
		return store, st, err
	}
	if st.Kind == "" {
		return nil, st, ErrNoStore
	}
	have, err := s.creds(ctx, storeKey)
	if err != nil {
		return nil, st, err
	}
	keys := in.Keys.apply(have)
	if st.Kind == StoreS3 && st.Credentials == CredentialsKeys && (keys.AccessKeyID == "" || keys.SecretAccessKey == "") {
		return nil, st, invalid("an access key is needed: give its ID and secret, or use the pod's identity")
	}
	if s.open != nil {
		store, err := s.open(st, keys)
		return store, st, err
	}
	if s.o.Store != nil {
		return s.o.Store, st, nil
	}
	store, err := s.openStore(st, keys)
	return store, st, err
}

// TestStore reads a store as typed, without saving it: whether it is
// reached, what it holds, and its warnings.
func (s *Service) TestStore(ctx context.Context, c Caller, in StoreInput) (StoreCheck, error) {
	if !c.SuperAdmin {
		return StoreCheck{}, wrapf(ErrDenied, "only a SuperAdmin changes where the wiki is kept")
	}
	if err := s.leads(); err != nil {
		return StoreCheck{}, err
	}
	store, _, err := s.storeWith(ctx, in)
	if err != nil {
		return StoreCheck{}, err
	}
	tctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	return store.Check(tctx)
}

// SaveStore saves where the wiki is kept. Changing to another store while
// runs wait is refused: their pages were staged in the store they came
// from. Use MoveStore to take the pages along.
func (s *Service) SaveStore(ctx context.Context, c Caller, in StoreInput) (StoreSettings, error) {
	if !c.SuperAdmin {
		return StoreSettings{}, wrapf(ErrDenied, "only a SuperAdmin changes where the wiki is kept")
	}
	if err := s.leads(); err != nil {
		return StoreSettings{}, err
	}
	st := in.Store.normalized()
	if err := st.Validate(); err != nil {
		return StoreSettings{}, err
	}
	if st.Kind == StoreConfigMap && s.cmStore == nil {
		return StoreSettings{}, invalid("this console has no ConfigMap for the wiki (the chart's <release>-wiki, -wiki-configmap): choose Git or S3")
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return StoreSettings{}, err
	}
	if was := s.storeKind(set.Store); was != "" && s.storeKind(st) != was {
		if runs, err := s.Runs(ctx, false); err == nil && len(runs) > 0 {
			return StoreSettings{}, invalid("%d runs wait for review in the store the wiki is kept in now: approve or reject them first", len(runs))
		}
	}
	st.SavedBy, st.SavedAt = c.label(), s.o.Now().UTC()
	if _, err := s.editSettings(ctx, func(v *Settings) error { v.Store = st; return nil }); err != nil {
		return StoreSettings{}, err
	}
	if err := s.saveCreds(ctx, storeKey, in.Keys); err != nil {
		return st, err
	}
	s.audit(audit.Event{Action: ActionStore, Who: c.who(), Outcome: audit.OutcomeOK, From: c.From, Detail: describeStore(st, in.Keys)})
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		if err := s.Refresh(ctx); err != nil && !errors.Is(err, ErrNoStore) {
			s.o.Logger.Warn("reading the wiki from its new store failed", "err", err)
		}
	}()
	return st, nil
}

func describeStore(st StoreSettings, keys CredsChange) string {
	var d string
	switch st.Kind {
	case StoreGit:
		d = fmt.Sprintf("the wiki is kept in the git repository %s, branch %s", st.URL, st.Branch)
	case StoreS3:
		d = fmt.Sprintf("the wiki is kept in the bucket %s under %q (%s), credentials %s", st.Bucket, st.Prefix, st.Endpoint, st.Credentials)
		if st.SSE != "" {
			d += ", encrypted " + st.SSE
		}
	case StoreConfigMap:
		d = "the wiki is kept in the ConfigMap <release>-wiki, temporary, for testing"
	case "":
		d = "the wiki is kept where the console keeps it by default: the temporary ConfigMap store when the chart made it, otherwise nowhere"
	default:
		d = "the wiki is kept nowhere"
	}
	for _, x := range []struct {
		v    *string
		name string
	}{{keys.Token, "token"}, {keys.AccessKeyID, "access key ID"}, {keys.SecretAccessKey, "secret access key"}} {
		switch {
		case x.v == nil:
		case *x.v == "":
			d += "; " + x.name + " removed"
		default:
			d += "; " + x.name + " replaced"
		}
	}
	return d
}

// MoveStore copies every page, the manifest and the excerpts from the
// store the wiki is kept in to another, and keeps it there from then on.
func (s *Service) MoveStore(ctx context.Context, c Caller, in StoreInput) (int, error) {
	if !c.SuperAdmin {
		return 0, wrapf(ErrDenied, "only a SuperAdmin moves the wiki")
	}
	if err := s.leads(); err != nil {
		return 0, err
	}
	if runs, err := s.Runs(ctx, false); err == nil && len(runs) > 0 {
		return 0, invalid("%d runs wait for review: approve or reject them before the wiki moves", len(runs))
	}
	from, _, err := s.currentStore(ctx)
	if err != nil {
		return 0, err
	}
	to, st, err := s.storeWith(ctx, in)
	if err != nil {
		return 0, err
	}
	files, err := from.Pull(ctx)
	if err != nil {
		return 0, fmt.Errorf("reading the wiki where it is: %w", err)
	}
	var all []Change
	for p, f := range files {
		all = append(all, Change{Path: p, Body: f.Body})
	}
	if err := to.Write(ctx, all, fmt.Sprintf("The wiki moves here from %s: %d files", from.Kind(), len(all))); err != nil {
		return 0, fmt.Errorf("copying the wiki: %w", err)
	}
	if _, err := s.SaveStore(ctx, c, StoreInput{Store: st, Keys: in.Keys}); err != nil {
		return len(all), err
	}
	s.audit(audit.Event{Action: ActionStore, Who: c.who(), Outcome: audit.OutcomeOK, From: c.From, Detail: fmt.Sprintf("the wiki moved from %s to %s: %d files copied", from.Kind(), st.Kind, len(all))})
	return len(all), nil
}

// SaveHookToken sets the token pushes and bucket notifications carry, or
// removes it ("").
func (s *Service) SaveHookToken(ctx context.Context, c Caller, token string) error {
	if !c.SuperAdmin {
		return wrapf(ErrDenied, "only a SuperAdmin sets the webhook's token")
	}
	if token != "" && len(token) < 16 {
		return invalid("the webhook's token is 16 characters or more")
	}
	if err := s.saveCreds(ctx, hookKey, CredsChange{Token: &token}); err != nil {
		return err
	}
	s.audit(audit.Event{Action: ActionHook, Who: c.who(), Outcome: audit.OutcomeOK, From: c.From, Detail: map[bool]string{true: "the knowledge webhook's token removed", false: "the knowledge webhook's token replaced"}[token == ""]})
	return nil
}

// HookAuth says whether a webhook's request carries the token: as
// GitHub's HMAC signature, GitLab's token header, or a bearer token
// (Gitea, MinIO and others).
func (s *Service) HookAuth(ctx context.Context, header func(string) string, body []byte) bool {
	c, err := s.creds(ctx, hookKey)
	if err != nil || c.Token == "" {
		return false
	}
	if sig := header("X-Hub-Signature-256"); sig != "" {
		mac := hmac.New(sha256.New, []byte(c.Token))
		mac.Write(body)
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		return hmac.Equal([]byte(sig), []byte(want))
	}
	for _, got := range []string{header("X-Gitlab-Token"), strings.TrimPrefix(header("Authorization"), "Bearer ")} {
		if got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(c.Token)) == 1 {
			return true
		}
	}
	return false
}

// Hook is a push or a bucket notification: "wiki" reads the local copy
// again; a source's id syncs that source.
func (s *Service) Hook(ctx context.Context, target string) error {
	if target == "wiki" {
		return s.Refresh(ctx)
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	x, ok := set.source(target)
	if !ok || x.Kind != SourceGit {
		return wrapf(ErrNotFound, "no git source %q", target)
	}
	s.audit(audit.Event{Action: ActionHook, Who: audit.Who{Name: "webhook"}, Target: target, Outcome: audit.OutcomeOK, Detail: "a push to " + x.Name + " starts a sync of it"})
	go func() {
		_, _ = s.SyncWith(context.WithoutCancel(ctx), SyncRequest{Source: target, By: "a push to " + x.Name, Who: audit.Who{Name: "webhook"}})
	}()
	return nil
}

// TestSource reads a source's listing as saved, without reading items.
func (s *Service) TestSource(ctx context.Context, c Caller, id string) (int, error) {
	if !c.SuperAdmin {
		return 0, wrapf(ErrDenied, "only a SuperAdmin tests Mycroft's sources")
	}
	if err := s.leads(); err != nil {
		return 0, err
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return 0, err
	}
	x, ok := set.source(id)
	if !ok {
		return 0, wrapf(ErrNotFound, "no source %q", id)
	}
	rd, err := s.readerFor(ctx, x)
	if err != nil {
		return 0, err
	}
	tctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	items, _, err := rd.List(tctx, time.Time{})
	return len(items), err
}
