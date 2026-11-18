package knowledge

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/url"
	"reflect"
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
	// SourceS3 is a bucket of documents (Phase 12, KN-25).
	SourceS3 = "s3"
	// SourceWeb is a web site, crawled from a start address (0.12.1,
	// KN-44, websource.go).
	SourceWeb = "web"
)

// ConsoleSourceID is the console's own source, there from the start.
const ConsoleSourceID = "console"

// AddedSourceID is the source of what people add in Mycroft (Administration › AI ›
// Knowledge's Added in Mycroft card, MY-11):
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
	// Repository names the git repository of Administration › Settings › Repositories the
	// source reads (0.12.1, RP-03): URL and Credential are the
	// repository's then, read from the list (gitrepos.go).
	Repository string `json:"repository,omitempty"`
	// A bucket of documents (KN-25): its name, the prefix to read under
	// ("" for all), its region, its endpoint ("" for AWS) and path-style
	// addressing for MinIO or another S3-compatible store, and its
	// credentials: CredentialsKeys (a read-only access key, in the Secret)
	// or CredentialsIdentity (the pod's).
	Bucket      string `json:"bucket,omitempty"`
	Prefix      string `json:"prefix,omitempty"`
	Region      string `json:"region,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
	PathStyle   bool   `json:"pathStyle,omitempty"`
	Credentials string `json:"credentials,omitempty"`
	// A live source (0.12.1, KN-39): its addresses, never copied, read
	// when an agent looks them up, and how it signs in: Auth is "",
	// LiveAuthBearer (the token in the Secret) or LiveAuthBasic (Username
	// and the token in the Secret as the password or API token).
	Addresses []LiveAddress `json:"addresses,omitempty"`
	Auth      string        `json:"auth,omitempty"`
	// A web site (0.12.1, KN-44): its start address is URL, and Depth and
	// Pages how many links away and how many pages a read goes; it signs
	// in as a live source does (Auth, Username, the token in the Secret).
	Depth int `json:"depth,omitempty"`
	Pages int `json:"pages,omitempty"`
	// Excluded are the items of a web site, Confluence space, repository
	// or bucket that are never read (0.12.1, KN-45, exclude.go): set from
	// AIOps › Mycroft › Sources, kept when the source is edited.
	Excluded []Exclusion `json:"excluded,omitempty"`
	// MigratedFrom are the web pages of Added in Mycroft this web site was
	// made from at the upgrade (KN-46): their excerpts stay until its first
	// read reads them again.
	MigratedFrom []string `json:"migratedFrom,omitempty"`
	// CAPEM is a private CA for the source's TLS.
	CAPEM   string    `json:"caPEM,omitempty"`
	Added   time.Time `json:"added,omitzero"`
	AddedBy string    `json:"addedBy,omitempty"`
	// Credential is the credential of Administration › Credentials a Confluence
	// space, repository, bucket, web site or live source signs in with, by
	// id; "" is none, or a bucket's pod identity (0.12.1, CR-11).
	Credential string `json:"credential,omitempty"`
	// CredMigrated says the source names its credential: the one it kept
	// in the knowledge Secret before 0.12.1 was moved to Administration ›
	// Credentials, or it had none (CR-12). Its Auth and Username, and that
	// kept credential, are not read again.
	CredMigrated bool `json:"credMigrated,omitempty"`
	// cert is a web site's or live source's client certificate, from its
	// credential, as it is read; never kept.
	cert *tls.Certificate
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
	// Migrated are the one-time migrations done (KN-46).
	Migrated []string `json:"migrated,omitempty"`
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
	// What people add in Administration › AI › Knowledge's Added in Mycroft card (MY-04, MY-11).
	if !slices.ContainsFunc(s.Sources, func(x Source) bool { return x.ID == AddedSourceID }) {
		s.Sources = append(s.Sources, Source{ID: AddedSourceID, Name: "Added in Mycroft", Kind: SourceUpload})
	}
	// The fixes proven in cases, offered for a runbook (KN-34).
	if !slices.ContainsFunc(s.Sources, func(x Source) bool { return x.ID == FixesSourceID }) {
		s.Sources = append(s.Sources, Source{ID: FixesSourceID, Name: "Fixes", Kind: SourceFixes})
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
		// A credential named gives the account's email (CR-11); a token
		// of its own needs it here.
		if x.Flavor == confluence.Cloud && !x.CredMigrated && x.Credential == "" && !strings.Contains(x.Email, "@") {
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
	case SourceS3:
		if err := checkBucket(x.Bucket, x.Region, x.Endpoint, x.Prefix); err != nil {
			return err
		}
		if x.Credentials != CredentialsKeys && x.Credentials != CredentialsIdentity {
			return invalid("a bucket's credentials are an access key or the pod's identity")
		}
	case SourceLive:
		if x.CredMigrated {
			x.Auth, x.Username = "", ""
		}
		return x.validateLive()
	case SourceWeb:
		if x.CredMigrated {
			x.Auth, x.Username = "", ""
		}
		return x.validateWeb()
	case SourceUpload, SourceConsole, SourceFixes:
	default:
		return invalid("a source is confluence, git, s3, web, live, upload or console")
	}
	return nil
}

var (
	bucketPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{1,62}$`)
	regionPattern = regexp.MustCompile(`^[a-z0-9-]{0,40}$`)
)

// checkBucket checks a bucket of documents' name, region, endpoint and
// prefix (KN-25).
func checkBucket(bucket, region, endpoint, prefix string) error {
	if !bucketPattern.MatchString(bucket) {
		return invalid("give the bucket's name: letters, digits, dots and dashes, up to 63")
	}
	if !regionPattern.MatchString(region) {
		return invalid("the region is a name such as eu-west-1")
	}
	if endpoint != "" {
		if err := httpsURL("the endpoint", endpoint, true); err != nil {
			return err
		}
	}
	if strings.HasPrefix(prefix, "/") || strings.Contains(prefix, "..") || len(prefix) > 200 {
		return invalid("the prefix is a path such as runbooks/")
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

// Settings reads the settings, the git sources that name a repository of
// Administration › Settings › Repositories with its address and credential (RP-03).
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	set, err := s.rawSettings(ctx)
	if err != nil {
		return Settings{}, err
	}
	return s.withRepos(ctx, set)
}

// rawSettings are the settings as kept.
func (s *Service) rawSettings(ctx context.Context) (Settings, error) {
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
	return s.SaveSourceWith(ctx, c, x, CredsChange{Token: token}, create)
}

// normalized is a source as it is saved and tested: its fields trimmed,
// a repository's branch main by default, a bucket's prefix ending in a
// slash and its credentials an access key by default.
func (x Source) normalized() Source {
	x.Name, x.BaseURL, x.URL, x.Space = strings.TrimSpace(x.Name), strings.TrimRight(strings.TrimSpace(x.BaseURL), "/"), strings.TrimSpace(x.URL), strings.TrimSpace(x.Space)
	if x.Kind == SourceGit && x.Branch == "" {
		x.Branch = "main"
	}
	if x.Kind == SourceLive {
		x.Addresses, x.Username = normalizedLive(x.Addresses), strings.TrimSpace(x.Username)
		if x.Auth != LiveAuthBasic {
			x.Username = ""
		}
	} else {
		x.Addresses = nil
		if x.Kind != SourceWeb {
			x.Auth = ""
		}
	}
	if x.Kind == SourceWeb {
		x.Username = strings.TrimSpace(x.Username)
		if x.Auth != LiveAuthBasic {
			x.Username = ""
		}
		if x.Pages == 0 {
			x.Pages = DefaultCrawlPages
		}
	} else {
		x.Depth, x.Pages, x.MigratedFrom = 0, 0, nil
	}
	if !excludable(x.Kind) {
		x.Excluded = nil
	}
	if x.Kind == SourceS3 {
		x.Bucket, x.Region, x.Prefix = strings.TrimSpace(x.Bucket), strings.TrimSpace(x.Region), strings.TrimSpace(x.Prefix)
		x.Endpoint = strings.TrimRight(strings.TrimSpace(x.Endpoint), "/")
		if x.Prefix != "" && !strings.HasSuffix(x.Prefix, "/") {
			x.Prefix += "/"
		}
		if x.Credentials == "" {
			x.Credentials = CredentialsKeys
		}
	}
	return x
}

// SaveSourceWith is SaveSource with any credential: a token, or a
// bucket's access key (KN-25).
func (s *Service) SaveSourceWith(ctx context.Context, c Caller, x Source, ch CredsChange, create bool) (Source, error) {
	if !c.SuperAdmin {
		return Source{}, wrapf(ErrDenied, "only a SuperAdmin changes Mycroft's sources")
	}
	if err := s.leads(); err != nil {
		return Source{}, err
	}
	x, err := s.fillRepo(ctx, x)
	if err != nil {
		return Source{}, err
	}
	x = x.normalized()
	if err := x.Validate(); err != nil {
		return Source{}, err
	}
	if s.keepsOwnCredential(ctx, x, ch, create) {
		// A credential of its own, kept in the knowledge Secret as before
		// 0.12.1: typed through the Go API, or kept by a source the
		// upgrade has not switched, edited without naming one. The
		// upgrade moves it (CR-12).
		x.CredMigrated = false
		if x.Kind == SourceS3 && x.Credentials == CredentialsKeys {
			have, err := s.creds(ctx, sourceKey(x.ID))
			if err != nil {
				return Source{}, err
			}
			if k := ch.apply(have); k.AccessKeyID == "" || k.SecretAccessKey == "" {
				return Source{}, invalid("an access key is needed: give its ID and secret, or use the pod's identity")
			}
		}
	} else {
		// A source saved names its credential from then on (CR-11),
		// checked against what it serves and the addresses it covers.
		ch = CredsChange{}
		x.CredMigrated = true
		if !signsIn(x.Kind) {
			x.Credential = ""
		}
		if x.Kind == SourceWeb || x.Kind == SourceLive {
			x.Auth, x.Username = "", ""
		}
		if x.Kind == SourceS3 && x.Credentials == CredentialsKeys && x.Credential == "" {
			return Source{}, invalid("choose the access key's credential, or use the pod's identity")
		}
		if x.Kind == SourceS3 && x.Credentials == CredentialsIdentity {
			x.Credential = ""
		}
		if err := s.checkCredential(ctx, x); err != nil {
			return Source{}, err
		}
	}
	if create && (x.Kind == SourceConsole || x.Kind == SourceUpload || x.Kind == SourceFixes) {
		return Source{}, invalid("the console, the uploaded documents and the fixes are there already")
	}
	var saved Source
	// renamedOnly says an edit changed the name alone: its job is renamed,
	// and the source is not read again for it.
	renamedOnly := false
	_, err = s.editSettings(ctx, func(set *Settings) error {
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
			// Exclusions are made in AIOps › Mycroft › Sources only.
			x.Excluded, x.MigratedFrom = nil, nil
			set.Sources = append(set.Sources, x)
		} else {
			old := set.Sources[i]
			if old.Kind != x.Kind {
				return invalid("a source keeps its kind; add a new one instead")
			}
			x.Added, x.AddedBy = old.Added, old.AddedBy
			// The dialog does not hold the exclusions: they stay as they are.
			x.Excluded, x.MigratedFrom = old.Excluded, old.MigratedFrom
			same := old
			same.Name, same.CredMigrated = x.Name, x.CredMigrated
			renamedOnly = reflect.DeepEqual(same, x) && ch.none()
			set.Sources[i] = x
		}
		saved = x
		return nil
	})
	if err != nil {
		return Source{}, err
	}
	detail := fmt.Sprintf("source %s (%s) %s", x.ID, x.Kind, map[bool]string{true: "added", false: "changed"}[create])
	if x.Kind == SourceS3 {
		detail += fmt.Sprintf(": the bucket %s under %q, credentials %s", x.Bucket, x.Prefix, x.Credentials)
	}
	if x.Credential != "" {
		detail += "; credential " + x.Credential
	}
	if x.Kind == SourceLive {
		detail += fmt.Sprintf(": live, %d addresses, never copied", len(x.Addresses))
		s.live.forget(x.ID)
	}
	if !ch.none() {
		if err := s.saveCreds(ctx, sourceKey(x.ID), ch); err != nil {
			return saved, err
		}
		// Which parts changed, never their values.
		for _, p := range []struct {
			v    *string
			name string
		}{{ch.Token, "token"}, {ch.AccessKeyID, "access key ID"}, {ch.SecretAccessKey, "secret access key"}} {
			if p.v != nil {
				detail += map[bool]string{true: "; its " + p.name + " removed", false: "; its " + p.name + " replaced"}[*p.v == ""]
			}
		}
	}
	s.audit(audit.Event{Action: ActionSource, Who: c.who(), Target: x.ID, Detail: detail, Outcome: audit.OutcomeOK, From: c.From})
	s.sourceSaved(ctx, c, x, create, !renamedOnly)
	return saved, nil
}

// keepsOwnCredential says a source saved keeps a credential of its own in
// the knowledge Secret, as before 0.12.1, rather than naming one of
// Administration › Credentials: one typed with it, or, for a source not switched
// yet edited without naming one, the one it keeps.
func (s *Service) keepsOwnCredential(ctx context.Context, x Source, ch CredsChange, create bool) bool {
	if x.Credential != "" || !signsIn(x.Kind) || x.namesRepo() {
		return false
	}
	if !ch.none() {
		return true
	}
	if create {
		return false
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return false
	}
	old, ok := set.source(x.ID)
	return ok && !old.CredMigrated && old.Credential == ""
}

// firstRead reads a source just added, in the background, once the wiki
// has a store (Mycroft's first import, GS-11): the inventory it describes
// becomes proposals in AIOps › Inbox at once. Without a store the source
// waits for the first sync after one is chosen. With the jobs, the read
// is a run of the source's Wiki sync job instead (KN-28, sourceSaved).
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

// Wait waits for the first reads, the reads of what was uploaded or
// added, and those after a new store or a push, started in the
// background (tests).
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
	if id == ConsoleSourceID || id == FixesSourceID {
		return invalid("the console's own sources can be switched off, not removed")
	}
	live := false
	_, err := s.editSettings(ctx, func(set *Settings) error {
		i := slices.IndexFunc(set.Sources, func(o Source) bool { return o.ID == id })
		if i < 0 {
			return wrapf(ErrNotFound, "no source %q", id)
		}
		if set.Sources[i].Kind == SourceUpload {
			return invalid("remove the documents one by one instead")
		}
		live = set.Sources[i].Kind == SourceLive
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
	s.live.forget(id)
	if !live {
		s.sourceRemoved(ctx, c, id)
	}
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
	live := false
	_, err := s.editSettings(ctx, func(set *Settings) error {
		i := slices.IndexFunc(set.Sources, func(o Source) bool { return o.ID == id })
		if i < 0 {
			return wrapf(ErrNotFound, "no source %q", id)
		}
		set.Sources[i].Off = !on
		live = set.Sources[i].Kind == SourceLive
		return nil
	})
	if err == nil {
		s.audit(audit.Event{Action: ActionSource, Who: c.who(), Target: id, Detail: "source " + id + map[bool]string{true: " switched on", false: " switched off"}[on], Outcome: audit.OutcomeOK, From: c.From})
		if !live {
			s.sourceSwitched(ctx, c, id, on)
		}
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
