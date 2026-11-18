package apps

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/certpin"
	"github.com/ucesys/221b/server/internal/credentials"
)

// --- Trust this certificate (0.12.1, CR-15 to CR-19) ---
//
// A repository's Test shows the certificate it presents when it is not
// trusted (CR-15). Trust pins it (CR-16): with the repository's
// credential when it names one, otherwise with the repository. Every
// fetch carries the pin in its context to the console's clients
// (certpin.ContextTransport), and accepts that certificate and no other
// besides those verified normally (CR-17, CR-18).

// httpsOf is the https:// address of a repository, a registry's for an
// oci:// one.
func httpsOf(repo Repository) string {
	if rest, ok := strings.CutPrefix(repo.URL, "oci://"); ok {
		host, _, _ := strings.Cut(rest, "/")
		return "https://" + host
	}
	return repo.URL
}

// probe looks at the certificate a repository presents, with trust.
func (r *Repos) probe(ctx context.Context, repo Repository, t certpin.Trust) (certpin.Cert, error) {
	d := &net.Dialer{Timeout: 10 * time.Second, Control: checkConnect}
	if AllowLocalRepos {
		d.Control = nil
	}
	var base *x509.CertPool
	if r.o.Client != nil {
		if tr, ok := r.o.Client.Transport.(*http.Transport); ok && tr.TLSClientConfig != nil {
			base = tr.TLSClientConfig.RootCAs
		}
	}
	return certpin.Probe(ctx, d.DialContext, httpsOf(repo), base, t)
}

// noteCertificate adds to a test the certificate the repository
// presented, when it is not trusted or is the pinned one (CR-15).
func (r *Repos) noteCertificate(ctx context.Context, repo Repository, res *TestResult) {
	if !strings.HasPrefix(httpsOf(repo), "https://") || res.OK && repo.trust.Pin == "" {
		return
	}
	c, err := r.probe(ctx, repo, repo.trust)
	if err != nil || c.Trusted && !c.Pinned {
		return
	}
	res.Certificate = &c
	switch {
	case c.Pinned && c.Expired:
		res.Text += fmt.Sprintf(" The pinned certificate expired on %s; it is still accepted, as pinned.", c.NotAfter.Format(time.DateOnly))
	case c.Changed() != "":
		res.Text += " " + strings.ToUpper(c.Changed()[:1]) + c.Changed()[1:] + "."
	case !c.OK() && c.Trustable:
		res.Text += " Its certificate is not trusted: check it and trust it, or give its credential the private CA."
	}
}

// Pinned is a certificate pinned, and with what: "repository", or
// "credential" (CredentialName names it).
type Pinned struct {
	Pin            *certpin.Pin `json:"pin"`
	With           string       `json:"with"`
	CredentialName string       `json:"credentialName,omitempty"`
}

// PinOf is the certificate pinned for a repository, nil for none: its
// own, or its credential's.
func (r *Repos) PinOf(ctx context.Context, repo Repository) *Pinned {
	if repo.PinnedCert != nil {
		return &Pinned{Pin: repo.PinnedCert, With: "repository"}
	}
	if repo.Credential != "" && r.o.Credentials != nil {
		if c, err := r.o.Credentials.Get(ctx, repo.Credential); err == nil && c.PinnedCert != nil {
			return &Pinned{Pin: c.PinnedCert, With: "credential", CredentialName: c.Name}
		}
	}
	return nil
}

// Trust pins the certificate a saved repository presents (CR-16): the one
// its Test showed, looked at again; with its credential when it names
// one, otherwise with the repository.
func (r *Repos) Trust(ctx context.Context, name, sha256, by string) (Repository, Pinned, error) {
	repo, err := r.Raw(ctx, name)
	if err != nil {
		return Repository{}, Pinned{}, err
	}
	if repo.Credential != "" {
		if r.o.Credentials == nil {
			return Repository{}, Pinned{}, fmt.Errorf("%w: this console keeps no credentials", ErrInvalidRepo)
		}
		c, _, err := r.o.Credentials.Trust(ctx, repo.Credential, repo.URL, sha256, by)
		if err != nil {
			return repo, Pinned{}, trustErr(err)
		}
		r.forget(repo.Name)
		return repo, Pinned{Pin: c.PinnedCert, With: "credential", CredentialName: c.Name}, nil
	}
	cert, err := credentials.CheckTrust(ctx, func(ctx context.Context) (certpin.Cert, error) {
		return r.probe(ctx, repo, certpin.Trust{})
	}, strings.TrimPrefix(httpsOf(repo), "https://"), sha256)
	if err != nil {
		return repo, Pinned{}, trustErr(err)
	}
	pin := certpin.PinOf(cert, by, r.o.Now())
	if err := r.setPin(ctx, repo.Name, repo.URL, pin); err != nil {
		return repo, Pinned{}, err
	}
	repo.PinnedCert = pin
	return repo, Pinned{Pin: pin, With: "repository"}, nil
}

// Forget lets a repository's pinned certificate go (CR-19): its own, or
// its credential's. It returns what was pinned.
func (r *Repos) Forget(ctx context.Context, name string) (Repository, Pinned, error) {
	repo, err := r.Raw(ctx, name)
	if err != nil {
		return Repository{}, Pinned{}, err
	}
	p := r.PinOf(ctx, repo)
	switch {
	case p == nil:
		return repo, Pinned{}, fmt.Errorf("%w: %s has no pinned certificate", ErrInvalidRepo, repo.Name)
	case p.With == "credential":
		if _, err := r.o.Credentials.Forget(ctx, repo.Credential); err != nil {
			return repo, Pinned{}, trustErr(err)
		}
	default:
		if err := r.setPin(ctx, repo.Name, repo.URL, nil); err != nil {
			return repo, Pinned{}, err
		}
	}
	r.forget(repo.Name)
	return repo, *p, nil
}

func (r *Repos) setPin(ctx context.Context, name, url string, pin *certpin.Pin) error {
	return r.update(ctx, func(rec *repoRecord) error {
		i := slices.IndexFunc(rec.Repositories, func(x Repository) bool { return x.Name == name })
		if i < 0 || rec.Repositories[i].URL != url {
			return fmt.Errorf("%w %q: it changed meanwhile", ErrNoRepository, name)
		}
		rec.Repositories[i].PinnedCert = pin
		return nil
	})
}

// ErrNotSeen is a fingerprint to trust that is not the certificate the
// repository presents now.
var ErrNotSeen = credentials.ErrNotSeen

// trustErr is a credential's refusal as the repositories answer it.
func trustErr(err error) error {
	if errors.Is(err, credentials.ErrInvalid) {
		return fmt.Errorf("%w: %s", ErrInvalidRepo, strings.TrimPrefix(err.Error(), credentials.ErrInvalid.Error()+": "))
	}
	return err
}
