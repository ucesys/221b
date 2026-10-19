package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// OIDCConfig says where people sign in (P3-01).
type OIDCConfig struct {
	// Issuer is the provider's issuer URL, where its discovery document
	// lives: https://login.corp.example/realms/data.
	Issuer       string
	ClientID     string
	ClientSecret string
	// Scopes asked for; openid is always included. Add groups for
	// providers that need it to send the groups claim.
	Scopes []string
	// RedirectURL is the callback the provider sends people back to;
	// empty derives it from each request as https://<host>/signin/callback.
	RedirectURL string
	// UsernameClaim names the claim that identifies a person to Kubernetes
	// and the audit log (email by default); GroupsClaim the claim that lists
	// their groups (groups by default). Nested claims use dots.
	UsernameClaim string
	GroupsClaim   string
	// ProviderName is what the sign-in button says: "Sign in with Okta".
	ProviderName string
}

// discovery is the provider's configuration document.
type discovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
	EndSessionEndpoint    string `json:"end_session_endpoint"`
}

// Provider talks to one OIDC provider: discovery, the code exchange and
// ID token verification. It needs nothing beyond the standard library and
// the oauth2 package: the token is a JWS verified against the provider's
// published keys.
type Provider struct {
	cfg    OIDCConfig
	client *http.Client
	now    func() time.Time

	mu        sync.Mutex
	disc      *discovery
	discErr   error
	discAt    time.Time
	keys      []JWK
	keysAt    time.Time
	keysTried time.Time
}

// NewProvider prepares a provider; discovery happens on first use and is
// retried, so the console starts even while the provider is down.
func NewProvider(cfg OIDCConfig, client *http.Client, now func() time.Time) *Provider {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if now == nil {
		now = time.Now
	}
	if cfg.UsernameClaim == "" {
		cfg.UsernameClaim = "email"
	}
	if cfg.GroupsClaim == "" {
		cfg.GroupsClaim = "groups"
	}
	return &Provider{cfg: cfg, client: client, now: now}
}

// Name is what the sign-in button calls the provider.
func (p *Provider) Name() string {
	if p.cfg.ProviderName != "" {
		return p.cfg.ProviderName
	}
	return "your identity provider"
}

func (p *Provider) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// discover reads the provider's configuration once, retrying a failure
// after a short while.
func (p *Provider) discover(ctx context.Context) (*discovery, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.disc != nil {
		return p.disc, nil
	}
	if p.discErr != nil && p.now().Sub(p.discAt) < 10*time.Second {
		return nil, p.discErr
	}
	var d discovery
	err := p.getJSON(ctx, strings.TrimSuffix(p.cfg.Issuer, "/")+"/.well-known/openid-configuration", &d)
	p.discAt = p.now()
	if err != nil {
		p.discErr = fmt.Errorf("reading the provider's configuration: %w", err)
		return nil, p.discErr
	}
	if strings.TrimSuffix(d.Issuer, "/") != strings.TrimSuffix(p.cfg.Issuer, "/") {
		p.discErr = fmt.Errorf("the provider calls itself %q, not %q", d.Issuer, p.cfg.Issuer)
		return nil, p.discErr
	}
	if d.AuthorizationEndpoint == "" || d.TokenEndpoint == "" || d.JWKSURI == "" {
		p.discErr = errors.New("the provider's configuration lacks an authorization, token or jwks endpoint")
		return nil, p.discErr
	}
	p.disc, p.discErr = &d, nil
	return p.disc, nil
}

// keySet returns the provider's keys, fetched at most once a minute and
// refreshed after a day; force asks for a fresh set (a kid not seen yet).
func (p *Provider) keySet(ctx context.Context, force bool) ([]JWK, error) {
	d, err := p.discover(ctx)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	fresh := p.keys != nil && now.Sub(p.keysAt) < 24*time.Hour
	if fresh && !(force && now.Sub(p.keysTried) > time.Minute) {
		return p.keys, nil
	}
	p.keysTried = now
	var set JWKS
	if err := p.getJSON(ctx, d.JWKSURI, &set); err != nil {
		if p.keys != nil {
			return p.keys, nil // the old set is better than none
		}
		return nil, fmt.Errorf("reading the provider's keys: %w", err)
	}
	p.keys, p.keysAt = set.Keys, now
	return p.keys, nil
}

// oauth builds the oauth2 configuration for a callback URL.
func (p *Provider) oauth(d *discovery, redirect string) *oauth2.Config {
	scopes := []string{"openid"}
	for _, s := range p.cfg.Scopes {
		if s = strings.TrimSpace(s); s != "" && s != "openid" {
			scopes = append(scopes, s)
		}
	}
	if len(scopes) == 1 {
		scopes = append(scopes, "profile", "email")
	}
	return &oauth2.Config{
		ClientID:     p.cfg.ClientID,
		ClientSecret: p.cfg.ClientSecret,
		Endpoint:     oauth2.Endpoint{AuthURL: d.AuthorizationEndpoint, TokenURL: d.TokenEndpoint, AuthStyle: oauth2.AuthStyleInHeader},
		RedirectURL:  redirect,
		Scopes:       scopes,
	}
}

// AuthURL is where a person is sent to sign in, with PKCE and a nonce.
func (p *Provider) AuthURL(ctx context.Context, redirect, state, nonce, verifier string) (string, error) {
	d, err := p.discover(ctx)
	if err != nil {
		return "", err
	}
	return p.oauth(d, redirect).AuthCodeURL(state, oauth2.AccessTypeOnline, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("nonce", nonce)), nil
}

// Identity is what an ID token says about a person.
type Identity struct {
	Subject  string
	Username string
	Name     string
	Email    string
	Groups   []string
	// Expires is when the ID token expires.
	Expires time.Time
	// IDToken is the raw token, for providers that want it at sign-out.
	IDToken string
}

// Exchange trades the code for tokens and verifies the ID token.
func (p *Provider) Exchange(ctx context.Context, redirect, code, verifier, nonce string) (Identity, error) {
	d, err := p.discover(ctx)
	if err != nil {
		return Identity{}, err
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, p.client)
	tok, err := p.oauth(d, redirect).Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Identity{}, fmt.Errorf("exchanging the code: %w", err)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return Identity{}, errors.New("the provider sent no ID token; is openid among the scopes?")
	}
	return p.Verify(ctx, raw, nonce)
}

// Verify checks an ID token's signature and claims and reads the identity.
func (p *Provider) Verify(ctx context.Context, raw, nonce string) (Identity, error) {
	keys, err := p.keySet(ctx, false)
	if err != nil {
		return Identity{}, err
	}
	payload, err := verifyJWS(raw, keys)
	if errors.Is(err, ErrNoKey) {
		// The provider may have rotated its keys since they were read.
		if keys, err = p.keySet(ctx, true); err == nil {
			payload, err = verifyJWS(raw, keys)
		}
	}
	if err != nil {
		return Identity{}, fmt.Errorf("the ID token does not verify: %w", err)
	}
	c, err := parseClaims(payload)
	if err != nil {
		return Identity{}, err
	}
	const skew = 2 * time.Minute
	now := p.now()
	switch {
	case strings.TrimSuffix(c.Issuer, "/") != strings.TrimSuffix(p.cfg.Issuer, "/"):
		return Identity{}, fmt.Errorf("the ID token was issued by %q, not %q", c.Issuer, p.cfg.Issuer)
	case !contains(c.Audience, p.cfg.ClientID):
		return Identity{}, fmt.Errorf("the ID token is for client %v, not %q", c.Audience, p.cfg.ClientID)
	case c.Expires == 0 || time.Unix(c.Expires, 0).Before(now.Add(-skew)):
		return Identity{}, errors.New("the ID token has expired")
	case c.IssuedAt != 0 && time.Unix(c.IssuedAt, 0).After(now.Add(skew)):
		return Identity{}, errors.New("the ID token is from the future; check the clocks")
	case c.NotBefore != 0 && time.Unix(c.NotBefore, 0).After(now.Add(skew)):
		return Identity{}, errors.New("the ID token is not valid yet")
	case nonce != "" && c.Nonce != nonce:
		return Identity{}, errors.New("the ID token's nonce does not match this sign-in")
	case c.Subject == "":
		return Identity{}, errors.New("the ID token names no subject")
	}
	id := Identity{Subject: c.Subject, Expires: time.Unix(c.Expires, 0), IDToken: raw}
	// An email the provider says it has not verified is not used: it could
	// name an administrator in auth.admins. As the API server does, a token
	// without email_verified is taken as verified, and one that names the
	// person by an unverified email is refused.
	if unverifiedEmail(c.All) {
		if p.cfg.UsernameClaim == "email" {
			return Identity{}, errors.New("the provider has not verified the email address that names you")
		}
	} else {
		id.Email = stringClaim(c.All, "email")
	}
	id.Name = stringClaim(c.All, "name")
	id.Username = stringClaim(c.All, p.cfg.UsernameClaim)
	if id.Username == "" {
		id.Username = stringClaim(c.All, "preferred_username")
	}
	if id.Username == "" {
		id.Username = c.Subject
	}
	id.Groups = stringsClaim(c.All, p.cfg.GroupsClaim)
	return id, nil
}

// unverifiedEmail says whether the token's email_verified claim is there
// and not true; some providers send it as the string "true".
func unverifiedEmail(all map[string]any) bool {
	v, ok := all["email_verified"]
	if !ok {
		return false
	}
	switch b := v.(type) {
	case bool:
		return !b
	case string:
		return !strings.EqualFold(b, "true")
	}
	return true
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
