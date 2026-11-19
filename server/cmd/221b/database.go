package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ucesys/221b/server/internal/api"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/kv"
)

// The database (Phase 13, ADR-162): PostgreSQL, the chart's bundled one or
// one the operator runs. A domain whose repository is written keeps its
// records there; the others stay in their ConfigMaps until theirs is.

// databaseWait is how long a start waits for a database that does not
// answer, and databaseMigrate how long the schema's steps may take. The
// chart's startup probe (deployment.yaml) is sized from their sum.
const (
	databaseWait    = 2 * time.Minute
	databaseMigrate = 5 * time.Minute
)

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// wireDatabase opens the database when one is configured, brings its
// schema to this build's, and starts hearing the other replicas.
func (w *wiring) wireDatabase(ctx context.Context) error {
	cfg, log := w.cfg.Database, w.log.With("component", "database")
	if !cfg.Enabled() {
		return nil
	}
	if cfg.AppName == "" {
		host, _ := os.Hostname()
		cfg.AppName = "221b/" + host
	}
	// The bundled database may still be starting, or failing over, when
	// the console does: try for databaseWait before giving up, and let the
	// pod start again after that. The chart's startup probe allows for
	// this wait and the schema's steps after it; the console does not
	// listen until both are done.
	var d *db.DB
	var err error
	deadline := time.Now().Add(databaseWait)
	for first := true; ; first = false {
		octx, cancel := context.WithDeadline(ctx, earlier(time.Now().Add(15*time.Second), deadline))
		d, err = db.Open(octx, cfg, log)
		cancel()
		if err == nil || !time.Now().Add(5*time.Second).Before(deadline) {
			break
		}
		if first {
			log.Warn("the database does not answer yet; trying for two minutes", "host", cfg.Host, "err", err)
		}
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err != nil {
		return fmt.Errorf("the database (-db-host, database.* in the chart) did not answer in %s of trying, so the console stops here and is started again: %w", databaseWait, err)
	}
	mctx, cancel := context.WithTimeout(ctx, databaseMigrate)
	defer cancel()
	n, err := d.Migrate(mctx)
	if err != nil {
		d.Close()
		return fmt.Errorf("bringing the database's schema up to date: %w", err)
	}
	version, err := d.SchemaVersion(mctx)
	if err != nil {
		d.Close()
		return err
	}
	log.Info("the database is open", "host", cfg.Host, "database", cfg.Name, "schema", version, "steps_applied", n)
	go d.Listen(ctx)
	go d.Watch(ctx, 10*time.Second)
	w.database, w.schemaVersion = d, version
	return nil
}

// databaseState is what GET /api/v1/database shows.
func (w *wiring) databaseState() api.DatabaseState {
	return api.DatabaseState{Health: w.database.Health(), SchemaVersion: w.schemaVersion, Domains: w.inDatabase}
}

// databaseGauges are the database's metrics.
func (w *wiring) databaseGauges() []api.Gauge {
	if w.database == nil {
		return nil
	}
	up := 0.0
	if w.database.Health().Available {
		up = 1
	}
	stat := w.database.Pool().Stat()
	return []api.Gauge{
		{Name: "b221_database_up", Help: "1 when the database answered the last time it was asked.", Value: up},
		{Name: "b221_database_schema_version", Help: "The newest step of the schema this replica applied or found.", Value: float64(w.schemaVersion)},
		{Name: "b221_database_connections", Help: "Connections this replica has open to the database.", Value: float64(stat.TotalConns())},
		{Name: "b221_database_connections_max", Help: "Most connections this replica opens to the database.", Value: float64(stat.MaxConns())},
	}
}

// inScopes keeps the documents under the keys named (key to scope) as
// scopes of the database's settings table, and every other key in b as
// before; without a database it is b. What b held under those keys is
// copied once. These are a service's singletons, read whole and written
// rarely (model rule 14), so they need no repository beyond the scope.
func (w *wiring) inScopes(ctx context.Context, b kv.Backend, scopes map[string]string) kv.Backend {
	if w.database == nil {
		return b
	}
	if b == nil {
		b = &kv.Memory{}
	}
	s := db.NewScopeKV(w.database, scopes)
	for _, id := range s.ImportIDs() {
		w.claimImport(id)
	}
	if _, inMemory := b.(*kv.Memory); !inMemory {
		ictx, cancel := context.WithTimeout(ctx, time.Minute)
		copied, err := s.Import(ictx, b, "configmap")
		cancel()
		log := w.log.With("component", "database")
		if err != nil {
			log.Warn("copying settings from their ConfigMap into the database failed; the next start tries again", "err", err)
		} else if len(copied) > 0 {
			log.Info("copied settings from their ConfigMap into the database", "keys", copied)
		}
	}
	keys := map[string]kv.Backend{}
	for key := range scopes {
		keys[key] = s
	}
	return &kv.Keyed{Keys: keys, Rest: b}
}

// movedDoc is one of a service's documents kept as rows of the model's
// tables (db.Doc), which can copy what the ConfigMap held.
type movedDoc interface {
	kv.Backend
	Import(ctx context.Context, from kv.Backend, source string) (bool, error)
	// ImportID is the name the copy is recorded under.
	ImportID() string
}

// claimImport notes that a record set is copied under id. Two record
// sets under one id would share one record of having been copied, and the
// second would never be copied (the fifth cluster test, F2: the jobs' and
// Automation's runs.json), so the console does not start when that
// happens: importClash is checked once everything is wired.
func (w *wiring) claimImport(id string) {
	if w.importIDs == nil {
		w.importIDs = map[string]bool{}
	}
	if w.importIDs[id] {
		w.importClash = append(w.importClash, id)
	}
	w.importIDs[id] = true
}

// inTables keeps the documents under the keys named in the database, each
// through its own mapping to tables, and every other key in b as before;
// without a database it is b. make is given the database and returns the
// mappings, so nothing is made without one. What b held under those keys
// is copied once.
func (w *wiring) inTables(ctx context.Context, b kv.Backend, make func(d *db.DB) map[string]movedDoc) kv.Backend {
	if w.database == nil {
		return b
	}
	if b == nil {
		b = &kv.Memory{}
	}
	log := w.log.With("component", "database")
	_, inMemory := b.(*kv.Memory)
	keys := map[string]kv.Backend{}
	for key, doc := range make(w.database) {
		keys[key] = doc
		w.claimImport(doc.ImportID())
		if inMemory {
			continue
		}
		ictx, cancel := context.WithTimeout(ctx, time.Minute)
		copied, err := doc.Import(ictx, b, "configmap")
		cancel()
		if err != nil {
			log.Warn("copying records from their ConfigMap into the database failed; the next start tries again", "key", key, "err", err)
		} else if copied {
			log.Info("copied records from their ConfigMap into the database", "key", key)
		}
	}
	return &kv.Keyed{Keys: keys, Rest: b}
}
