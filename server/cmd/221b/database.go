package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sort"
	"time"

	"github.com/ucesys/221b/server/internal/api"
	"github.com/ucesys/221b/server/internal/cards"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/iamauth"
	"github.com/ucesys/221b/server/internal/kv"
)

// The database (Phase 13, ADR-162): PostgreSQL, the chart's bundled one or
// one the operator runs. It is the only store (DB-69): every record set
// the console keeps is kept there, and the console does not run without
// it. The ConfigMaps a release before 0.13 kept its records in are the
// one-time copies' source and nothing else: each is read once, at the
// first start with the database, no record is ever saved there (notKept),
// and each is deleted whole once every record set it held is in the
// database (DB-72, removeCopiedSources).

// errNoDatabase is why a console given no database does not start.
var errNoDatabase = errors.New("the console keeps its records in PostgreSQL and does not run without one (DB-69): name the database with -db-host and the other -db-* flags; the chart does, with database.mode bundled (the default) or external")

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

// wireDatabase opens the database, brings its schema to this build's, and
// starts hearing the other replicas. A console that is given none does
// not start.
func (w *wiring) wireDatabase(ctx context.Context) error {
	cfg, log := w.cfg.Database, w.log.With("component", "database")
	if !cfg.Enabled() {
		return errNoDatabase
	}
	if cfg.AppName == "" {
		host, _ := os.Hostname()
		cfg.AppName = "221b/" + host
	}
	// A token of the cloud's IAM in place of a password (DB-83).
	if cfg.IAM() {
		tokens, err := iamauth.Source(ctx, cfg)
		if err != nil {
			return err
		}
		cfg.Tokens = tokens
		log.Info("signing in to the database with an IAM token for each connection", "iamAuth", cfg.IAMAuth)
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

// notKept is the rest of a store whose record sets are all in the
// database: a key that no table and no scope keeps. Reading one finds
// what a release before 0.13 left under it in the copy's source (the
// clean start reads such keys to count them), and deleting one deletes
// nothing: the ConfigMap goes whole once its record sets are copied
// (DB-72), and the console is not granted a ConfigMap's patch. A save is
// refused: nothing is kept outside the database by oversight.
type notKept struct{ from kv.Backend }

func (n notKept) LoadKey(ctx context.Context, key string) ([]byte, string, error) {
	if n.from == nil {
		return nil, "0", kv.ErrNotFound
	}
	return n.from.LoadKey(ctx, key)
}

func (n notKept) SaveKey(_ context.Context, key string, _ []byte, _ string) (string, error) {
	return "", fmt.Errorf("the console keeps nothing under %q: every record set is kept in the database (DB-69)", key)
}

func (n notKept) DeleteKey(context.Context, string) error { return nil }

// hasSource says whether from holds anything to copy: a ConfigMap that
// was named, under whatever earlier calls here made of it.
func hasSource(from kv.Backend) bool {
	switch b := from.(type) {
	case nil:
		return false
	case *kv.Keyed:
		return hasSource(b.Rest)
	case notKept:
		return b.from != nil
	}
	return true
}

// notNamed is where the keys a store does not name go: nowhere (notKept),
// reading from. A store that inScopes or inTables made already is given
// back as it is: its own rest refuses.
func notNamed(from kv.Backend) kv.Backend {
	if k, ok := from.(*kv.Keyed); ok {
		return k
	}
	return notKept{from: from}
}

// inScopes keeps the documents under the keys named (key to scope) as
// scopes of the database's settings table. These are a service's
// singletons, read whole and written rarely (model rule 14), so they need
// no repository beyond the scope.
//
// from is the one-time copy's source and nothing else: the ConfigMap a
// release before 0.13 kept the documents in (or a store an earlier call
// here made over it), nil when no ConfigMap is named or there. What it
// holds under the keys named is copied once, and the ConfigMap is deleted
// once every record set it held is copied (readFrom, DB-72). A key that
// is not named is kept nowhere (notNamed).
func (w *wiring) inScopes(ctx context.Context, from kv.Backend, scopes map[string]string) kv.Backend {
	s := db.NewScopeKV(w.database, scopes)
	for _, id := range s.ImportIDs() {
		w.claimImport(id)
		w.readFrom(from, id, id)
	}
	if hasSource(from) {
		ictx, cancel := context.WithTimeout(ctx, time.Minute)
		copied, err := s.Import(ictx, from, "configmap")
		cancel()
		log := w.dbLog()
		if err != nil {
			for _, key := range s.ImportIDs() {
				w.noteNotCopied(key)
			}
			log.Warn("copying settings from their ConfigMap into the database failed; the next start tries again", "err", err)
		} else if len(copied) > 0 {
			log.Info("copied settings from their ConfigMap into the database", "keys", copied)
		}
	}
	keys := map[string]kv.Backend{}
	for key := range scopes {
		keys[key] = s
	}
	return &kv.Keyed{Keys: keys, Rest: notNamed(from)}
}

// noteNotCopied remembers that the one-time copy of a key failed at this
// start, for what must not run on a record set that is not there yet (the
// clean start, whose marker says it ran).
func (w *wiring) noteNotCopied(key string) {
	if w.notCopied == nil {
		w.notCopied = map[string]bool{}
	}
	w.notCopied[key] = true
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
// through its own mapping to tables. make is given the database and
// returns the mappings.
//
// from is the one-time copy's source and nothing else, as for inScopes:
// what it holds under the keys named is copied once, in copyOrder, and
// the ConfigMap is deleted once every record set it held is copied
// (readFrom, DB-72). A key that is not named is kept nowhere (notNamed).
func (w *wiring) inTables(ctx context.Context, from kv.Backend, make func(d *db.DB) map[string]movedDoc) kv.Backend {
	log := w.dbLog()
	keys := map[string]kv.Backend{}
	docs := make(w.database)
	notCopied := map[string]bool{}
	for _, key := range copyOrder(docs) {
		doc := docs[key]
		keys[key] = doc
		w.claimImport(doc.ImportID())
		if !hasSource(from) {
			continue
		}
		w.readFrom(from, doc.ImportID(), key)
		if first, ok := copiedAfter[key]; ok && notCopied[first] {
			// Its rows would have nothing to hang from, and would not be kept.
			notCopied[key] = true
			w.noteNotCopied(key)
			log.Warn("records are not copied from their ConfigMap before the records they belong to; the next start tries again", "key", key, "after", first)
			continue
		}
		ictx, cancel := context.WithTimeout(ctx, time.Minute)
		copied, err := doc.Import(ictx, from, "configmap")
		cancel()
		if err != nil {
			notCopied[key] = true
			w.noteNotCopied(key)
			log.Warn("copying records from their ConfigMap into the database failed; the next start tries again", "key", key, "err", err)
		} else if copied {
			log.Info("copied records from their ConfigMap into the database", "key", key)
		}
	}
	return &kv.Keyed{Keys: keys, Rest: notNamed(from)}
}

// --- A ConfigMap of before is deleted once its record sets are copied (DB-55, DB-72) ---
//
// Decided by the owner on November 19, 2026, after the sixth cluster test
// (F1): a ConfigMap kept as it was at the copy let a console from before
// the move show changes that had since been approved and run as waiting
// again, and run them a second time. So once a record set's one-time copy
// is recorded in the database, it leaves where it was kept. On November
// 20 he decided that the ConfigMaps are then deleted, not left empty
// (DB-72): every copy names the ConfigMaps it reads and the record set it
// copies under (readFrom), and once the wiring is done and every copy has
// run, each ConfigMap whose record sets are all recorded in imports is
// deleted whole (removeCopiedSources): at the start that copies the last
// of them, or at a later start that still finds it (one that a copy or a
// deletion failed for, or one made again empty, which holds nothing to
// copy). A ConfigMap with a record set that has no record of a copy is
// left as it is, and the next start tries the copy again. The chart
// renders a ConfigMap of before only while it is there, with
// helm.sh/resource-policy keep, so Helm never deletes one first and never
// makes one again (ADR-167).

// copySource is a ConfigMap a release before 0.13 kept record sets in: a
// copy's source, which can be deleted whole.
type copySource interface {
	kv.Backend
	Ref() string
	Remove(ctx context.Context) error
}

// sourceUse is a copy's source with the record sets read from it, by the
// name each is copied under.
type sourceUse struct {
	src     copySource
	imports map[string]bool
}

// readFrom notes that the record set copied under importID is read from
// from under keys: every ConfigMap the keys are read from holds it, and is
// deleted only once its copy is recorded. It is called before the copy,
// so that a copy that fails keeps its ConfigMap.
func (w *wiring) readFrom(from kv.Backend, importID string, keys ...string) {
	w.sourcesMu.Lock()
	defer w.sourcesMu.Unlock()
	for _, key := range keys {
		for _, src := range leavesOf(from, key) {
			if w.sources == nil {
				w.sources = map[string]*sourceUse{}
			}
			u := w.sources[src.Ref()]
			if u == nil {
				u = &sourceUse{src: src, imports: map[string]bool{}}
				w.sources[src.Ref()] = u
			}
			u.imports[importID] = true
		}
	}
}

// leavesOf are the ConfigMaps a key is read from through from: through
// the limits, the routes (a routed key is read where it is routed and,
// when it is not there, where it was kept before), the keys an earlier
// call here sent to the database (none) and the rest.
func leavesOf(from kv.Backend, key string) []copySource {
	switch b := from.(type) {
	case nil:
		return nil
	case copySource:
		return []copySource{b}
	case *kv.Limited:
		if b == nil {
			return nil
		}
		return leavesOf(b.Backend, key)
	case *kv.Routed:
		if r, ok := b.Routes[key]; ok && r != nil {
			return append(leavesOf(r, key), leavesOf(b.Default, key)...)
		}
		return leavesOf(b.Default, key)
	case *kv.Keyed:
		if k, ok := b.Keys[key]; ok && k != nil {
			return nil
		}
		return leavesOf(b.Rest, key)
	case notKept:
		return leavesOf(b.from, key)
	case proposalsOfBefore:
		return append(leavesOf(b.now, key), leavesOf(b.before, key)...)
	}
	return nil
}

// removeCopiedSources deletes each ConfigMap of before whose record sets
// are all recorded in imports, and says which it keeps and why. A failure
// is logged and does not stop the console: the next start tries again.
func (w *wiring) removeCopiedSources(ctx context.Context) {
	w.sourcesMu.Lock()
	uses := make([]*sourceUse, 0, len(w.sources))
	for _, u := range w.sources {
		uses = append(uses, u)
	}
	w.sourcesMu.Unlock()
	sort.Slice(uses, func(i, j int) bool { return uses[i].src.Ref() < uses[j].src.Ref() })
	log := w.dbLog()
	for _, u := range uses {
		ref := u.src.Ref()
		ids := make([]string, 0, len(u.imports))
		for id := range u.imports {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		rctx, cancel := context.WithTimeout(ctx, time.Minute)
		var waiting []string
		for _, id := range ids {
			copied, err := w.database.Imported(rctx, id)
			if err != nil {
				// The database cannot say: nothing is deleted, and the pass
				// stops with one line rather than one a ConfigMap that
				// would say their records are not copied (the eleventh
				// cluster test: 11 such lines during an outage).
				cancel()
				log.Warn("the ConfigMaps of before are kept: the database cannot say now whether their record sets are copied; the next start looks again", "err", err)
				return
			}
			if !copied {
				waiting = append(waiting, id)
			}
		}
		if len(waiting) > 0 {
			cancel()
			log.Warn("a ConfigMap of before is kept: record sets it holds are not in the database yet; the next start copies them and deletes it", "configmap", ref, "records", waiting)
			continue
		}
		err := u.src.Remove(rctx)
		cancel()
		if err != nil {
			log.Warn("deleting a ConfigMap of before after its copy failed; the next start tries again", "configmap", ref, "err", err)
			continue
		}
		log.Info("deleted a ConfigMap of before: the database keeps every record set it held", "configmap", ref, "records", ids)
	}
}

// removeCopiedObjects deletes the objects a record set was kept in, each
// whole (a case's own ConfigMap), when the copy recorded under importID is
// in the database.
func (w *wiring) removeCopiedObjects(ctx context.Context, from kv.Objects, importID string) {
	if from == nil {
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
