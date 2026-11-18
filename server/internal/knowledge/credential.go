package knowledge

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/ucesys/221b/server/internal/confluence"
	"github.com/ucesys/221b/server/internal/credentials"
)

// --- Credentials (0.12.1, CR-11, CR-12) ---
//
// A Confluence space, a git repository, a bucket, a web site and a live
// source sign in with a credential of Administration › Credentials, named by id
// (Source.Credential), resolved for the source's address at each read and
// test and never sent outside its endpoint. A source the upgrade has not
// switched yet reads the credential it kept in the knowledge Secret, as
// before; once switched (Source.CredMigrated) that one is not read again.

// credentialUse is what a source uses a credential for.
func credentialUse(x Source) string {
	switch x.Kind {
	case SourceGit:
		return credentials.UseGit
	case SourceS3:
		return credentials.UseS3
	case SourceConfluence:
		if x.Flavor == confluence.DataCenter {
			return credentials.UseConfluenceDC
		}
		return credentials.UseConfluenceCloud
	case SourceWeb, SourceLive:
		return credentials.UseHTTP
	}
	return ""
}

// credentialAddress is the address a source's credential must cover: the
// site, the repository, the bucket, the web site's start, a live source's
// first address (each address is checked again when it is read).
func credentialAddress(x Source) string {
	switch x.Kind {
	case SourceConfluence:
		return x.BaseURL
	case SourceGit, SourceWeb:
		return x.URL
	case SourceS3:
		return credentials.S3Address(x.Endpoint, x.Bucket)
	case SourceLive:
		if len(x.Addresses) > 0 {
			return x.Addresses[0].URL
		}
	}
	return ""
}

// signsIn says whether a kind of source signs in with a credential.
func signsIn(kind string) bool {
	return kind == SourceConfluence || kind == SourceGit || kind == SourceS3 || kind == SourceWeb || kind == SourceLive
}

// checkCredential checks the credential a source names: there, serving
// the source's kind, covering each of its addresses.
func (s *Service) checkCredential(ctx context.Context, x Source) error {
	if x.Credential == "" || !signsIn(x.Kind) {
		return nil
	}
	if s.o.Credentials == nil {
		return invalid("this console keeps no credentials")
	}
	addrs := []string{credentialAddress(x)}
	if x.Kind == SourceLive {
		addrs = addrs[:0]
		for _, a := range x.Addresses {
			addrs = append(addrs, a.URL)
		}
	}
	for _, a := range addrs {
		if _, err := s.o.Credentials.CheckChoice(ctx, x.Credential, credentialUse(x), a); err != nil {
			return invalid("%s", strings.TrimPrefix(err.Error(), credentials.ErrInvalid.Error()+": "))
		}
	}
	return nil
}

// sourceAuth is a source as it is read, with its credential: the one it
// names, resolved for its address, its sign-in filled in from it; the one
// kept in the knowledge Secret for a source not switched yet; none.
func (s *Service) sourceAuth(ctx context.Context, x Source) (Source, Creds, error) {
	if !signsIn(x.Kind) {
		return x, Creds{}, nil
	}
	if !x.CredMigrated && x.Credential == "" {
		c, err := s.creds(ctx, sourceKey(x.ID))
		return x, c, err
	}
	// The sign-in of before 0.12.1 is the credential's now.
	x.Auth, x.Username = "", ""
	if x.Kind == SourceS3 && x.Credentials == CredentialsKeys && x.Credential == "" {
		return x, Creds{}, invalid("%s names no credential: choose an access key, or the pod's identity", x.Name)
	}
	if x.Credential == "" {
		return x, Creds{}, nil
	}
	if s.o.Credentials == nil {
		return x, Creds{}, invalid("this console keeps no credentials")
	}
	use, err := s.o.Credentials.Resolve(ctx, x.Credential, credentialUse(x), credentialAddress(x))
	if err != nil {
		return x, Creds{}, fmt.Errorf("%s's credential: %w", x.Name, err)
	}
	c := Creds{values: use.Values()}
	if strings.TrimSpace(x.CAPEM) == "" {
		x.CAPEM = use.CAPEM
	}
	switch x.Kind {
	case SourceGit:
		if user, pass, ok := use.Basic(); ok {
			x.Username, c.Token = user, pass
		} else if tok, ok := use.Bearer(); ok {
			c.Token, c.bearer = tok, true
		}
	case SourceConfluence:
		if user, pass, ok := use.Basic(); ok {
			x.Email, c.Token = user, pass
		} else if tok, ok := use.Bearer(); ok {
			c.Token = tok
		}
	case SourceS3:
		if use.Method == credentials.MethodSigV4 {
			x.Credentials = CredentialsKeys
			c.AccessKeyID, c.SecretAccessKey, c.sessionToken = use.AccessKeyID, use.SecretAccessKey, use.SessionToken
			if x.Region == "" {
				x.Region = use.Region
			}
		} else {
			x.Credentials = CredentialsIdentity
		}
	case SourceWeb, SourceLive:
		c.Token, c.apply = use.Token, use.Signer()
		x.cert = use.Certificate()
	}
	return x, c, nil
}

// --- the upgrade (CR-12) ---

// CredentialConsumer moves the sources' own credentials to Administration ›
// Credentials.
type CredentialConsumer struct{ S *Service }

// Find lists the sources not switched yet that keep a credential.
func (m CredentialConsumer) Find(ctx context.Context) ([]credentials.Found, []string, error) {
	set, err := m.S.Settings(ctx)
	if err != nil {
		return nil, nil, err
	}
	var out []credentials.Found
	var notes []string
	for _, x := range set.Sources {
		if x.CredMigrated || x.Credential != "" || !signsIn(x.Kind) {
			continue
		}
		c, err := m.S.creds(ctx, sourceKey(x.ID))
		if err != nil {
			return nil, nil, err
		}
		if c.empty() {
			continue
		}
		f := credentials.Found{Key: x.ID, Label: "source " + x.Name}
		basic := func(user string) credentials.SendAs {
			return credentials.SendAs{Method: credentials.SendBasic, Username: user}
		}
		switch x.Kind {
		case SourceGit:
			user := x.Username
			if user == "" {
				user = "x-access-token"
			}
			f.Endpoint, f.Kind, f.SendAs, f.Secret.Token = repoOwner(x.URL), credentials.KindToken, basic(user), c.Token
		case SourceConfluence:
			f.Endpoint, f.Kind, f.Secret.Token = x.BaseURL, credentials.KindToken, c.Token
			f.SendAs = credentials.SendAs{Method: credentials.SendBearer}
			if x.Flavor == confluence.Cloud {
				f.SendAs = basic(x.Email)
			}
		case SourceS3:
			if x.Credentials != CredentialsKeys {
				continue
			}
			f.Endpoint, f.Kind, f.Region = credentials.S3Address(x.Endpoint, x.Bucket), credentials.KindAccessKey, x.Region
			f.Secret.AccessKeyID, f.Secret.SecretAccessKey = c.AccessKeyID, c.SecretAccessKey
		case SourceWeb, SourceLive:
			if x.Auth == "" || c.Token == "" {
				continue
			}
			ep := siteOf(x.URL)
			if x.Kind == SourceLive {
				if ep = oneSite(x.Addresses); ep == "" {
					notes = append(notes, "source "+x.Name+" keeps its own credential: its addresses are on more than one site, and a credential covers one")
					continue
				}
			}
			f.Endpoint, f.Kind, f.Secret.Token = ep, credentials.KindToken, c.Token
			f.SendAs = credentials.SendAs{Method: credentials.SendBearer}
			if x.Auth == LiveAuthBasic {
				f.SendAs = basic(x.Username)
			}
		}
		out = append(out, f)
	}
	return out, notes, nil
}

// Switch has a source name its credential.
func (m CredentialConsumer) Switch(ctx context.Context, id, credential string) error {
	_, err := m.S.editSettings(ctx, func(set *Settings) error {
		i := slices.IndexFunc(set.Sources, func(o Source) bool { return o.ID == id })
		if i < 0 {
			return wrapf(ErrNotFound, "no source %q", id)
		}
		set.Sources[i].Credential, set.Sources[i].CredMigrated = credential, true
		return nil
	})
	m.S.live.forget(id)
	return err
}

// Uses are the sources' credentials, for Administration › Credentials' Used by.
func (s *Service) Uses(ctx context.Context) ([]credentials.Ref, error) {
	set, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	var out []credentials.Ref
	for _, x := range set.Sources {
		// One naming a git repository uses the repository's, which lists it.
		if x.Credential != "" && !x.namesRepo() {
			out = append(out, credentials.Ref{Credential: x.Credential, What: "knowledge source " + x.Name, Where: "Administration › AI › Knowledge › Sources"})
		}
	}
	return out, nil
}

// repoOwner is a repository's host and owner: https://github.com/org.
func repoOwner(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	owner, _, _ := strings.Cut(strings.Trim(u.Path, "/"), "/")
	out := u.Scheme + "://" + u.Host
	if owner != "" {
		out += "/" + owner
	}
	return out
}

// siteOf is an address's scheme and host.
func siteOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Scheme + "://" + u.Host
}

// oneSite is the one site a live source's addresses are on, "" when they
// are on several.
func oneSite(addrs []LiveAddress) string {
	site := ""
	for _, a := range addrs {
		s := siteOf(a.URL)
		if site != "" && !strings.EqualFold(s, site) {
			return ""
		}
		site = s
	}
	return site
}
