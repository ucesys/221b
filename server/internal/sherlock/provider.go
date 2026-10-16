package sherlock

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Sherlock's model runs behind one of four kinds of API, each reached
// directly or through an enterprise gateway at an address the
// administrator gives (Phase 6 review):
//
//   - anthropic: the Claude Messages API, or anything that speaks it.
//   - openai: the Chat Completions API, which OpenAI, Azure OpenAI, Gemini,
//     Mistral, vLLM, Ollama and most AI gateways (LiteLLM, Portkey, Kong)
//     speak.
//   - bedrock: Amazon Bedrock's Converse API, signed with AWS credentials
//     (SigV4) or, behind a gateway, with the gateway's header.
//   - vertex: Google Vertex AI with a Google identity: Claude models
//     through their Messages API there, others through Vertex's
//     Chat Completions endpoint.
//
// The conversation keeps the Messages API's shape; the other kinds are
// translated to and from it, so the tools, the history and the audit are
// the same whichever answers.

// Kinds of provider.
const (
	KindAnthropic = "anthropic"
	KindOpenAI    = "openai"
	KindBedrock   = "bedrock"
	KindVertex    = "vertex"
)

// Kinds of authentication.
const (
	// AuthHeader sends the credential in a header, such as x-api-key or
	// Authorization: Bearer.
	AuthHeader = "header"
	// AuthAWS signs each request with AWS credentials (SigV4): the stored
	// credential, or the pod's identity (IRSA, EKS Pod Identity) without one.
	AuthAWS = "aws"
	// AuthGoogle sends a Google access token: from the stored service
	// account key, or the pod's identity (Workload Identity) without one.
	AuthGoogle = "google"
	// AuthNone sends nothing, for a gateway that authenticates otherwise.
	AuthNone = "none"
)

// Provider is where the model runs and how the console reaches it.
type Provider struct {
	Kind string `json:"kind"`
	// Endpoint is the API's base address, or a gateway's. Empty is the
	// provider's own.
	Endpoint string `json:"endpoint,omitempty"`
	Auth     string `json:"auth,omitempty"`
	// AuthHeaderName and AuthPrefix say how a header credential is sent:
	// x-api-key with no prefix, Authorization with "Bearer ", api-key, …
	AuthHeaderName string `json:"authHeader,omitempty"`
	AuthPrefix     string `json:"authPrefix,omitempty"`
	// Headers are sent with every request; they are not secret.
	Headers map[string]string `json:"headers,omitempty"`
	// Query is added to every request's address, such as
	// api-version=2024-10-21 for Azure OpenAI.
	Query string `json:"query,omitempty"`
	// Region is the AWS region, or the Google Cloud location.
	Region string `json:"region,omitempty"`
	// Project is the Google Cloud project.
	Project string `json:"project,omitempty"`
	// CACert is a PEM bundle the endpoint's certificate must chain to, for
	// a gateway with a private certificate authority; empty is the
	// system's.
	CACert string `json:"caCert,omitempty"`

	// custom says the endpoint was given, not the provider's own.
	custom bool
}

// Normalized fills in the defaults of the provider's kind.
func (p Provider) Normalized(defaultBase string) Provider {
	if p.Endpoint != "" {
		p.custom = true
	}
	if p.Kind == "" {
		p.Kind = KindAnthropic
	}
	if p.Auth == "" {
		switch p.Kind {
		case KindBedrock:
			p.Auth = AuthAWS
		case KindVertex:
			p.Auth = AuthGoogle
		default:
			p.Auth = AuthHeader
		}
	}
	if p.Auth == AuthHeader && p.AuthHeaderName == "" {
		if p.Kind == KindAnthropic {
			p.AuthHeaderName = "x-api-key"
		} else {
			p.AuthHeaderName, p.AuthPrefix = "Authorization", "Bearer "
		}
	}
	if p.Endpoint == "" {
		switch p.Kind {
		case KindAnthropic:
			p.Endpoint = defaultBase
			if p.Endpoint == "" {
				p.Endpoint = DefaultBaseURL
			}
		case KindOpenAI:
			p.Endpoint = "https://api.openai.com/v1"
		case KindBedrock:
			p.Endpoint = "https://bedrock-runtime." + p.Region + ".amazonaws.com"
		case KindVertex:
			if p.Region == "" || p.Region == "global" {
				p.Endpoint = "https://aiplatform.googleapis.com"
			} else {
				p.Endpoint = "https://" + p.Region + "-aiplatform.googleapis.com"
			}
		}
	}
	p.Endpoint = strings.TrimSuffix(p.Endpoint, "/")
	return p
}

// NeedsKey says whether the provider needs a stored credential: an API key
// for header authentication. AWS and Google fall back to the pod's
// identity.
func (p Provider) NeedsKey() bool {
	p = p.Normalized("")
	return p.Auth == AuthHeader
}

// Name names the provider in messages.
func (p Provider) Name() string {
	switch p.Kind {
	case KindOpenAI:
		return "the model's API"
	case KindBedrock:
		return "Amazon Bedrock"
	case KindVertex:
		return "Vertex AI"
	}
	if p.custom && !strings.HasPrefix(p.Endpoint, DefaultBaseURL) {
		return "the model's API"
	}
	return "the Claude API"
}

var (
	reHeaderName = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_|~-]{1,64}$`)
	reRegion     = regexp.MustCompile(`^[a-z0-9-]{0,40}$`)
	reProject    = regexp.MustCompile(`^[a-z0-9-]{0,64}$`)
)

// Validate reports what is wrong with the provider's settings.
func (p Provider) Validate() error {
	switch p.Kind {
	case "", KindAnthropic, KindOpenAI, KindBedrock, KindVertex:
	default:
		return fmt.Errorf("the provider %q is not one Sherlock speaks: anthropic, openai, bedrock or vertex", p.Kind)
	}
	switch p.Auth {
	case "", AuthHeader, AuthAWS, AuthGoogle, AuthNone:
	default:
		return fmt.Errorf("the authentication %q is not one Sherlock knows: header, aws, google or none", p.Auth)
	}
	if p.Auth == AuthAWS && p.Kind != "" && p.Kind != KindBedrock {
		return errors.New("AWS signatures are for Amazon Bedrock only")
	}
	if p.Auth == AuthGoogle && p.Kind != "" && p.Kind != KindVertex {
		return errors.New("Google identities are for Vertex AI only")
	}
	if p.Endpoint != "" {
		u, err := url.Parse(p.Endpoint)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
			return errors.New("the endpoint must be an http or https address without a user, query or fragment; put query parameters in Query")
		}
	}
	if (p.Kind == KindBedrock || p.Kind == KindVertex) && p.Endpoint == "" && p.Region == "" {
		return errors.New("give the region (or the endpoint of your gateway)")
	}
	if p.Kind == KindVertex && p.Project == "" {
		return errors.New("give the Google Cloud project")
	}
	if !reRegion.MatchString(p.Region) || !reProject.MatchString(p.Project) {
		return errors.New("the region or project has characters it cannot have")
	}
	if p.AuthHeaderName != "" && !reHeaderName.MatchString(p.AuthHeaderName) {
		return errors.New("the credential's header name is not a valid header name")
	}
	if strings.ContainsAny(p.AuthPrefix, "\r\n") || len(p.AuthPrefix) > 32 {
		return errors.New("the credential's prefix is not valid")
	}
	if len(p.Headers) > 20 {
		return errors.New("at most 20 extra headers")
	}
	for k, v := range p.Headers {
		if !reHeaderName.MatchString(k) || strings.ContainsAny(v, "\r\n") || len(v) > 1024 {
			return fmt.Errorf("the header %q is not valid", k)
		}
		switch strings.ToLower(k) {
		case "host", "content-length", "content-type", "transfer-encoding", "connection":
			return fmt.Errorf("the header %q is set by the console", k)
		}
	}
	if p.Query != "" {
		if _, err := url.ParseQuery(p.Query); err != nil || len(p.Query) > 512 {
			return errors.New("the query is not valid: write it as name=value&name=value")
		}
	}
	if p.CACert != "" {
		pool := x509.NewCertPool()
		if len(p.CACert) > 64<<10 || !pool.AppendCertsFromPEM([]byte(p.CACert)) {
			return errors.New("the CA certificate is not a PEM certificate bundle")
		}
	}
	return nil
}

// Delta is what arrives while a message streams: a piece of text, or the
// start of a tool call.
type Delta struct {
	Text string
	// Tool names a tool the model has started to call.
	Tool string
}

// Client calls the model's API.
type Client struct {
	// BaseURL is the Claude API's address when the provider gives none.
	BaseURL string
	// Key returns the stored credential: an API key, AWS credentials or a
	// Google service account key, as JSON for the latter two. It is read
	// for every request, so a new one takes effect at once.
	Key func() string
	// Provider returns the provider in force; nil is the Claude API at
	// BaseURL.
	Provider func() Provider
	HTTP     *http.Client
	// Retries is how many times a retryable failure before any output is
	// tried again.
	Retries int
	// Backoff is the first wait before a retry; it doubles.
	Backoff time.Duration
	// Now is the clock, for signatures and tokens.
	Now func() time.Time
	// Ambient finds the pod's AWS or Google identity; tests replace it.
	Ambient *Ambient

	mu      sync.Mutex
	clients map[string]*http.Client
	aws     *awsCreds
	google  *googleToken
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) provider() Provider {
	var p Provider
	if c.Provider != nil {
		p = c.Provider()
	}
	return p.Normalized(c.BaseURL)
}

// httpClient is the client for the provider: the configured one, or one
// that trusts the provider's CA bundle.
func (c *Client) httpClient(p Provider) (*http.Client, error) {
	base := c.HTTP
	if base == nil {
		base = http.DefaultClient
	}
	if p.CACert == "" {
		return base, nil
	}
	sum := sha256.Sum256([]byte(p.CACert))
	key := hex.EncodeToString(sum[:])
	c.mu.Lock()
	defer c.mu.Unlock()
	if hc := c.clients[key]; hc != nil {
		return hc, nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(p.CACert)) {
		return nil, errors.New("the provider's CA certificate is not PEM")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	hc := &http.Client{Transport: tr, Timeout: base.Timeout}
	if c.clients == nil {
		c.clients = map[string]*http.Client{}
	}
	c.clients[key] = hc
	return hc, nil
}

// Stream sends the request and calls onDelta as the answer arrives. It
// returns the whole message once it is complete.
func (c *Client) Stream(ctx context.Context, req Request, onDelta func(Delta)) (Response, error) {
	p := c.provider()
	wait := c.Backoff
	if wait <= 0 {
		wait = time.Second
	}
	for attempt := 0; ; attempt++ {
		resp, started, err := c.once(ctx, p, req, onDelta)
		var ae *APIError
		if err != nil && !started && attempt < c.Retries && errors.As(err, &ae) && ae.Retryable() {
			select {
			case <-ctx.Done():
				return Response{}, ctx.Err()
			case <-time.After(wait):
			}
			wait *= 2
			continue
		}
		return resp, err
	}
}

// reader reads a provider's streamed answer.
type reader func(r io.Reader, onDelta func(Delta)) (Response, bool, error)

func (c *Client) once(ctx context.Context, p Provider, req Request, onDelta func(Delta)) (Response, bool, error) {
	var (
		addr string
		body []byte
		read reader
		err  error
	)
	switch p.Kind {
	case KindOpenAI:
		addr, body, read, err = buildOpenAI(p, p.Endpoint, req)
	case KindBedrock:
		addr, body, read, err = buildBedrock(p, req)
	case KindVertex:
		addr, body, read, err = buildVertex(p, req)
	default:
		addr, body, read, err = buildAnthropic(p, req)
	}
	if err != nil {
		return Response{}, false, err
	}
	if p.Query != "" {
		if strings.Contains(addr, "?") {
			addr += "&" + p.Query
		} else {
			addr += "?" + p.Query
		}
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, addr, bytes.NewReader(body))
	if err != nil {
		return Response{}, false, err
	}
	hr.Header.Set("Content-Type", "application/json")
	if p.Kind == KindBedrock {
		hr.Header.Set("Accept", "application/vnd.amazon.eventstream")
	} else {
		hr.Header.Set("Accept", "text/event-stream")
	}
	if p.Kind == KindAnthropic {
		hr.Header.Set("anthropic-version", APIVersion)
	}
	for k, v := range p.Headers {
		hr.Header.Set(k, v)
	}
	if err := c.authorize(ctx, p, hr, body); err != nil {
		return Response{}, false, err
	}
	hc, err := c.httpClient(p)
	if err != nil {
		return Response{}, false, err
	}
	res, err := hc.Do(hr)
	if err != nil {
		return Response{}, false, fmt.Errorf("reaching %s: %w", p.Name(), err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		e := apiError(res.StatusCode, b)
		e.Provider = p.Name()
		return Response{}, false, e
	}
	out, started, err := read(res.Body, onDelta)
	var ae *APIError
	if errors.As(err, &ae) && ae.Provider == "" {
		ae.Provider = p.Name()
	}
	return out, started, err
}

// authorize adds the credential to a request.
func (c *Client) authorize(ctx context.Context, p Provider, hr *http.Request, body []byte) error {
	key := ""
	if c.Key != nil {
		key = c.Key()
	}
	switch p.Auth {
	case AuthHeader:
		if key != "" {
			hr.Header.Set(p.AuthHeaderName, p.AuthPrefix+key)
		}
	case AuthAWS:
		creds, err := c.awsCredentials(ctx, key, p.Region)
		if err != nil {
			return err
		}
		region := p.Region
		if region == "" {
			region = regionOf(hr.URL.Host)
		}
		signV4(hr, body, creds, region, "bedrock", c.now())
	case AuthGoogle:
		tok, err := c.googleAccessToken(ctx, key)
		if err != nil {
			return err
		}
		hr.Header.Set("Authorization", "Bearer "+tok)
	}
	return nil
}

// regionOf reads an AWS region from an endpoint such as
// bedrock-runtime.eu-west-1.amazonaws.com.
func regionOf(host string) string {
	parts := strings.Split(host, ".")
	if len(parts) >= 4 && parts[len(parts)-2] == "amazonaws" {
		return parts[len(parts)-3]
	}
	return "us-east-1"
}

// buildAnthropic is a Messages API request: to <endpoint>/v1/messages, or
// to the endpoint itself when it already ends in /messages.
func buildAnthropic(p Provider, req Request) (string, []byte, reader, error) {
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return "", nil, nil, err
	}
	addr := p.Endpoint
	if !strings.HasSuffix(addr, "/messages") {
		addr += "/v1/messages"
	}
	return addr, body, readStream, nil
}

// buildVertex is a request to Vertex AI: a Claude model through its
// Messages API there (streamRawPredict, with the Vertex version in the
// body instead of the model), any other through Vertex's Chat Completions
// endpoint.
func buildVertex(p Provider, req Request) (string, []byte, reader, error) {
	loc := p.Region
	if loc == "" {
		loc = "global"
	}
	base := p.Endpoint + "/v1/projects/" + url.PathEscape(p.Project) + "/locations/" + url.PathEscape(loc)
	if !strings.HasPrefix(req.Model, "claude") {
		return buildOpenAI(p, base+"/endpoints/openapi", req)
	}
	req.Stream = true
	raw, err := json.Marshal(req)
	if err != nil {
		return "", nil, nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", nil, nil, err
	}
	delete(m, "model")
	m["anthropic_version"] = json.RawMessage(`"vertex-2023-10-16"`)
	body, err := json.Marshal(m)
	if err != nil {
		return "", nil, nil, err
	}
	return base + "/publishers/anthropic/models/" + url.PathEscape(req.Model) + ":streamRawPredict", body, readStream, nil
}
