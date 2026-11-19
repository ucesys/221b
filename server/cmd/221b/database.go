package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ucesys/221b/server/internal/api"
	"github.com/ucesys/221b/server/internal/db"
)

// The database (Phase 13, ADR-162): PostgreSQL, the chart's bundled one or
// one the operator runs. A domain whose repository is written keeps its
// records there; the others stay in their ConfigMaps until theirs is.

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
	// the console does: try for two minutes before giving up, and let the
	// pod start again after that.
	var d *db.DB
	var err error
	for try := 0; ; try++ {
		octx, cancel := context.WithTimeout(ctx, 15*time.Second)
		d, err = db.Open(octx, cfg, log)
		cancel()
		if err == nil || try == 23 {
			break
		}
		if try == 0 {
			log.Warn("the database does not answer yet; trying for two minutes", "host", cfg.Host, "err", err)
		}
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err != nil {
		return fmt.Errorf("the database (-db-host, database.* in the chart): %w", err)
	}
	mctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
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
