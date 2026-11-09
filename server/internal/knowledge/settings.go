package knowledge

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/confluence"
	"github.com/ucesys/221b/server/internal/s3"
)

// The kinds of source.
const (
	SourceConfluence = "confluence"
	SourceGit        = "git"
	SourceUpload     = "upload"
	SourceConsole    = "console"
)

// ConsoleSourceID is the console's own source, there from the start.
const ConsoleSourceID = "console"

// AddedSourceID is the source of what people add in Cases › Mycroft:
// notes, web pages read from an address, and documents (MY-04); a source
// of uploaded documents, there from the start.
const AddedSourceID = "added"

// Source is one place Mycroft reads. Its credential is in the Secret,
// never here.
type Source struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Off sources are not read; their pages stay and grow stale.
	Off bool `json:"off,omitempty"`
	// A Confluence space: the site, Cloud or Data Center, the account's
	// email (Cloud) and the space's key. Its read-only token is in the
	// Secret.
	BaseURL string `json:"baseURL,omitempty"`
	Flavor  string `json:"flavor,omitempty"`
	Email   string `json:"email,omitempty"`
	Space   string `json:"space,omitempty"`
	// A git repository of runbooks: its HTTPS address, branch, the
	// directory to read ("" for all) and the user the token goes with.
	URL      string `json:"url,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Path     string `json:"path,omitempty"`
	Username string `json:"username,omitempty"`
	// CAPEM is a private CA for the source's TLS.
	CAPEM   string    `json:"caPEM,omitempty"`
	Added   time.Time `json:"added,omitzero"`
	AddedBy string    `json:"addedBy,omitempty"`
}

// Store kinds.
const (
	StoreGit = "git"
	StoreS3  = "s3"
	// StoreConfigMap is the temporary store in the ConfigMap <release>-wiki
	// (CF-57): the default while the settings name none, for trying the
	// wiki; it will be removed.
	StoreConfigMap = "configmap"
)

// S3 credentials.
const (
	CredentialsKeys     = "keys"
	CredentialsIdentity = "identity"
)

// StoreSettings say where the wiki is kept.
type StoreSettings struct {
	// Kind is StoreGit, StoreS3 or StoreConfigMap; "" is none yet, which
	// is the ConfigMap store when the chart made its ConfigMap.
	Kind string `json:"kind"`
	// A git repository: its HTTPS address, branch and the user its token
	// goes with.
	URL      string `json:"url,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Username string `json:"username,omitempty"`
	// A bucket: its endpoint ("" for AWS), region, name, the prefix of
	// the wiki's keys, path-style addressing, its credentials (keys in
	// the Secret, or the pod's identity) and server-side encryption.
	Endpoint    string `json:"endpoint,omitempty"`
	Region      string `json:"region,omitempty"`
	Bucket      string `json:"bucket,omitempty"`
	Prefix      string `json:"prefix,omitempty"`
	PathStyle   bool   `json:"pathStyle,omitempty"`
	Credentials string `json:"credentials,omitempty"`
	SSE         string `json:"sse,omitempty"`
	KMSKeyID    string `json:"kmsKeyId,omitempty"`
	// CAPEM is a private CA for the store's TLS.
	CAPEM string `json:"caPEM,omitempty"`
	// RefreshMinutes is how often the local copy is read again; 0 is the
	// default (15 for git, 5 for S3).
	RefreshMinutes int       `json:"refreshMinutes,omitempty"`
	SavedBy        string    `json:"savedBy,omitempty"`
	SavedAt        time.Time `json:"savedAt,omitzero"`
}

// Settings are the knowledge's settings.
type Settings struct {
	Sources []Source      `json:"sources"`
	Store   StoreSettings `json:"store"`
	// StaleDays is how long a page's sources may go unseen before it is
	// marked stale (default 30).
	StaleDays int `json:"staleDays,omitempty"`
}

// DefaultStaleDays is when a page is marked stale.
const DefaultStaleDays = 30

// MaxSources bounds the sources.
const MaxSources = 50

func (s Settings) staleDays() int {
	if s.StaleDays <= 0 {
		return DefaultStaleDays
	}
	return s.StaleDays
}

// withDefaults adds the console's own source and the uploaded documents
// when missing, so a new installation lists them.
func (s Settings) withDefaults() Settings {
	if !slices.ContainsFunc(s.Sources, func(x Source) bool { return x.ID == ConsoleSourceID }) {
		s.Sources = append([]Source{{ID: ConsoleSourceID, Name: "This console", Kind: SourceConsole}}, s.Sources...)
	}
	if !slices.ContainsFunc(s.Sources, func(x Source) bool { return x.Kind == SourceUpload }) {
		s.Sources = append(s.Sources, Source{ID: "documents", Name: "Uploaded documents", Kind: SourceUpload})
	}
	// --- Mycroft tab (Phase 12, wave 5) ---
	// What people add in Cases › Mycroft › Wiki › Add (MY-04).
	if !slices.ContainsFunc(s.Sources, func(x Source) bool { return x.ID == AddedSourceID }) {
		s.Sources = append(s.Sources, Source{ID: AddedSourceID, Name: "Added in Mycroft", Kind: SourceUpload})
	}
	return s
}

func (s Settings) source(id string) (Source, bool) {
	for _, x := range s.Sources {
		if x.ID == id {
			return x, true
		}
	}
	return Source{}, false
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// httpsURL checks a web address the console reaches out to.
func httpsURL(what, s string, allowHTTP bool) error {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(allowHTTP && u.Scheme == "http")) || u.User != nil {
		if allowHTTP {
			return invalid("%s must be an http or https address with no password in it", what)
		}
		return invalid("%s must be an https address with no password in it", what)
	}
	return nil
}

// checkCA holds a private CA bundle to PEM and its size.
func checkCA(pem string) error {
	if strings.TrimSpace(pem) == "" {
		return nil
	}
	if len(pem) > 12<<10 {
		return invalid("the private CA bundle is at most 12 KiB")
	}
	if !strings.Contains(pem, "-----BEGIN CERTIFICATE-----") {
		return invalid("the private CA is not a PEM certificate")
	}
	return nil
}

// Validate checks a source as a SuperAdmin saves it.
func (x Source) Validate() error {
	if !idPattern.MatchString(x.ID) {
		return invalid("a source's id is lower-case letters, digits and dashes, up to 40")
	}
	if strings.TrimSpace(x.Name) == "" || len(x.Name) > 120 {
		return invalid("give the source a name of up to 120 characters")
	}
	if err := checkCA(x.CAPEM); err != nil {
		return err
	}
	switch x.Kind {
	case SourceConfluence:
		if err := httpsURL("the Confluence address", x.BaseURL, false); err != nil {
			return err
		}
		if x.Flavor != confluence.Cloud && x.Flavor != confluence.DataCenter {
			return invalid("Confluence is cloud or datacenter")
		}
		if x.Flavor == confluence.Cloud && !strings.Contains(x.Email, "@") {
			return invalid("Confluence Cloud needs the email of the account the API token belongs to")
		}
		if !regexp.MustCompile(`^~?[A-Za-z0-9_]{1,255}$`).MatchString(x.Space) {
			return invalid("give the space's key, such as INFRA")
		}
	case SourceGit:
		if err := httpsURL("the repository's address", x.URL, false); err != nil {
			return err
		}
		if strings.Contains(x.Path, "..") || strings.HasPrefix(x.Path, "/") {
			return invalid("the directory is a path inside the repository")
		}
	case SourceUpload, SourceConsole:
	default:
		return invalid("a source is confluence, git, upload or console")
	}
	return nil
}

// Validate checks where the wiki is kept.
func (st StoreSettings) Validate() error {
	if err := checkCA(st.CAPEM); err != nil {
		return err
	}
	if st.RefreshMinutes < 0 || st.RefreshMinutes > 24*60 {
		return invalid("the refresh is between 1 minute and a day")
	}
	switch st.Kind {
	case "", StoreConfigMap:
	case StoreGit:
		if err := httpsURL("the repository's address", st.URL, false); err != nil {
			return err
		}
	case StoreS3:
		if st.Bucket == "" || len(st.Bucket) > 63 {
			return invalid("give the bucket's name")
		}
		if st.Endpoint != "" {
			if err := httpsURL("the endpoint", st.Endpoint, true); err != nil {
				return err
			}
		}
		if strings.HasPrefix(st.Prefix, "/") || strings.Contains(st.Prefix, "..") || len(st.Prefix) > 200 {
			return invalid("the prefix is a path such as wiki/")
		}
		if st.Credentials != CredentialsKeys && st.Credentials != CredentialsIdentity {
			return invalid("a bucket's credentials are an access key or the pod's identity")
		}
		switch st.SSE {
		case "", s3.SSES3, s3.SSEKMS:
		default:
			return invalid("server-side encryption is AES256 (SSE-S3) or aws:kms (SSE-KMS)")
		}
		if st.SSE != s3.SSEKMS && st.KMSKeyID != "" {
			return invalid("a KMS key goes with SSE-KMS")
		}
	default:
		return invalid("the wiki is kept in git, s3 or configmap")
	}
	return nil
}

// normalized trims what a person typed.
func (st StoreSettings) normalized() StoreSettings {
	st.URL, st.Endpoint, st.Bucket = strings.TrimSpace(st.URL), strings.TrimRight(strings.TrimSpace(st.Endpoint), "/"), strings.TrimSpace(st.Bucket)
	if st.Kind == StoreGit && st.Branch == "" {
		st.Branch = "main"
	}
	if st.Kind == StoreS3 {
		if st.Prefix != "" && !strings.HasSuffix(st.Prefix, "/") {
			st.Prefix += "/"
		}
		if st.Credentials == "" {
			st.Credentials = CredentialsKeys
		}
	}
	return st
}

// fingerprint tells one store's settings from another's, credentials
// apart, for when the client must be made again.
func (st StoreSettings) fingerprint() string {
	st.SavedBy, st.SavedAt = "", time.Time{}
	return fmt.Sprintf("%+v", st)
}

// Settings reads the settings.
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	var set Settings
	if _, err := load(ctx, s.kv, SettingsKey, &set); err != nil {
		return Settings{}, err
	}
	return set.withDefaults(), nil
}

func (s *Service) editSettings(ctx context.Context, fn func(*Settings) error) (Settings, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	set, err := update(ctx, s.kv, SettingsKey, s.limits.SettingsBytes, nil, func(v *Settings) error {
		*v = v.withDefaults()
		return fn(v)
	})
	return set, err
}

// SaveSource adds a source (create) or changes one, with its credential:
// a value replaces it, "" removes it, nil keeps it.
func (s *Service) SaveSource(ctx context.Context, c Caller, x Source, token *string, create bool) (Source, error) {
	if !c.SuperAdmin {
		return Source{}, wrapf(ErrDenied, "only a SuperAdmin changes Mycroft's sources")
	}
	if err := s.leads(); err != nil {
		return Source{}, err
	}
	x.Name, x.BaseURL, x.URL, x.Space = strings.TrimSpace(x.Name), strings.TrimRight(strings.TrimSpace(x.BaseURL), "/"), strings.TrimSpace(x.URL), strings.TrimSpace(x.Space)
	if x.Kind == SourceGit && x.Branch == "" {
		x.Branch = "main"
	}
	if err := x.Validate(); err != nil {
		return Source{}, err
	}
	if create && (x.Kind == SourceConsole || x.Kind == SourceUpload) {
		return Source{}, invalid("the console and the uploaded documents are there already")
	}
	var saved Source
	_, err := s.editSettings(ctx, func(set *Settings) error {
		i := slices.IndexFunc(set.Sources, func(o Source) bool { return o.ID == x.ID })
		switch {
		case create && i >= 0:
			return invalid("there is a source %q already", x.ID)
		case !create && i < 0:
			return wrapf(ErrNotFound, "no source %q", x.ID)
		case create && len(set.Sources) >= MaxSources:
			return invalid("at most %d sources", MaxSources)
		}
		if create {
			x.Added, x.AddedBy = s.o.Now().UTC(), c.label()
			set.Sources = append(set.Sources, x)
		} else {
			old := set.Sources[i]
			if old.Kind != x.Kind {
				return invalid("a source keeps its kind; add a new one instead")
			}
			x.Added, x.AddedBy = old.Added, old.AddedBy
			set.Sources[i] = x
		}
		saved = x
		return nil
	})
	if err != nil {
		return Source{}, err
	}
	detail := fmt.Sprintf("source %s (%s) %s", x.ID, x.Kind, map[bool]string{true: "added", false: "changed"}[create])
	if token != nil {
		if err := s.saveCreds(ctx, sourceKey(x.ID), CredsChange{Token: token}); err != nil {
			return saved, err
		}
		detail += map[bool]string{true: "; its token removed", false: "; its token replaced"}[*token == ""]
	}
	s.audit(audit.Event{Action: ActionSource, Who: c.who(), Target: x.ID, Detail: detail, Outcome: audit.OutcomeOK, From: c.From})
	if create && s.o.ReadNew && !x.Off {
		s.firstRead(ctx, c, x)
	}
	return saved, nil
}

// firstRead reads a source just added, in the background, once the wiki
// has a store (Mycroft's first import, GS-11): the inventory it describes
// becomes proposals in Cases › Inbox at once. Without a store the source
// waits for the first sync after one is chosen.
func (s *Service) firstRead(ctx context.Context, c Caller, x Source) {
	if _, _, err := s.currentStore(ctx); err != nil {
		return
	}
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		ctx := context.WithoutCancel(ctx)
		for {
			_, err := s.SyncWith(ctx, SyncRequest{Source: x.ID, By: "first read of " + x.Name + " by " + c.label(), Who: c.who()})
			if err != ErrBusy {
				if err != nil {
					s.o.Logger.Warn("the first read of a new source failed; the next sync reads it", "source", x.ID, "err", err)
				}
				return
			}
			// Another sync runs: this one follows it.
			s.syncMu.Lock()
			s.syncMu.Unlock()
		}
	}()
}

// Wait waits for the first reads, and the reads of what was uploaded or
// added, started in the background (tests).
func (s *Service) Wait() { s.bg.Wait() }

// RemoveSource removes a source and its credential. Its pages stay, and
// grow stale; its excerpts leave the index at the next sync.
func (s *Service) RemoveSource(ctx context.Context, c Caller, id string) error {
	if !c.SuperAdmin {
		return wrapf(ErrDenied, "only a SuperAdmin changes Mycroft's sources")
	}
	if err := s.leads(); err != nil {
		return err
	}
	if id == ConsoleSourceID {
		return invalid("the console's own source can be switched off, not removed")
	}
	_, err := s.editSettings(ctx, func(set *Settings) error {
		i := slices.IndexFunc(set.Sources, func(o Source) bool { return o.ID == id })
		if i < 0 {
			return wrapf(ErrNotFound, "no source %q", id)
		}
		if set.Sources[i].Kind == SourceUpload {
			return invalid("remove the documents one by one instead")
		}
		set.Sources = slices.Delete(set.Sources, i, i+1)
		return nil
	})
	if err != nil {
		return err
	}
	if err := s.dropCreds(ctx, sourceKey(id)); err != nil {
		s.o.Logger.Warn("a removed source's token stays in the Secret", "source", id, "err", err)
	}
	s.audit(audit.Event{Action: ActionSource, Who: c.who(), Target: id, Detail: "source " + id + " removed", Outcome: audit.OutcomeOK, From: c.From})
	return nil
}

// SetSourceOn switches a source on or off.
func (s *Service) SetSourceOn(ctx context.Context, c Caller, id string, on bool) error {
	if !c.SuperAdmin {
		return wrapf(ErrDenied, "only a SuperAdmin changes Mycroft's sources")
	}
	if err := s.leads(); err != nil {
		return err
	}
	_, err := s.editSettings(ctx, func(set *Settings) error {
		i := slices.IndexFunc(set.Sources, func(o Source) bool { return o.ID == id })
		if i < 0 {
			return wrapf(ErrNotFound, "no source %q", id)
		}
		set.Sources[i].Off = !on
		return nil
	})
	if err == nil {
		s.audit(audit.Event{Action: ActionSource, Who: c.who(), Target: id, Detail: "source " + id + map[bool]string{true: " switched on", false: " switched off"}[on], Outcome: audit.OutcomeOK, From: c.From})
	}
	return err
}

// SetStaleDays changes when pages are marked stale.
func (s *Service) SetStaleDays(ctx context.Context, c Caller, days int) error {
	if !c.SuperAdmin {
		return wrapf(ErrDenied, "only a SuperAdmin changes the knowledge settings")
	}
	if days < 1 || days > 365 {
		return invalid("a page is marked stale after 1 to 365 days")
	}
	_, err := s.editSettings(ctx, func(set *Settings) error { set.StaleDays = days; return nil })
	if err == nil {
		s.audit(audit.Event{Action: ActionStore, Who: c.who(), Detail: fmt.Sprintf("pages marked stale after %d days unseen", days), Outcome: audit.OutcomeOK, From: c.From})
		s.reindex()
	}
	return err
}
