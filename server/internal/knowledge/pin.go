package knowledge

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/certpin"
	"github.com/ucesys/221b/server/internal/credentials"
)

// --- Trust this certificate (0.12.1, CR-15 to CR-19) ---
//
// A source's or the store's Test shows the certificate its address
// presents when it is not trusted (CR-15). Trust pins it (CR-16): with
// the source's credential when it names one, otherwise with the source,
// or with the store. Forget lets it go (CR-19). Both are SuperAdmins'
// and audited.

// Audit actions of a certificate trusted and let go.
const (
	ActionTrust  = "knowledge.trust"
	ActionForget = "knowledge.forget"
)

// sameAddr says two addresses are the same scheme, host and port.
func sameAddr(a, b *url.URL) bool {
	return a != nil && b != nil && strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

// probeAddr is the https:// address of a source whose certificate a Test
// looks at: its address, or a live source's first https:// one; nil for
// none.
func probeAddr(x Source) *url.URL {
	if x.Kind == SourceLive {
		for _, a := range x.Addresses {
			if strings.HasPrefix(a.URL, "https://") {
				return sourceAddr(Source{Kind: SourceWeb, URL: a.URL})
			}
		}
		return nil
	}
	u := sourceAddr(x)
	if u == nil || u.Scheme != "https" {
		return nil
	}
	return u
}

// probe looks at the certificate an address presents, with trust.
func (s *Service) probe(ctx context.Context, x Source, u *url.URL, t certpin.Trust) (certpin.Cert, error) {
	var dial certpin.DialFunc
	if s.dial != nil {
		dial = s.dial
	}
	base := s.rootsFor(Source{Kind: x.Kind})
	return certpin.ProbeHost(ctx, dial, u.Host, u.Hostname(), base, t)
}

// certificateOf is the certificate a source's address presents, for its
// Test: when it is not trusted, or is the pinned one; nil otherwise, or
// when it cannot be looked at (through a proxy, say).
func (s *Service) certificateOf(ctx context.Context, x Source, ok bool) *certpin.Cert {
	if ok && x.pin() == "" {
		return nil
	}
	u := probeAddr(x)
	if u == nil {
		return nil
	}
	c, err := s.probe(ctx, x, u, x.trust())
	if err != nil || c.Trusted && !c.Pinned {
		return nil
	}
	return &c
}

// TrustInput is Trust this certificate in a source's dialog: the
// fingerprint its Test showed, and the source as typed (Source) or saved
// (Saved, its id).
type TrustInput struct {
	SHA256 string  `json:"sha256"`
	Source *Source `json:"source,omitempty"`
	Saved  string  `json:"saved,omitempty"`
}

// Pinned is a certificate pinned, and with what: "source", "store",
// "credential" (CredentialName names it) or "repository".
type Pinned struct {
	Pin            *certpin.Pin `json:"pin"`
	With           string       `json:"with"`
	CredentialName string       `json:"credentialName,omitempty"`
	Repository     string       `json:"repository,omitempty"`
}

func pinDetail(addr string, p *certpin.Pin) string {
	d := addr + ": SHA-256 " + p.SHA256
	if p.Subject != "" {
		d += ", subject " + p.Subject
	}
	if !p.NotAfter.IsZero() {
		d += ", valid until " + p.NotAfter.Format(time.DateOnly)
	}
	return d
}

// TrustSource pins the certificate a source's address presents (CR-16):
// the one its Test showed, looked at again. A source naming a credential
// pins it with the credential, which every use of that credential then
// accepts; one without pins it with itself, and must be saved first.
func (s *Service) TrustSource(ctx context.Context, c Caller, in TrustInput) (Pinned, error) {
	if !c.SuperAdmin {
		return Pinned{}, wrapf(ErrDenied, "only a SuperAdmin trusts a source's certificate")
	}
	if err := s.leads(); err != nil {
		return Pinned{}, err
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return Pinned{}, err
	}
	var x Source
	saved, isSaved := set.source(in.Saved)
	switch {
	case in.Source != nil:
		if x, err = s.fillRepo(ctx, *in.Source); err != nil {
			return Pinned{}, err
		}
		x = x.normalized()
	case isSaved:
		x = saved
	default:
		return Pinned{}, wrapf(ErrNotFound, "no source %q", in.Saved)
	}
	u := probeAddr(x)
	if u == nil {
		return Pinned{}, invalid("only a source at an https:// address presents a certificate")
	}
	if x.Credential != "" && signsIn(x.Kind) {
		if s.o.Credentials == nil {
			return Pinned{}, invalid("this console keeps no credentials")
		}
		cr, _, err := s.o.Credentials.Trust(ctx, x.Credential, credentialAddress(x), in.SHA256, c.label())
		if err != nil {
			s.audit(audit.Event{Action: ActionTrust, Who: c.who(), Target: cmp.Or(in.Saved, x.Name), Outcome: audit.OutcomeFailed, From: c.From, Detail: "certificate of " + u.Host + " not trusted: " + err.Error()})
			return Pinned{}, credentialErr(err)
		}
		s.audit(audit.Event{Action: ActionTrust, Who: c.who(), Target: cmp.Or(in.Saved, x.Name), Outcome: audit.OutcomeOK, From: c.From, Detail: "certificate trusted with the credential " + cr.Name + " for " + pinDetail(u.Host, cr.PinnedCert)})
		s.live.forget(x.ID)
		return Pinned{Pin: cr.PinnedCert, With: "credential", CredentialName: cr.Name}, nil
	}
	if !isSaved {
		return Pinned{}, invalid("save the source first: without a credential, its certificate is trusted with the source")
	}
	if !sameAddr(probeAddr(saved), u) {
		return Pinned{}, invalid("the source's address was changed in the dialog: save it, then test and trust its certificate")
	}
	cert, err := credentials.CheckTrust(ctx, func(ctx context.Context) (certpin.Cert, error) {
		return s.probe(ctx, x, u, certpin.Trust{CAPEM: x.CAPEM})
	}, u.Host, in.SHA256)
	if err != nil {
		s.audit(audit.Event{Action: ActionTrust, Who: c.who(), Target: saved.ID, Outcome: audit.OutcomeFailed, From: c.From, Detail: "certificate of " + u.Host + " not trusted: " + err.Error()})
		return Pinned{}, credentialErr(err)
	}
	pin := certpin.PinOf(cert, c.label(), s.o.Now())
	if _, err := s.editSettings(ctx, func(set *Settings) error {
		i := slices.IndexFunc(set.Sources, func(o Source) bool { return o.ID == saved.ID })
		if i < 0 {
			return wrapf(ErrNotFound, "no source %q", saved.ID)
		}
		set.Sources[i].PinnedCert = pin
		return nil
	}); err != nil {
		return Pinned{}, err
	}
	s.live.forget(saved.ID)
	s.audit(audit.Event{Action: ActionTrust, Who: c.who(), Target: saved.ID, Outcome: audit.OutcomeOK, From: c.From, Detail: "certificate trusted for the source " + saved.Name + " at " + pinDetail(u.Host, pin)})
	return Pinned{Pin: pin, With: "source"}, nil
}

// credentialErr is a credential's refusal as the knowledge API answers
// it: not valid, or not the certificate seen, is the person's to fix.
func credentialErr(err error) error {
	switch {
	case errors.Is(err, credentials.ErrInvalid):
		return invalid("%s", strings.TrimPrefix(err.Error(), credentials.ErrInvalid.Error()+": "))
	case errors.Is(err, credentials.ErrNotSeen):
		return fmt.Errorf("%w: %s", ErrConflict, strings.TrimPrefix(err.Error(), credentials.ErrNotSeen.Error()+": "))
	}
	return err
}

// ForgetSource lets a source's pinned certificate go (CR-19): its own, or
// its credential's.
func (s *Service) ForgetSource(ctx context.Context, c Caller, id string) error {
	if !c.SuperAdmin {
		return wrapf(ErrDenied, "only a SuperAdmin forgets a source's certificate")
	}
	if err := s.leads(); err != nil {
		return err
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	x, ok := set.source(id)
	if !ok {
		return wrapf(ErrNotFound, "no source %q", id)
	}
	var detail string
	switch {
	case x.PinnedCert != nil:
		if _, err := s.editSettings(ctx, func(set *Settings) error {
			i := slices.IndexFunc(set.Sources, func(o Source) bool { return o.ID == id })
			if i < 0 {
				return wrapf(ErrNotFound, "no source %q", id)
			}
			set.Sources[i].PinnedCert = nil
			return nil
		}); err != nil {
			return err
		}
		detail = "pinned certificate of the source " + x.Name + " forgotten: SHA-256 " + x.PinnedCert.SHA256
	case x.Credential != "" && s.o.Credentials != nil:
		cr, err := s.o.Credentials.Forget(ctx, x.Credential)
		if err != nil {
			return credentialErr(err)
		}
		detail = "pinned certificate of the credential " + cr.Name + " forgotten (from the source " + x.Name + "): SHA-256 " + cr.PinnedCert.SHA256
	case x.repoPin != nil:
		return invalid("the certificate is pinned with the repository %s: forget it in Administration › Settings › Repositories", x.Repository)
	default:
		return invalid("%s has no pinned certificate", x.Name)
	}
	s.live.forget(id)
	s.audit(audit.Event{Action: ActionForget, Who: c.who(), Target: id, Outcome: audit.OutcomeOK, From: c.From, Detail: detail})
	return nil
}

// SourcePins are the certificates pinned for the sources, by id: their
// own, their credential's, their repository's (CR-19).
func (s *Service) SourcePins(ctx context.Context, set Settings) map[string]Pinned {
	out := map[string]Pinned{}
	for _, x := range set.Sources {
		switch {
		case x.PinnedCert != nil:
			out[x.ID] = Pinned{Pin: x.PinnedCert, With: "source"}
		case x.Credential != "" && s.o.Credentials != nil:
			if cr, err := s.o.Credentials.Get(ctx, x.Credential); err == nil && cr.PinnedCert != nil {
				out[x.ID] = Pinned{Pin: cr.PinnedCert, With: "credential", CredentialName: cr.Name}
			}
		case x.repoPin != nil:
			out[x.ID] = Pinned{Pin: x.repoPin, With: "repository", Repository: x.Repository}
		}
	}
	return out
}

// --- the wiki's store ---

// withSavedPin is a store as typed with the saved store's pinned
// certificate, while it is reached at the same address.
func (s *Service) withSavedPin(ctx context.Context, st StoreSettings) StoreSettings {
	st.PinnedCert = nil
	if set, err := s.Settings(ctx); err == nil && sameAddr(set.Store.addr(), st.addr()) {
		st.PinnedCert = set.Store.PinnedCert
	}
	return st
}

// StoreCertificate is the certificate the store as typed presents, for
// its Test: when it is not trusted, or is the pinned one.
func (s *Service) StoreCertificate(ctx context.Context, c Caller, in StoreInput, ok bool) *certpin.Cert {
	if !c.SuperAdmin {
		return nil
	}
	st := s.withSavedPin(ctx, in.Store.normalized())
	u := st.addr()
	if u == nil || u.Scheme != "https" || ok && st.PinnedCert == nil {
		return nil
	}
	cert, err := s.probe(ctx, Source{Kind: SourceS3}, u, st.trust())
	if err != nil || cert.Trusted && !cert.Pinned {
		return nil
	}
	return &cert
}

// TrustStore pins the certificate the saved store presents (CR-16).
func (s *Service) TrustStore(ctx context.Context, c Caller, sha256 string) (Pinned, error) {
	if !c.SuperAdmin {
		return Pinned{}, wrapf(ErrDenied, "only a SuperAdmin trusts the store's certificate")
	}
	if err := s.leads(); err != nil {
		return Pinned{}, err
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return Pinned{}, err
	}
	st := set.Store
	u := st.addr()
	if u == nil || u.Scheme != "https" {
		return Pinned{}, invalid("save a store at an https:// address first: its certificate is trusted with it")
	}
	cert, err := credentials.CheckTrust(ctx, func(ctx context.Context) (certpin.Cert, error) {
		return s.probe(ctx, Source{Kind: SourceS3}, u, certpin.Trust{CAPEM: st.CAPEM})
	}, u.Host, sha256)
	if err != nil {
		s.audit(audit.Event{Action: ActionTrust, Who: c.who(), Target: "store", Outcome: audit.OutcomeFailed, From: c.From, Detail: "certificate of " + u.Host + " not trusted: " + err.Error()})
		return Pinned{}, credentialErr(err)
	}
	pin := certpin.PinOf(cert, c.label(), s.o.Now())
	if _, err := s.editSettings(ctx, func(v *Settings) error {
		if !sameAddr(v.Store.addr(), u) {
			return wrapf(ErrConflict, "the store changed meanwhile")
		}
		v.Store.PinnedCert = pin
		return nil
	}); err != nil {
		return Pinned{}, err
	}
	s.audit(audit.Event{Action: ActionTrust, Who: c.who(), Target: "store", Outcome: audit.OutcomeOK, From: c.From, Detail: "certificate trusted for the wiki's store at " + pinDetail(u.Host, pin)})
	s.refreshSoon(ctx)
	return Pinned{Pin: pin, With: "store"}, nil
}

// ForgetStore lets the store's pinned certificate go (CR-19).
func (s *Service) ForgetStore(ctx context.Context, c Caller) error {
	if !c.SuperAdmin {
		return wrapf(ErrDenied, "only a SuperAdmin forgets the store's certificate")
	}
	if err := s.leads(); err != nil {
		return err
	}
	var old *certpin.Pin
	if _, err := s.editSettings(ctx, func(v *Settings) error {
		if old = v.Store.PinnedCert; old == nil {
			return invalid("the store has no pinned certificate")
		}
		v.Store.PinnedCert = nil
		return nil
	}); err != nil {
		return err
	}
	s.audit(audit.Event{Action: ActionForget, Who: c.who(), Target: "store", Outcome: audit.OutcomeOK, From: c.From, Detail: "pinned certificate of the wiki's store forgotten: SHA-256 " + old.SHA256})
	return nil
}

// refreshSoon reads the wiki again from its store, in the background.
func (s *Service) refreshSoon(ctx context.Context) {
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		if err := s.Refresh(ctx); err != nil && !errors.Is(err, ErrNoStore) {
			s.o.Logger.Warn("reading the wiki from its store failed", "err", err)
		}
	}()
}
