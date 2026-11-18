// Package imagecheck says whether a newer image is published for an image
// a Helm release runs (HA-21 to HA-23, ADR-154): the newest tag of the
// running tag's form in its registry, or, for a tag that is not a version
// but is pinned by digest, whether the tag now points at another image.
//
// Checks never block a request: Check answers from what is kept and asks
// the registry in the background, a bounded number at a time, so the page
// asks again for what was pending. What a registry answered is kept per
// repository for hours; a registry that could not be reached is asked
// again after an hour, and says only "could not check", so an air-gapped
// cluster sees no error.
package imagecheck

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// States of a check.
const (
	// StateNewer: a newer tag of the same form, or the tag moved.
	StateNewer = "newer"
	// StateCurrent: nothing newer of the same form.
	StateCurrent = "current"
	// StatePending: the registry is being asked.
	StatePending = "pending"
	// StateUnknown: the registry could not be asked or wants credentials.
	StateUnknown = "unknown"
	// StateSkipped: nothing to compare, as with latest and no digest, or
	// an image pinned by digest alone.
	StateSkipped = "skipped"
)

// Result is what a check says about one image.
type Result struct {
	State string `json:"state"`
	// Newer is the newest tag of the running tag's form; for a tag that
	// moved, the tag itself, with Digest the digest it points at now.
	Newer  string `json:"newer,omitempty"`
	Digest string `json:"digest,omitempty"`
	// Reason says why the check could not be made or was skipped.
	Reason  string     `json:"reason,omitempty"`
	Checked *time.Time `json:"checked,omitempty"`
}

// Options configure a Checker; the zero values are the defaults.
type Options struct {
	Client *Client
	// TTL is how long a registry's answer is kept: six hours.
	TTL time.Duration
	// FailTTL is how long a failed check is kept before it is tried
	// again: one hour.
	FailTTL time.Duration
	// Workers bounds the registries asked at once: four.
	Workers int
	// Timeout bounds one repository's check: twenty seconds.
	Timeout time.Duration
	// Active says whether this replica may ask registries: the leader
	// (ADR-70). nil is always.
	Active func() bool
	Now    func() time.Time
	Logger *slog.Logger
}

// maxQueued bounds the checks waiting for a worker; more are asked for on
// a later look.
const maxQueued = 512

// Checker keeps what registries answered.
type Checker struct {
	o    Options
	sem  chan struct{}
	mu   sync.Mutex
	kept map[string]*entry
}

type entry struct {
	tags     []string
	digest   string
	err      error
	when     time.Time
	inFlight bool
}

// New makes a Checker.
func New(o Options) *Checker {
	if o.Client == nil {
		o.Client = &Client{}
	}
	if o.Client.HTTP == nil {
		cp := *o.Client
		cp.HTTP = defaultHTTP()
		o.Client = &cp
	}
	if o.TTL <= 0 {
		o.TTL = 6 * time.Hour
	}
	if o.FailTTL <= 0 {
		o.FailTTL = time.Hour
	}
	if o.Workers <= 0 {
		o.Workers = 4
	}
	if o.Timeout <= 0 {
		o.Timeout = 20 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	return &Checker{o: o, sem: make(chan struct{}, o.Workers), kept: map[string]*entry{}}
}

// Check says what is known about an image, from the parts of its
// reference, and starts asking its registry when nothing is kept or what
// is kept is old.
func (c *Checker) Check(registry, repository, tag, digest string) Result {
	host, repo := Host(registry), Repository(registry, repository)
	switch {
	case tag == "" && digest != "":
		return Result{State: StateSkipped, Reason: "pinned by digest alone"}
	case tag == "":
		tag = "latest"
	}
	if Versioned(tag) {
		e, r := c.look("tags\x00"+host+"/"+repo, func(ctx context.Context) (*entry, error) {
			tags, err := c.o.Client.Tags(ctx, host, repo)
			return &entry{tags: tags}, err
		})
		if e == nil {
			return r
		}
		if n := Newest(tag, e.tags); n != "" {
			r.State, r.Newer = StateNewer, n
		} else {
			r.State = StateCurrent
		}
		return r
	}
	if digest == "" {
		return Result{State: StateSkipped, Reason: "the tag is not a version, and no digest says what runs"}
	}
	e, r := c.look("digest\x00"+host+"/"+repo+":"+tag, func(ctx context.Context) (*entry, error) {
		d, err := c.o.Client.Digest(ctx, host, repo, tag)
		return &entry{digest: d}, err
	})
	if e == nil {
		return r
	}
	if e.digest != digest {
		r.State, r.Newer, r.Digest = StateNewer, tag, e.digest
	} else {
		r.State = StateCurrent
	}
	return r
}

// look returns the kept answer under the key, or nil with the pending or
// failed result; it starts a check when none is kept or it is old.
func (c *Checker) look(key string, fetch func(ctx context.Context) (*entry, error)) (*entry, Result) {
	now := c.o.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.kept[key]
	fresh := e != nil && !e.when.IsZero() && (e.err == nil && now.Sub(e.when) < c.o.TTL || e.err != nil && now.Sub(e.when) < c.o.FailTTL)
	if !fresh && (e == nil || !e.inFlight) {
		c.start(key, e, fetch)
		if e == nil {
			e = c.kept[key]
		}
	}
	if e == nil || e.when.IsZero() {
		return nil, Result{State: StatePending}
	}
	when := e.when
	if e.err != nil {
		return nil, Result{State: StateUnknown, Reason: e.err.Error(), Checked: &when}
	}
	// A copy: the background check replaces the entry under the lock.
	cp := *e
	return &cp, Result{Checked: &when}
}

// start asks in the background; c.mu is held. Nothing is asked on a
// replica that does not lead, nor beyond maxQueued at once.
func (c *Checker) start(key string, e *entry, fetch func(ctx context.Context) (*entry, error)) {
	if c.o.Active != nil && !c.o.Active() {
		return
	}
	queued := 0
	for _, x := range c.kept {
		if x.inFlight {
			queued++
		}
	}
	if queued >= maxQueued {
		return
	}
	if e == nil {
		e = &entry{}
		c.kept[key] = e
	}
	e.inFlight = true
	go func() {
		c.sem <- struct{}{}
		defer func() { <-c.sem }()
		ctx, cancel := context.WithTimeout(context.Background(), c.o.Timeout)
		defer cancel()
		got, err := fetch(ctx)
		if got == nil {
			got = &entry{}
		}
		if err != nil && errors.Is(err, context.DeadlineExceeded) {
			err = errors.New("the registry did not answer in time")
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		cur := c.kept[key]
		if cur == nil {
			return
		}
		if err != nil {
			c.o.Logger.Debug("image check failed", "check", strings.ReplaceAll(key, "\x00", " "), "err", err)
			// A failure keeps what an earlier check found: the page
			// says the newer image it knew rather than nothing.
			if !cur.when.IsZero() && cur.err == nil {
				cur.when, cur.inFlight = c.o.Now(), false
				return
			}
			*cur = entry{err: err, when: c.o.Now()}
			return
		}
		got.when = c.o.Now()
		*cur = *got
	}()
}

// defaultHTTP is a client that goes through the proxy the environment
// names (HTTPS_PROXY), as the console's other outbound requests do.
func defaultHTTP() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 15 * time.Second
	return &http.Client{Transport: t, Timeout: 30 * time.Second}
}
