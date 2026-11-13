package imagecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// The registry side (HA-21): the OCI distribution API's tag list and a
// manifest's digest, asked anonymously. A registry that wants a bearer
// token for a public repository names where to get one in its
// WWW-Authenticate challenge (ghcr.io, Docker Hub, quay.io, Harbor, most
// others); the token is asked for with no credentials and kept only for
// the request. No pull secret is read or sent: a private repository
// answers "needs credentials" and is not checked.

const (
	// maxTagPages bounds the pages of a tag list that are read.
	maxTagPages = 20
	// maxBody bounds each answer read: a page of tags, a token.
	maxBody = 4 << 20
	// pageSize asks for large pages; registries cap it themselves.
	pageSize = 1000
)

// Errors a check ends with, said quietly on the page as "could not check".
var (
	ErrCredentials = errors.New("the registry wants credentials")
	ErrNotFound    = errors.New("the registry does not know the repository")
	ErrLimited     = errors.New("the registry limits requests; asked again later")
)

// Client asks registries over HTTPS.
type Client struct {
	HTTP      *http.Client
	UserAgent string
}

// Host is the registry's host as the API is asked at: Docker Hub's
// images, named without a registry or as docker.io, are served from
// registry-1.docker.io.
func Host(registry string) string {
	switch registry {
	case "", "docker.io", "index.docker.io", "registry.hub.docker.com":
		return "registry-1.docker.io"
	}
	return registry
}

// Repository is the repository's path at its host: an official Docker
// Hub image (nginx) is library/nginx.
func Repository(registry, repo string) string {
	if Host(registry) == "registry-1.docker.io" && !strings.Contains(repo, "/") {
		return "library/" + repo
	}
	return repo
}

// Tags lists a repository's tags, following the pages the registry
// links (at most maxTagPages).
func (c *Client) Tags(ctx context.Context, host, repo string) ([]string, error) {
	next := fmt.Sprintf("https://%s/v2/%s/tags/list?n=%d", host, repo, pageSize)
	var out []string
	token := ""
	for page := 0; next != "" && page < maxTagPages; page++ {
		res, tok, err := c.get(ctx, http.MethodGet, next, repo, token, nil)
		if err != nil {
			return nil, err
		}
		token = tok
		var body struct {
			Tags []string `json:"tags"`
		}
		err = json.NewDecoder(io.LimitReader(res.Body, maxBody)).Decode(&body)
		link := res.Header.Get("Link")
		res.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("reading the tag list: %w", err)
		}
		out = append(out, body.Tags...)
		next = nextPage(next, link)
	}
	return out, nil
}

// Digest is the digest a tag points at now: the image index's for a
// multi-platform image, as a pod spec pinned by digest names it.
func (c *Client) Digest(ctx context.Context, host, repo, tag string) (string, error) {
	accept := []string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}
	res, _, err := c.get(ctx, http.MethodHead, fmt.Sprintf("https://%s/v2/%s/manifests/%s", host, repo, url.PathEscape(tag)), repo, "", accept)
	if err != nil {
		return "", err
	}
	res.Body.Close()
	d := res.Header.Get("Docker-Content-Digest")
	if !strings.HasPrefix(d, "sha256:") {
		return "", errors.New("the registry did not say the tag's digest")
	}
	return d, nil
}

// get asks once, and again with an anonymous token when the registry
// challenges for one; it returns the token for the next page.
func (c *Client) get(ctx context.Context, method, u, repo, token string, accept []string) (*http.Response, string, error) {
	res, err := c.do(ctx, method, u, token, accept)
	if err != nil {
		return nil, "", err
	}
	if res.StatusCode == http.StatusUnauthorized && token == "" {
		challenge := res.Header.Get("WWW-Authenticate")
		res.Body.Close()
		token, err = c.token(ctx, challenge, repo)
		if err != nil {
			return nil, "", err
		}
		if res, err = c.do(ctx, method, u, token, accept); err != nil {
			return nil, "", err
		}
	}
	if err := status(res); err != nil {
		res.Body.Close()
		return nil, "", err
	}
	return res, token, nil
}

func (c *Client) do(ctx context.Context, method, u, token string, accept []string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for _, a := range accept {
		req.Header.Add("Accept", a)
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	res, err := c.client().Do(req)
	if err != nil {
		// The address only: a URL error carries nothing secret, but the
		// message is shorter without the request.
		var ue *url.Error
		if errors.As(err, &ue) {
			return nil, fmt.Errorf("the registry could not be reached: %w", ue.Err)
		}
		return nil, fmt.Errorf("the registry could not be reached: %w", err)
	}
	return res, nil
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func status(res *http.Response) error {
	switch {
	case res.StatusCode >= 200 && res.StatusCode < 300:
		return nil
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return ErrCredentials
	case res.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case res.StatusCode == http.StatusTooManyRequests:
		return ErrLimited
	}
	return fmt.Errorf("the registry answered %s", res.Status)
}

// token asks the realm of a Bearer challenge for an anonymous pull token.
// A Basic challenge wants credentials, which the check never sends.
func (c *Client) token(ctx context.Context, challenge, repo string) (string, error) {
	scheme, params := parseChallenge(challenge)
	if !strings.EqualFold(scheme, "bearer") || params["realm"] == "" {
		return "", ErrCredentials
	}
	realm, err := url.Parse(params["realm"])
	if err != nil || realm.Scheme != "https" || realm.Host == "" {
		return "", fmt.Errorf("the registry's token service %q is not an HTTPS address", params["realm"])
	}
	q := realm.Query()
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	scope := params["scope"]
	if scope == "" {
		scope = "repository:" + repo + ":pull"
	}
	q.Set("scope", scope)
	realm.RawQuery = q.Encode()
	res, err := c.do(ctx, http.MethodGet, realm.String(), "", nil)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if err := status(res); err != nil {
		return "", err
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxBody)).Decode(&body); err != nil {
		return "", fmt.Errorf("reading the registry's token: %w", err)
	}
	if body.Token != "" {
		return body.Token, nil
	}
	if body.AccessToken != "" {
		return body.AccessToken, nil
	}
	return "", ErrCredentials
}

// parseChallenge reads `Bearer realm="…",service="…",scope="…"`.
func parseChallenge(h string) (string, map[string]string) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	params := map[string]string{}
	for rest != "" {
		rest = strings.TrimLeft(rest, " ,")
		k, after, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}
		k = strings.ToLower(strings.TrimSpace(k))
		var v string
		if strings.HasPrefix(after, `"`) {
			end := strings.Index(after[1:], `"`)
			if end < 0 {
				v, rest = after[1:], ""
			} else {
				v, rest = after[1:1+end], after[2+end:]
			}
		} else {
			v, rest, _ = strings.Cut(after, ",")
		}
		params[k] = v
	}
	return scheme, params
}

// nextPage reads a Link header's next page (`</v2/…?last=x&n=1000>;
// rel="next"`), resolved against the page asked; a link to another host
// is not followed.
func nextPage(cur, link string) string {
	if link == "" || !strings.Contains(link, `rel="next"`) {
		return ""
	}
	start, end := strings.Index(link, "<"), strings.Index(link, ">")
	if start < 0 || end <= start {
		return ""
	}
	base, err := url.Parse(cur)
	if err != nil {
		return ""
	}
	ref, err := url.Parse(link[start+1 : end])
	if err != nil {
		return ""
	}
	u := base.ResolveReference(ref)
	if u.Host != base.Host || u.Scheme != base.Scheme {
		return ""
	}
	return u.String()
}
