package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/db"
)

// A start without the database (DB-67, ADR-166).
//
// A console given a database that does not answer within the wait starts
// without it: it serves what does not need it (sign-in, the local cluster
// read live), and stops itself when the database answers, to be started
// again. The schema's steps, the one-time copies and the removals then
// run at that next start, on the path every start takes.
//
// What makes it safe is that such a console keeps no records at all:
//
//  1. its database handle refuses every call (db.OpenWithout), also once
//     the database is back, so nothing is read from the database and
//     nothing written to it;
//  2. as itself, it writes no ConfigMap and no Secret (writesNoRecords),
//     but the local accounts' Secret and the session key's, which sign-in
//     needs and which never move;
//  3. no copy and no removal runs (the wiring's moveFromSecrets, inTables,
//     inScopes, removeCopied and the copies beside them), and none of the
//     work that keeps records is started (wiring.keepsRecords).
//
// The first two hold whatever a service does with an error; the third
// keeps the log quiet and spares the work.

// withoutProbe is how often a console started without its database asks
// whether it answers.
const withoutProbe = 5 * time.Second

// keepsRecords says whether this console may copy, remove and keep
// records: false for one started without its database.
func (w *wiring) keepsRecords() bool { return !w.without }

// startWithoutTheDatabase is wireDatabase's other end: the database did
// not answer within the wait, for the reason given.
func (w *wiring) startWithoutTheDatabase(ctx context.Context, cfg db.Config, wait time.Duration, why error, log *slog.Logger) error {
	d, err := db.OpenWithout(cfg, log)
	if err != nil {
		return err
	}
	log.Error("the database did not answer in the time a start waits for it: this console starts WITHOUT it. Sign-in and the pages read live from the local cluster work; everything kept in the database says it is unavailable; nothing is copied, removed or migrated; and the console stops and is started again as soon as the database answers (database.startWithoutIt in the chart, -db-start-without)",
		"host", cfg.Host, "waited", wait.String(), "err", why)
	w.database, w.without = d, true
	// What the saved settings and the saved connections say cannot be
	// read, so nothing that they allow is allowed: Watson changes nothing
	// in the local cluster (its saved environment and the two-person rule
	// are among what cannot be read), and wireSettings puts the cautious
	// choice where a setting is missing.
	w.cfg.LocalMode = string(clusters.ModeRead)
	// From here on every client made of w.restCfg is refused a write of a
	// ConfigMap or a Secret; the accounts' Secret and the session key's
	// are opened with the configuration as it was (wireAuth).
	w.signInCfg, w.restCfg = w.restCfg, writesNoRecords(w.restCfg)
	if w.restart == nil {
		w.restart = make(chan struct{})
	}
	go w.restartWhenTheDatabaseAnswers(ctx, cfg, log)
	return nil
}

// restartWhenTheDatabaseAnswers asks the database, through a handle of its
// own, whether it is there, and has the console stop when it is: the
// handle the services hold never finds out (db.OpenWithout).
func (w *wiring) restartWhenTheDatabaseAnswers(ctx context.Context, cfg db.Config, log *slog.Logger) {
	every := w.withoutProbe
	if every <= 0 {
		every = withoutProbe
	}
	quiet := slog.New(slog.DiscardHandler)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		octx, cancel := context.WithTimeout(ctx, max(2*every, 10*time.Second))
		d, err := db.Open(octx, cfg, quiet)
		cancel()
		if err != nil {
			continue
		}
		d.Close()
		log.Warn("the database answers: this console was started without it and stops now, to be started again with it", "host", cfg.Host)
		close(w.restart)
		return
	}
}

// recordPath is the path of a ConfigMap or a Secret, or of the ConfigMaps
// or Secrets of a namespace, in the Kubernetes API.
var recordPath = regexp.MustCompile(`^/api/v1/namespaces/[^/]+/(configmaps|secrets)(/[^/]+)?$`)

// writesNoRecords is cfg with every write of a ConfigMap or a Secret made
// as the console itself refused before it is sent: the console's records
// that are not in the database are in those, and a console started
// without its database keeps none (DB-67). A write made as a person (a
// ConfigMap edited on its page: Impersonate-User) is that person's, and
// passes; so does every read, and every other kind.
func writesNoRecords(cfg *rest.Config) *rest.Config {
	out := rest.CopyConfig(cfg)
	out.Wrap(func(rt http.RoundTripper) http.RoundTripper { return noRecordWrites{rt} })
	return out
}

type noRecordWrites struct{ rt http.RoundTripper }

// errNoRecordWrites is what a refused write reads.
const errNoRecordWrites = "this console was started without its database and keeps no records until it is started again: it does not write a ConfigMap or a Secret as itself"

func (n noRecordWrites) RoundTrip(r *http.Request) (*http.Response, error) {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return n.rt.RoundTrip(r)
	}
	if r.Header.Get("Impersonate-User") != "" || !recordPath.MatchString(r.URL.Path) {
		return n.rt.RoundTrip(r)
	}
	if r.Body != nil {
		_ = r.Body.Close()
	}
	body, _ := json.Marshal(metav1.Status{
		TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
		Status:   metav1.StatusFailure, Code: http.StatusForbidden, Reason: metav1.StatusReasonForbidden,
		Message: errNoRecordWrites,
	})
	return &http.Response{
		Status: "403 Forbidden", StatusCode: http.StatusForbidden, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)), Request: r,
	}, nil
}
