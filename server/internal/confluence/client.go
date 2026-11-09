// Package confluence is the little of Confluence's REST API the console
// needs (Phase 12, ADR-97): reading a space's pages for Mycroft's
// knowledge, and writing the known-issue and wiki page trees the
// publisher keeps. Confluence Cloud is reached through REST v2 with an
// account's email and API token; Data Center through REST v1
// (/rest/api) with a personal access token. Every call retries what
// Confluence asks to be retried: 429 after its Retry-After, and 5xx with
// backoff.
package confluence

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Flavors of Confluence.
const (
	Cloud      = "cloud"
	DataCenter = "datacenter"
)

// Config says where Confluence is and how to sign in.
type Config struct {
	// BaseURL is the site: https://example.atlassian.net for Cloud, or
	// https://confluence.example.com[/context] for Data Center.
	BaseURL string
	Flavor  string
	// Email and Token are a Cloud account's email and API token; Data
	// Center takes Token alone, as a personal access token.
	Email string
	Token string
	// CAPEM, when set, is a PEM bundle trusted besides the system's.
	CAPEM string
	// HTTP replaces the client (tests); CAPEM is then not used.
	HTTP *http.Client
	// Retries is how often a call is tried again (default 4); Backoff the
	// first wait (default 1s), doubled each time; MaxWait bounds one wait,
	// Retry-After included (default 60s).
	Retries int
	Backoff time.Duration
	MaxWait time.Duration
	// Sleep waits (tests replace it).
	Sleep func(ctx context.Context, d time.Duration) error
}

// Client talks to one Confluence site.
type Client struct {
	cfg  Config
	base *url.URL
	http *http.Client
}

// Space is a space.
type Space struct {
	ID         string `json:"id"`
	Key        string `json:"key"`
	Name       string `json:"name"`
	HomepageID string `json:"homepageId,omitempty"`
}

// Page is a page; Body (storage format, XHTML) only when read one by one.
type Page struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	SpaceKey string    `json:"spaceKey,omitempty"`
	ParentID string    `json:"parentId,omitempty"`
	Version  int       `json:"version"`
	Modified time.Time `json:"modified,omitzero"`
	Body     string    `json:"body,omitempty"`
	URL      string    `json:"url,omitempty"`
}

// Error is Confluence's refusal.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("Confluence answered %d: %s", e.Status, msg)
}

// NotFound says whether err is Confluence's answer that there is no such
// page or space.
func NotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// VersionConflict says whether err is a write refused because the page's version
// moved on since it was read.
func VersionConflict(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusConflict
}

// New checks the configuration and makes the client.
func New(c Config) (*Client, error) {
	if c.Flavor != Cloud && c.Flavor != DataCenter {
		return nil, fmt.Errorf("Confluence is %s or %s, not %q", Cloud, DataCenter, c.Flavor)
	}
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(c.BaseURL), "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" {
		return nil, fmt.Errorf("the address %q is not one like https://example.atlassian.net", c.BaseURL)
	}
	if c.Token == "" {
		return nil, errors.New("no API token")
	}
	if c.Flavor == Cloud && c.Email == "" {
		return nil, errors.New("Confluence Cloud needs the account's email with its API token")
	}
	hc := c.HTTP
	if hc == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		if strings.TrimSpace(c.CAPEM) != "" {
			pool, err := x509.SystemCertPool()
			if err != nil || pool == nil {
				pool = x509.NewCertPool()
			}
			if !pool.AppendCertsFromPEM([]byte(c.CAPEM)) {
				return nil, errors.New("the CA certificate is not PEM")
			}
			tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		}
		hc = &http.Client{Transport: tr, Timeout: time.Minute}
	}
	if c.Retries <= 0 {
		c.Retries = 4
	}
	if c.Backoff <= 0 {
		c.Backoff = time.Second
	}
	if c.MaxWait <= 0 {
		c.MaxWait = time.Minute
	}
	if c.Sleep == nil {
		c.Sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	return &Client{cfg: c, base: u, http: hc}, nil
}

// Flavor is the client's flavor.
func (c *Client) Flavor() string { return c.cfg.Flavor }

// api is the REST address of a path: Cloud's v2 under /wiki/api/v2, its
// v1 under /wiki/rest/api, Data Center's under /rest/api.
func (c *Client) api(v2 bool, path string, q url.Values) string {
	u := *c.base
	switch {
	case c.cfg.Flavor == Cloud && v2:
		u.Path += "/wiki/api/v2" + path
	case c.cfg.Flavor == Cloud:
		u.Path += "/wiki/rest/api" + path
	default:
		u.Path += "/rest/api" + path
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// do sends a request, retrying 429 and 5xx, and decodes the answer into
// out when given.
func (c *Client) do(ctx context.Context, method, addr string, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	wait := c.cfg.Backoff
	for try := 0; ; try++ {
		req, err := http.NewRequestWithContext(ctx, method, addr, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		if body == nil {
			req.Body, req.GetBody, req.ContentLength = http.NoBody, nil, 0
		} else {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Accept", "application/json")
		if c.cfg.Flavor == Cloud {
			req.SetBasicAuth(c.cfg.Email, c.cfg.Token)
		} else {
			req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
		}
		res, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil || try >= c.cfg.Retries {
				return err
			}
			if err := c.cfg.Sleep(ctx, wait); err != nil {
				return err
			}
			wait = min(wait*2, c.cfg.MaxWait)
			continue
		}
		b, rerr := io.ReadAll(io.LimitReader(res.Body, 32<<20))
		res.Body.Close()
		if res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500 {
			if try >= c.cfg.Retries {
				return &Error{Status: res.StatusCode, Message: message(b)}
			}
			d := wait
			if s, err := strconv.Atoi(strings.TrimSpace(res.Header.Get("Retry-After"))); err == nil && s >= 0 {
				d = time.Duration(s) * time.Second
			}
			if err := c.cfg.Sleep(ctx, min(d, c.cfg.MaxWait)); err != nil {
				return err
			}
			wait = min(wait*2, c.cfg.MaxWait)
			continue
		}
		if rerr != nil {
			return rerr
		}
		if res.StatusCode >= 300 {
			return &Error{Status: res.StatusCode, Message: message(b)}
		}
		if out == nil || len(b) == 0 {
			return nil
		}
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("reading Confluence's answer: %w", err)
		}
		return nil
	}
}

// message is the reason in an error answer, short.
func message(b []byte) string {
	var e struct {
		Message string `json:"message"`
		Errors  []struct {
			Title string `json:"title"`
		} `json:"errors"`
	}
	if json.Unmarshal(b, &e) == nil {
		if e.Message != "" {
			return clip(e.Message, 300)
		}
		if len(e.Errors) > 0 {
			return clip(e.Errors[0].Title, 300)
		}
	}
	return ""
}

// The answers' shapes.
type (
	v2Version struct {
		Number    int       `json:"number"`
		CreatedAt time.Time `json:"createdAt"`
		Message   string    `json:"message,omitempty"`
	}
	v2Body struct {
		Storage *struct {
			Value string `json:"value"`
		} `json:"storage,omitempty"`
	}
	links struct {
		WebUI string `json:"webui"`
		Base  string `json:"base"`
		Next  string `json:"next"`
	}
	v2Page struct {
		ID       string    `json:"id"`
		Title    string    `json:"title"`
		SpaceID  string    `json:"spaceId"`
		ParentID string    `json:"parentId"`
		Version  v2Version `json:"version"`
		Body     v2Body    `json:"body"`
		Links    links     `json:"_links"`
	}
	v1Content struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Space *struct {
			Key string `json:"key"`
		} `json:"space,omitempty"`
		Version *struct {
			Number int       `json:"number"`
			When   time.Time `json:"when"`
		} `json:"version,omitempty"`
		Body *struct {
			Storage *struct {
				Value string `json:"value"`
			} `json:"storage,omitempty"`
		} `json:"body,omitempty"`
		Ancestors []struct {
			ID string `json:"id"`
		} `json:"ancestors,omitempty"`
		Links links `json:"_links"`
	}
)

func (c *Client) webURL(l links) string {
	if l.WebUI == "" {
		return ""
	}
	base := l.Base
	if base == "" {
		base = c.base.String()
		if c.cfg.Flavor == Cloud {
			base += "/wiki"
		}
	}
	return base + l.WebUI
}

func (c *Client) fromV2(p v2Page, spaceKey string) Page {
	out := Page{ID: p.ID, Title: p.Title, SpaceKey: spaceKey, ParentID: p.ParentID, Version: p.Version.Number, Modified: p.Version.CreatedAt, URL: c.webURL(p.Links)}
	if p.Body.Storage != nil {
		out.Body = p.Body.Storage.Value
	}
	return out
}

func (c *Client) fromV1(p v1Content) Page {
	out := Page{ID: p.ID, Title: p.Title, URL: c.webURL(p.Links)}
	if p.Space != nil {
		out.SpaceKey = p.Space.Key
	}
	if p.Version != nil {
		out.Version, out.Modified = p.Version.Number, p.Version.When
	}
	if p.Body != nil && p.Body.Storage != nil {
		out.Body = p.Body.Storage.Value
	}
	if n := len(p.Ancestors); n > 0 {
		out.ParentID = p.Ancestors[n-1].ID
	}
	return out
}

// Space finds a space by its key.
func (c *Client) Space(ctx context.Context, key string) (Space, error) {
	if c.cfg.Flavor == Cloud {
		var r struct {
			Results []Space `json:"results"`
		}
		if err := c.do(ctx, http.MethodGet, c.api(true, "/spaces", url.Values{"keys": {key}}), nil, &r); err != nil {
			return Space{}, err
		}
		if len(r.Results) == 0 {
			return Space{}, &Error{Status: http.StatusNotFound, Message: "no space " + key}
		}
		return r.Results[0], nil
	}
	var r struct {
		ID       json.Number `json:"id"`
		Key      string      `json:"key"`
		Name     string      `json:"name"`
		Homepage *struct {
			ID string `json:"id"`
		} `json:"homepage"`
	}
	if err := c.do(ctx, http.MethodGet, c.api(false, "/space/"+url.PathEscape(key), url.Values{"expand": {"homepage"}}), nil, &r); err != nil {
		return Space{}, err
	}
	s := Space{ID: r.ID.String(), Key: r.Key, Name: r.Name}
	if r.Homepage != nil {
		s.HomepageID = r.Homepage.ID
	}
	return s, nil
}

// Page reads a page with its body.
func (c *Client) Page(ctx context.Context, id string) (Page, error) {
	if c.cfg.Flavor == Cloud {
		var p v2Page
		if err := c.do(ctx, http.MethodGet, c.api(true, "/pages/"+url.PathEscape(id), url.Values{"body-format": {"storage"}}), nil, &p); err != nil {
			return Page{}, err
		}
		return c.fromV2(p, ""), nil
	}
	var p v1Content
	if err := c.do(ctx, http.MethodGet, c.api(false, "/content/"+url.PathEscape(id), url.Values{"expand": {"body.storage,version,ancestors,space"}}), nil, &p); err != nil {
		return Page{}, err
	}
	return c.fromV1(p), nil
}

// maxPages bounds a listing.
const maxPages = 5000

// Children lists a page's child pages, without their bodies.
func (c *Client) Children(ctx context.Context, id string) ([]Page, error) {
	if c.cfg.Flavor == Cloud {
		return c.listV2(ctx, c.api(true, "/pages/"+url.PathEscape(id)+"/children", url.Values{"limit": {"250"}}), "", time.Time{})
	}
	return c.listV1(ctx, "/content/"+url.PathEscape(id)+"/child/page", url.Values{"expand": {"version,ancestors"}}, time.Time{})
}

// Pages lists a space's pages, without their bodies: those changed since
// a time when it is set, newest first, or every one.
func (c *Client) Pages(ctx context.Context, space Space, since time.Time) ([]Page, error) {
	if c.cfg.Flavor == Cloud {
		return c.listV2(ctx, c.api(true, "/spaces/"+url.PathEscape(space.ID)+"/pages", url.Values{"limit": {"250"}, "sort": {"-modified-date"}}), space.Key, since)
	}
	q := url.Values{"spaceKey": {space.Key}, "type": {"page"}, "expand": {"version,ancestors"}}
	return c.listV1(ctx, "/content", q, since)
}

func (c *Client) listV2(ctx context.Context, addr, spaceKey string, since time.Time) ([]Page, error) {
	var out []Page
	for addr != "" && len(out) < maxPages {
		var r struct {
			Results []v2Page `json:"results"`
			Links   links    `json:"_links"`
		}
		if err := c.do(ctx, http.MethodGet, addr, nil, &r); err != nil {
			return nil, err
		}
		for _, p := range r.Results {
			pg := c.fromV2(p, spaceKey)
			if !since.IsZero() && !pg.Modified.IsZero() && pg.Modified.Before(since) {
				// Newest first: the rest are older.
				return out, nil
			}
			out = append(out, pg)
		}
		addr = ""
		if r.Links.Next != "" {
			next, err := url.Parse(r.Links.Next)
			if err != nil {
				return nil, err
			}
			addr = c.base.ResolveReference(next).String()
		}
	}
	return out, nil
}

func (c *Client) listV1(ctx context.Context, path string, q url.Values, since time.Time) ([]Page, error) {
	var out []Page
	for start := 0; start < maxPages; {
		q.Set("start", strconv.Itoa(start))
		q.Set("limit", "100")
		var r struct {
			Results []v1Content `json:"results"`
			Size    int         `json:"size"`
			Links   links       `json:"_links"`
		}
		if err := c.do(ctx, http.MethodGet, c.api(false, path, q), nil, &r); err != nil {
			return nil, err
		}
		for _, p := range r.Results {
			pg := c.fromV1(p)
			if !since.IsZero() && !pg.Modified.IsZero() && pg.Modified.Before(since) {
				continue
			}
			out = append(out, pg)
		}
		if r.Links.Next == "" || len(r.Results) == 0 {
			break
		}
		start += len(r.Results)
	}
	return out, nil
}

// Create writes a new page under parentID (the space's home when empty).
func (c *Client) Create(ctx context.Context, space Space, parentID, title, body string) (Page, error) {
	if c.cfg.Flavor == Cloud {
		in := map[string]any{"spaceId": space.ID, "status": "current", "title": title, "body": map[string]string{"representation": "storage", "value": body}}
		if parentID != "" {
			in["parentId"] = parentID
		}
		var p v2Page
		if err := c.do(ctx, http.MethodPost, c.api(true, "/pages", nil), in, &p); err != nil {
			return Page{}, err
		}
		return c.fromV2(p, space.Key), nil
	}
	in := map[string]any{"type": "page", "title": title, "space": map[string]string{"key": space.Key},
		"body": map[string]any{"storage": map[string]string{"value": body, "representation": "storage"}}}
	if parentID != "" {
		in["ancestors"] = []map[string]string{{"id": parentID}}
	}
	var p v1Content
	if err := c.do(ctx, http.MethodPost, c.api(false, "/content", nil), in, &p); err != nil {
		return Page{}, err
	}
	return c.fromV1(p), nil
}

// Update writes a page as its next version: version is the number it
// was read at, and Confluence refuses (Conflict) when it moved on.
// parentID, when set, moves the page under another.
func (c *Client) Update(ctx context.Context, space Space, id string, version int, parentID, title, body, note string) (Page, error) {
	if c.cfg.Flavor == Cloud {
		in := map[string]any{"id": id, "status": "current", "title": title,
			"body": map[string]string{"representation": "storage", "value": body}, "version": map[string]any{"number": version + 1, "message": note}}
		if parentID != "" {
			in["parentId"] = parentID
		}
		var p v2Page
		if err := c.do(ctx, http.MethodPut, c.api(true, "/pages/"+url.PathEscape(id), nil), in, &p); err != nil {
			return Page{}, err
		}
		return c.fromV2(p, space.Key), nil
	}
	in := map[string]any{"id": id, "type": "page", "title": title, "space": map[string]string{"key": space.Key},
		"version": map[string]any{"number": version + 1, "message": note},
		"body":    map[string]any{"storage": map[string]string{"value": body, "representation": "storage"}}}
	if parentID != "" {
		in["ancestors"] = []map[string]string{{"id": parentID}}
	}
	var p v1Content
	if err := c.do(ctx, http.MethodPut, c.api(false, "/content/"+url.PathEscape(id), nil), in, &p); err != nil {
		return Page{}, err
	}
	return c.fromV1(p), nil
}
