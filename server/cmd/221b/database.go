package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/api"
	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/kv"
)

// The database (Phase 13, ADR-162): PostgreSQL, the chart's bundled one or
// one the operator runs. A domain whose repository is written keeps its
// records there; the others stay in their ConfigMaps until theirs is.

// databaseWait is how long a start waits for a database that does not
// answer, and databaseMigrate how long the schema's steps may take. The
// chart's startup probe (deployment.yaml) is sized from their sum. After
// the wait the console stops, and is started again: a console does not
// start without its database (DB-02).
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
	// the console does: try for the wait before giving up on it. The
	// chart's startup probe allows for this wait and the schema's steps
	// after it; the console does not listen until both are done.
	wait := w.databaseWait
	if wait <= 0 {
		wait = databaseWait
	}
	var d *db.DB
	var err error
	deadline := time.Now().Add(wait)
	for first := true; ; first = false {
		octx, cancel := context.WithDeadline(ctx, earlier(time.Now().Add(15*time.Second), deadline))
		d, err = db.Open(octx, cfg, log)
		cancel()
		pause := min(5*time.Second, wait/4)
		if err == nil || !time.Now().Add(pause).Before(deadline) {
			break
		}
		if first {
			log.Warn("the database does not answer yet; trying for a while", "host", cfg.Host, "for", wait.String(), "err", err)
		}
		select {
		case <-time.After(pause):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err != nil {
		return fmt.Errorf("the database (-db-host, database.* in the chart) did not answer in %s of trying, so the console stops here and is started again: a console does not start without its database: %w", wait, err)
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
		return fmt.Errorf("reading the database's schema version: %w", err)
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
		if err == nil {
			for _, key := range s.ImportIDs() {
				w.removeCopied(ctx, b, key, key)
			}
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

// copiedAfter names, for a key whose rows hang from another key's rows,
// that other key: a waiting change's conversation is joined to the
// change's row (DB-57), and is not kept without it, so the changes are
// copied first and the conversations only once the changes are.
var copiedAfter = map[string]string{cards.ChatsKey: cards.CardsKey}

// copyOrder is the order a store's documents are copied in: by key, with
// a key that is copied after another put behind it.
func copyOrder(docs map[string]movedDoc) []string {
	keys := make([]string, 0, len(docs))
	for key := range docs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for key, first := range copiedAfter {
		i, j := slices.Index(keys, key), slices.Index(keys, first)
		if i < 0 || j < 0 || i > j {
			continue
		}
		keys = slices.Insert(slices.Delete(keys, i, i+1), j, key)
	}
	return keys
}

// inTables keeps the documents under the keys named in the database, each
// through its own mapping to tables, and every other key in b as before;
// without a database it is b. make is given the database and returns the
// mappings, so nothing is made without one. What b held under those keys
// is copied once, in copyOrder.
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
	docs := make(w.database)
	notCopied := map[string]bool{}
	for _, key := range copyOrder(docs) {
		doc := docs[key]
		keys[key] = doc
		w.claimImport(doc.ImportID())
		if inMemory {
			continue
		}
		if first, ok := copiedAfter[key]; ok && notCopied[first] {
			// Its rows would have nothing to hang from, and would not be kept.
			notCopied[key] = true
			log.Warn("records are not copied from their ConfigMap before the records they belong to; the next start tries again", "key", key, "after", first)
			continue
		}
		ictx, cancel := context.WithTimeout(ctx, time.Minute)
		copied, err := doc.Import(ictx, b, "configmap")
		cancel()
		if err != nil {
			notCopied[key] = true
			log.Warn("copying records from their ConfigMap into the database failed; the next start tries again", "key", key, "err", err)
		} else if copied {
			log.Info("copied records from their ConfigMap into the database", "key", key)
		}
		if err == nil {
			w.removeCopied(ctx, b, doc.ImportID(), key)
		}
	}
	return &kv.Keyed{Keys: keys, Rest: b}
}

// --- A record set leaves its ConfigMap once it is copied (DB-55) ---
//
// Decided by the owner on November 19, 2026, after the sixth cluster test
// (F1): a ConfigMap kept as it was at the copy let a console from before
// the move show changes that had since been approved and run as waiting
// again, and run them a second time. So once a record set's one-time copy
// is recorded in the database, the console removes it from where it was
// kept: at the start that copies it, and at any later start that still
// finds it there (what an earlier build left, or a removal that failed).
// A record set with no record of a copy is left as it is.

// emptied says what a key holds is nothing: what is left of a record set
// in a store that cannot delete a key.
func emptied(b []byte) bool {
	switch strings.TrimSpace(string(b)) {
	case "", "{}", "null":
		return true
	}
	return false
}

// removeCopied removes keys from the store a record set was kept in, when
// the copy recorded under importID is in the database. A store that cannot
// delete a key has it emptied. A failure is logged and does not stop the
// start: the next one tries again.
func (w *wiring) removeCopied(ctx context.Context, from kv.Backend, importID string, keys ...string) {
	if w.database == nil || from == nil {
		return
	}
	log := w.dbLog()
	rctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if copied, err := w.database.Imported(rctx, importID); err != nil || !copied {
		return
	}
	for _, key := range keys {
		b, _, err := from.LoadKey(rctx, key)
		if errors.Is(err, kv.ErrNotFound) || (err == nil && emptied(b)) {
			continue
		}
		if err == nil {
			if d, ok := from.(kv.Deleter); ok {
				err = d.DeleteKey(rctx, key)
			} else {
				_, err = from.SaveKey(rctx, key, []byte("{}"), "")
			}
		}
		if err != nil {
			log.Warn("removing a record set from its ConfigMap after its copy failed; the next start tries again", "key", key, "err", err)
			continue
		}
		log.Info("removed a record set from its ConfigMap: the database keeps it", "key", key)
	}
}

// removeCopiedObjects deletes the objects a record set was kept in, each
// whole (a case's own ConfigMap), when the copy recorded under importID is
// in the database.
func (w *wiring) removeCopiedObjects(ctx context.Context, from kv.Objects, importID string) {
	if w.database == nil || from == nil {
		return
	}
	log := w.dbLog()
	rctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if copied, err := w.database.Imported(rctx, importID); err != nil || !copied {
		return
	}
	names, err := from.List(rctx)
	if err != nil {
		log.Warn("listing the cases' ConfigMaps to remove them after their copy failed; the next start tries again", "err", err)
		return
	}
	gone := 0
	for _, name := range names {
		if err := from.Delete(rctx, name); err != nil {
			log.Warn("removing a case's ConfigMap after its copy failed; the next start tries again", "case", name, "err", err)
			continue
		}
		gone++
	}
	if gone > 0 {
		log.Info("removed the cases' ConfigMaps: the database keeps the cases", "cases", gone)
	}
}

// --- The same for what is kept in Secrets (DB-61) ---
//
// A record set kept in Secrets has a secret part, which stays in its
// Secret, and the rest, which moves to the database. The copy and the
// removal therefore differ from a ConfigMap's in one way: only the part
// that moved is removed, by the record set's own function, and no Secret
// is ever deleted here.

// moveFromSecrets copies what a record set kept in its Secrets beside the
// secrets into the database, once, under importID, and then has that part
// removed from the Secrets. copy runs the one-time copy (it is given
// nothing to do when the copy is already recorded, db.ImportOnce) and
// says how many records it copied; remove takes the copied part out of
// the Secrets and says how many it took out. A copy that fails is logged
// and removes nothing: the next start tries again.
func (w *wiring) moveFromSecrets(ctx context.Context, importID, what string, copy, remove func(ctx context.Context) (int, error)) {
	w.claimImport(importID)
	if w.database == nil {
		return
	}
	log := w.dbLog()
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	n, err := copy(cctx)
	cancel()
	if err != nil {
		log.Warn("copying a record set from its Secret into the database failed; it stays in the Secret, and the next start tries again", "records", what, "err", err)
		return
	}
	if n > 0 {
		log.Info("copied a record set from its Secret into the database; the secrets stay in the Secret", "records", what, "count", n)
	}
	w.removeCopiedFromSecrets(ctx, importID, what, remove)
}

// removeCopiedFromSecrets has the part of a record set that moved removed
// from its Secrets, when the copy recorded under importID is in the
// database: at the start that copies it, and at any later start that
// finds it there again (a removal that failed, or what a console from
// before the move wrote since, which is removed and not read). Nothing is
// removed when the database cannot say the copy was made.
func (w *wiring) removeCopiedFromSecrets(ctx context.Context, importID, what string, remove func(ctx context.Context) (int, error)) {
	if w.database == nil {
		return
	}
	log := w.dbLog()
	rctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if copied, err := w.database.Imported(rctx, importID); err != nil || !copied {
		return
	}
	n, err := remove(rctx)
	if err != nil {
		log.Warn("removing a record set from its Secret after its copy failed; the next start tries again", "records", what, "err", err)
	}
	if n > 0 {
		log.Info("removed a record set from its Secret: the database keeps it, and the secrets stay in the Secret", "records", what, "count", n)
	}
}

// dbLog is where what the wiring does with the database is logged.
func (w *wiring) dbLog() *slog.Logger {
	if w.log == nil {
		return slog.Default().With("component", "database")
	}
	return w.log.With("component", "database")
}
