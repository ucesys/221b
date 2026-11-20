package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/certpin"
	"github.com/ucesys/221b/server/internal/db"
)

// Mycroft's records in the database (Phase 13, DB-48 to DB-50), every
// field a column, through db.Doc; the service is unchanged.
//
//   - The settings (SettingsKey): a source is a row of knowledge_sources,
//     known by the id the console gives it, with its live addresses and
//     its exclusions in tables of their own. Where the wiki is kept, the
//     days before a page is stale and the migrations done are of no row:
//     one settings scope.
//   - The syncs' state (StatusKey): each source's last read is a row of
//     knowledge_source_reads, the last sync and the newest few in short
//     rows of knowledge_syncs with their batches; whether one runs is a
//     settings scope.
//   - The page changes that wait for review (DraftsKey): a run is a row of
//     wiki_runs with its page changes (wiki_changes, and their sources,
//     approvals and contradictions) and its decisions; the requests to
//     retire a page and the corrections are wiki_page_requests and
//     wiki_corrections.

const (
	knowledgeScope = "knowledge"
	statusScope    = "knowledge-status"
)

func pgSame(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// A list as a column that is never null, and back: no elements is no
// list, as the document's JSON leaves an empty one out.
func pgList(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func pgListOf(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return append([]string(nil), s...)
}

func pgAt(t time.Time) *time.Time {
	u := t.UTC()
	return &u
}

func pgAtOrNone(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return pgAt(t)
}

func pgTimeOf(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

func pgLoadScope(ctx context.Context, q db.Querier, scope string, v any) (bool, error) {
	b, _, err := db.LoadScope(ctx, q, scope)
	if errors.Is(err, db.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(b, v)
}

func pgSaveScope(ctx context.Context, tx pgx.Tx, scope string, v any) error {
	b, _ := json.Marshal(v)
	_, err := db.SaveScope(ctx, tx, scope, b, db.AnyVersion, "")
	return err
}

// ---------- the settings ----------

// settingsKept is what the scope "knowledge" holds.
type settingsKept struct {
	Store     StoreSettings `json:"store"`
	StaleDays int           `json:"staleDays,omitempty"`
	Migrated  []string      `json:"migrated,omitempty"`
}

// NewSettingsPG keeps the sources and where the wiki is kept in d.
func NewSettingsPG(d *db.DB) *db.Doc[Settings] {
	return &db.Doc[Settings]{DB: d, Key: SettingsKey, Read: readSettings, Write: writeSettings}
}

func readSettings(ctx context.Context, q db.Querier) (Settings, bool, error) {
	var set Settings
	var kept settingsKept
	found, err := pgLoadScope(ctx, q, knowledgeScope, &kept)
	if err != nil {
		return set, false, err
	}
	set.Store, set.StaleDays, set.Migrated = kept.Store, kept.StaleDays, kept.Migrated
	var id string
	addresses := map[string][]LiveAddress{}
	rows, err := q.Query(ctx, "SELECT knowledge_source_id, kind, url, space FROM knowledge_source_addresses ORDER BY knowledge_source_id, position")
	if err != nil {
		return set, false, err
	}
	var a LiveAddress
	if _, err := pgx.ForEachRow(rows, []any{&id, &a.Kind, &a.URL, &a.Space}, func() error {
		addresses[id] = append(addresses[id], a)
		return nil
	}); err != nil {
		return set, false, err
	}
	excluded := map[string][]Exclusion{}
	rows, err = q.Query(ctx, "SELECT knowledge_source_id, pattern, is_under, is_item, title, reason, excluded_by, excluded_at FROM source_exclusions ORDER BY knowledge_source_id, position")
	if err != nil {
		return set, false, err
	}
	var e Exclusion
	var when time.Time
	if _, err := pgx.ForEachRow(rows, []any{&id, &e.Pattern, &e.Under, &e.Item, &e.Title, &e.Reason, &e.By, &when}, func() error {
		x := e
		x.At = when.UTC()
		excluded[id] = append(excluded[id], x)
		return nil
	}); err != nil {
		return set, false, err
	}
	rows, err = q.Query(ctx, `SELECT knowledge_source_id, name, kind, is_enabled, base_url, flavor, email, space, url, branch, path, username, repository_name, bucket, prefix, region,
			endpoint, is_path_style, credentials_kind, auth, depth, page_limit, migrated_from, ca_pem, pinned_cert_sha256, pinned_cert_subject, pinned_cert_issuer,
			pinned_cert_not_after, pinned_by, pinned_at, added_at, added_by, coalesce(credential_id, ''), is_credential_migrated
		FROM knowledge_sources ORDER BY position`)
	if err != nil {
		return set, false, err
	}
	var s Source
	var on bool
	var migrated []string
	var sha *string
	var pin certpin.Pin
	var notAfter, pinnedAt, added *time.Time
	if _, err := pgx.ForEachRow(rows, []any{&s.ID, &s.Name, &s.Kind, &on, &s.BaseURL, &s.Flavor, &s.Email, &s.Space, &s.URL, &s.Branch, &s.Path, &s.Username, &s.Repository, &s.Bucket,
		&s.Prefix, &s.Region, &s.Endpoint, &s.PathStyle, &s.Credentials, &s.Auth, &s.Depth, &s.Pages, &migrated, &s.CAPEM, &sha, &pin.Subject, &pin.Issuer,
		&notAfter, &pin.By, &pinnedAt, &added, &s.AddedBy, &s.Credential, &s.CredMigrated}, func() error {
		x := s
		x.Off, x.MigratedFrom, x.Added, x.PinnedCert = !on, pgListOf(migrated), pgTimeOf(added), nil
		x.Addresses, x.Excluded = addresses[x.ID], excluded[x.ID]
		if sha != nil {
			p := pin
			p.SHA256, p.NotAfter, p.At = *sha, pgTimeOf(notAfter), pgTimeOf(pinnedAt)
			x.PinnedCert = &p
		}
		set.Sources = append(set.Sources, x)
		return nil
	}); err != nil {
		return set, false, err
	}
	return set, found || len(set.Sources) > 0, nil
}

func writeSettings(ctx context.Context, tx pgx.Tx, old, next Settings) error {
	was, now := settingsKept{old.Store, old.StaleDays, old.Migrated}, settingsKept{next.Store, next.StaleDays, next.Migrated}
	if !pgSame(was, now) {
		if err := pgSaveScope(ctx, tx, knowledgeScope, now); err != nil {
			return err
		}
	}
	before, places := map[string]Source{}, map[string]int{}
	for i, s := range old.Sources {
		before[s.ID], places[s.ID] = s, i
	}
	b := &pgx.Batch{}
	kept := make([]string, 0, len(next.Sources))
	for i, s := range next.Sources {
		kept = append(kept, s.ID)
		if o, had := before[s.ID]; had && pgSame(o, s) {
			if places[s.ID] != i {
				b.Queue("UPDATE knowledge_sources SET position = $2 WHERE knowledge_source_id = $1", s.ID, i)
			}
			continue
		}
		// Whole: its addresses and exclusions go with the row.
		b.Queue("DELETE FROM knowledge_sources WHERE knowledge_source_id = $1", s.ID)
		var sha *string
		var pin certpin.Pin
		if s.PinnedCert != nil {
			pin = *s.PinnedCert
			sha = &pin.SHA256
		}
		// Its credential is a key (step 0034): none is null, and one whose
		// own record is not in the database, a list kept in memory or a
		// credential since removed, gets a row with only its id, so the
		// source still names it.
		var cred *string
		if s.Credential != "" {
			cred = &s.Credential
			b.Queue("INSERT INTO credentials (credential_id) VALUES ($1) ON CONFLICT (credential_id) DO NOTHING", s.Credential)
		}
		b.Queue(`INSERT INTO knowledge_sources (knowledge_source_id, position, name, kind, is_enabled, base_url, flavor, email, space, url, branch, path, username, repository_name,
				bucket, prefix, region, endpoint, is_path_style, credentials_kind, auth, depth, page_limit, migrated_from, ca_pem, pinned_cert_sha256, pinned_cert_subject,
				pinned_cert_issuer, pinned_cert_not_after, pinned_by, pinned_at, added_at, added_by, credential_id, is_credential_migrated)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30, $31, $32, $33, $34, $35)`,
			s.ID, i, s.Name, s.Kind, !s.Off, s.BaseURL, s.Flavor, s.Email, s.Space, s.URL, s.Branch, s.Path, s.Username, s.Repository,
			s.Bucket, s.Prefix, s.Region, s.Endpoint, s.PathStyle, s.Credentials, s.Auth, s.Depth, s.Pages, pgList(s.MigratedFrom), s.CAPEM, sha, pin.Subject,
			pin.Issuer, pgAtOrNone(pin.NotAfter), pin.By, pgAtOrNone(pin.At), pgAtOrNone(s.Added), s.AddedBy, cred, s.CredMigrated)
		for n, a := range s.Addresses {
			b.Queue("INSERT INTO knowledge_source_addresses (knowledge_source_id, position, kind, url, space) VALUES ($1, $2, $3, $4, $5)", s.ID, n, a.Kind, a.URL, a.Space)
		}
		for n, e := range s.Excluded {
			b.Queue(`INSERT INTO source_exclusions (knowledge_source_id, position, pattern, is_under, is_item, title, reason, excluded_by, excluded_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, s.ID, n, e.Pattern, e.Under, e.Item, e.Title, e.Reason, e.By, pgAt(e.At))
		}
	}
	b.Queue("DELETE FROM knowledge_sources WHERE NOT (knowledge_source_id = ANY($1))", kept)
	return tx.SendBatch(ctx, b).Close()
}

// ---------- the syncs' state ----------

// NewStatusPG keeps the syncs' state in d.
func NewStatusPG(d *db.DB) *db.Doc[Status] {
	return &db.Doc[Status]{DB: d, Key: StatusKey, Read: readStatus, Write: writeStatus}
}

// The two slots of knowledge_syncs.
const (
	slotLast    = "last"
	slotHistory = "history"
)

func readStatus(ctx context.Context, q db.Querier) (Status, bool, error) {
	var st Status
	var kept struct {
		Running bool `json:"running"`
	}
	found, err := pgLoadScope(ctx, q, statusScope, &kept)
	if err != nil {
		return st, false, err
	}
	st.Running = kept.Running
	rows, err := q.Query(ctx, "SELECT knowledge_source_id, last_read_at, item_count, error, reported_count, skipped_count, skipped_why, last_full_at FROM knowledge_source_reads")
	if err != nil {
		return st, false, err
	}
	var id string
	var ss SourceStatus
	var read, full *time.Time
	if _, err := pgx.ForEachRow(rows, []any{&id, &read, &ss.Items, &ss.Error, &ss.Reported, &ss.Skipped, &ss.SkippedWhy, &full}, func() error {
		x := ss
		x.LastRead, x.LastFull = pgTimeOf(read), pgTimeOf(full)
		if st.Sources == nil {
			st.Sources = map[string]SourceStatus{}
		}
		st.Sources[id] = x
		return nil
	}); err != nil {
		return st, false, err
	}
	batches := map[string][]BatchOutcome{}
	rows, err = q.Query(ctx, "SELECT knowledge_sync_id::text, items, outcome, page_count, note FROM knowledge_sync_batches ORDER BY knowledge_sync_id, position")
	if err != nil {
		return st, false, err
	}
	var bo BatchOutcome
	var items []string
	if _, err := pgx.ForEachRow(rows, []any{&id, &items, &bo.Outcome, &bo.Pages, &bo.Note}, func() error {
		x := bo
		// A batch's items are always written, empty or not.
		x.Items = append([]string{}, items...)
		batches[id] = append(batches[id], x)
		return nil
	}); err != nil {
		return st, false, err
	}
	rows, err = q.Query(ctx, `SELECT knowledge_sync_id::text, slot, run_key, started_at, finished_at, started_by, is_full, is_rebuild, read_count, unchanged_count, gone_count, page_count,
			proposal_count, reported, errors, not_proposed, summary, knowledge_source_id, source_name, skipped_count, skipped_why, unsent_count, model, tokens_in, tokens_out,
			cost_usd, fixes, contradictions
		FROM knowledge_syncs ORDER BY slot, position`)
	if err != nil {
		return st, false, err
	}
	var r SyncResult
	var slot string
	var started time.Time
	var finished *time.Time
	var reported, errs, notProposed, fixes, contradictions []string
	if _, err := pgx.ForEachRow(rows, []any{&id, &slot, &r.Run, &started, &finished, &r.By, &r.Full, &r.Rebuild, &r.Read, &r.Unchanged, &r.Gone, &r.Pages,
		&r.Proposals, &reported, &errs, &notProposed, &r.Text, &r.Source, &r.SourceName, &r.Skipped, &r.SkippedWhy, &r.Unsent, &r.Model, &r.TokensIn, &r.TokensOut,
		&r.CostUSD, &fixes, &contradictions}, func() error {
		x := r
		x.Started, x.Finished = started.UTC(), pgTimeOf(finished)
		x.Reported, x.Errors, x.NotProposed, x.Fixes, x.Contradictions = pgListOf(reported), pgListOf(errs), pgListOf(notProposed), pgListOf(fixes), pgListOf(contradictions)
		x.Batches = batches[id]
		if slot == slotLast {
			st.Last = &x
		} else {
			st.History = append(st.History, x)
		}
		return nil
	}); err != nil {
		return st, false, err
	}
	return st, found || st.Last != nil || len(st.History) > 0 || len(st.Sources) > 0, nil
}

func queueSync(b *pgx.Batch, slot string, position int, r SyncResult) {
	id := db.NewID()
	b.Queue(`INSERT INTO knowledge_syncs (knowledge_sync_id, slot, position, run_key, started_at, finished_at, started_by, is_full, is_rebuild, read_count, unchanged_count, gone_count,
			page_count, proposal_count, reported, errors, not_proposed, summary, knowledge_source_id, source_name, skipped_count, skipped_why, unsent_count, model, tokens_in,
			tokens_out, cost_usd, fixes, contradictions)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29)`,
		id, slot, position, r.Run, pgAt(r.Started), pgAtOrNone(r.Finished), r.By, r.Full, r.Rebuild, r.Read, r.Unchanged, r.Gone,
		r.Pages, r.Proposals, pgList(r.Reported), pgList(r.Errors), pgList(r.NotProposed), r.Text, r.Source, r.SourceName, r.Skipped, r.SkippedWhy, r.Unsent, r.Model, r.TokensIn,
		r.TokensOut, r.CostUSD, pgList(r.Fixes), pgList(r.Contradictions))
	for i, bo := range r.Batches {
		b.Queue("INSERT INTO knowledge_sync_batches (knowledge_sync_id, position, items, outcome, page_count, note) VALUES ($1, $2, $3, $4, $5, $6)", id, i, pgList(bo.Items), bo.Outcome, bo.Pages, bo.Note)
	}
}

func writeStatus(ctx context.Context, tx pgx.Tx, old, next Status) error {
	// The scope says a status is kept at all, so it is written with the first one.
	if _, _, err := db.LoadScope(ctx, tx, statusScope); errors.Is(err, db.ErrNotFound) || old.Running != next.Running {
		if err := pgSaveScope(ctx, tx, statusScope, struct {
			Running bool `json:"running"`
		}{next.Running}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	b := &pgx.Batch{}
	if !pgSame(old.Last, next.Last) {
		b.Queue("DELETE FROM knowledge_syncs WHERE slot = $1", slotLast)
		if next.Last != nil {
			queueSync(b, slotLast, 0, *next.Last)
		}
	}
	// The history is the newest few, in short: written again whole when it changes.
	if !pgSame(old.History, next.History) {
		b.Queue("DELETE FROM knowledge_syncs WHERE slot = $1", slotHistory)
		for i, r := range next.History {
			queueSync(b, slotHistory, i, r)
		}
	}
	ids := make([]string, 0, len(next.Sources))
	for id := range next.Sources {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s := next.Sources[id]
		if o, had := old.Sources[id]; had && pgSame(o, s) {
			continue
		}
		b.Queue(`INSERT INTO knowledge_source_reads (knowledge_source_id, last_read_at, item_count, error, reported_count, skipped_count, skipped_why, last_full_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (knowledge_source_id) DO UPDATE SET last_read_at = excluded.last_read_at, item_count = excluded.item_count, error = excluded.error,
				reported_count = excluded.reported_count, skipped_count = excluded.skipped_count, skipped_why = excluded.skipped_why, last_full_at = excluded.last_full_at`,
			id, pgAtOrNone(s.LastRead), s.Items, s.Error, s.Reported, s.Skipped, s.SkippedWhy, pgAtOrNone(s.LastFull))
	}
	b.Queue("DELETE FROM knowledge_source_reads WHERE NOT (knowledge_source_id = ANY($1))", ids)
	return tx.SendBatch(ctx, b).Close()
}

// ---------- the page changes that wait for review ----------

// NewDraftsPG keeps Mycroft's runs, the page requests and the corrections in d.
func NewDraftsPG(d *db.DB) *db.Doc[draftsDoc] {
	return &db.Doc[draftsDoc]{DB: d, Key: DraftsKey, Read: readDrafts, Write: writeDrafts}
}

// pageOf names a page change of a run: the run's row and the page's place.
type pageOf struct {
	run  string
	page int
}

func readDrafts(ctx context.Context, q db.Querier) (draftsDoc, bool, error) {
	var d draftsDoc
	var id string
	var page int
	var when time.Time
	sources := map[pageOf][]SourceRef{}
	rows, err := q.Query(ctx, "SELECT wiki_run_id::text, change_position, source_key, title, version, dated_at, url FROM wiki_change_sources ORDER BY wiki_run_id, change_position, position")
	if err != nil {
		return d, false, err
	}
	var ref SourceRef
	var dated *time.Time
	if _, err := pgx.ForEachRow(rows, []any{&id, &page, &ref.Key, &ref.Title, &ref.Version, &dated, &ref.URL}, func() error {
		x := ref
		x.Date = pgTimeOf(dated)
		sources[pageOf{id, page}] = append(sources[pageOf{id, page}], x)
		return nil
	}); err != nil {
		return d, false, err
	}
	approvals := map[pageOf][]Approval{}
	rows, err = q.Query(ctx, "SELECT wiki_run_id::text, change_position, approved_by, approved_by_name, approved_at FROM wiki_change_approvals ORDER BY wiki_run_id, change_position, position")
	if err != nil {
		return d, false, err
	}
	var ap Approval
	if _, err := pgx.ForEachRow(rows, []any{&id, &page, &ap.By, &ap.Name, &when}, func() error {
		x := ap
		x.At = when.UTC()
		approvals[pageOf{id, page}] = append(approvals[pageOf{id, page}], x)
		return nil
	}); err != nil {
		return d, false, err
	}
	contradicts := map[pageOf][]Contradiction{}
	rows, err = q.Query(ctx, "SELECT wiki_run_id::text, change_position, fix_key, title, case_count, why FROM wiki_change_contradictions ORDER BY wiki_run_id, change_position, position")
	if err != nil {
		return d, false, err
	}
	var c Contradiction
	if _, err := pgx.ForEachRow(rows, []any{&id, &page, &c.Fix, &c.Title, &c.Cases, &c.Why}, func() error {
		contradicts[pageOf{id, page}] = append(contradicts[pageOf{id, page}], c)
		return nil
	}); err != nil {
		return d, false, err
	}
	pages := map[string][]DraftPage{}
	rows, err = q.Query(ctx, `SELECT wiki_run_id::text, position, path, title, kind, is_critical, is_new, base_version, diff, added_count, removed_count, is_clipped, state, reason
		FROM wiki_changes ORDER BY wiki_run_id, position`)
	if err != nil {
		return d, false, err
	}
	var p DraftPage
	if _, err := pgx.ForEachRow(rows, []any{&id, &page, &p.Path, &p.Title, &p.Kind, &p.Critical, &p.New, &p.Base, &p.Diff, &p.Added, &p.Removed, &p.Clipped, &p.State, &p.Reason}, func() error {
		x := p
		k := pageOf{id, page}
		x.Sources, x.Approvals, x.Contradicts = sources[k], approvals[k], contradicts[k]
		pages[id] = append(pages[id], x)
		return nil
	}); err != nil {
		return d, false, err
	}
	decisions := map[string][]Decision{}
	rows, err = q.Query(ctx, "SELECT wiki_run_id::text, decided_by, decided_by_name, decided_at, action, comment, pages FROM wiki_run_decisions ORDER BY wiki_run_id, position")
	if err != nil {
		return d, false, err
	}
	var dec Decision
	var names []string
	if _, err := pgx.ForEachRow(rows, []any{&id, &dec.By, &dec.Name, &when, &dec.Action, &dec.Comment, &names}, func() error {
		x := dec
		x.At, x.Pages = when.UTC(), pgListOf(names)
		decisions[id] = append(decisions[id], x)
		return nil
	}); err != nil {
		return d, false, err
	}
	rows, err = q.Query(ctx, "SELECT wiki_run_id::text, run_key, started_at, started_by, started_by_name, state, store_kind, reported, decided_at FROM wiki_runs ORDER BY position")
	if err != nil {
		return d, false, err
	}
	var r Run
	var decided *time.Time
	if _, err := pgx.ForEachRow(rows, []any{&id, &r.ID, &when, &r.By, &r.StartedBy, &r.State, &r.Store, &names, &decided}, func() error {
		x := r
		x.Started, x.Reported, x.Decided = when.UTC(), pgListOf(names), pgTimeOf(decided)
		x.Pages, x.Decisions = pages[id], decisions[id]
		d.Runs = append(d.Runs, x)
		return nil
	}); err != nil {
		return d, false, err
	}
	rows, err = q.Query(ctx, `SELECT request_key, action, path, title, reason, requested_by, requested_by_name, requested_at, state, decided_by, decided_by_name, decided_at, comment
		FROM wiki_page_requests ORDER BY position`)
	if err != nil {
		return d, false, err
	}
	var req PageRequest
	if _, err := pgx.ForEachRow(rows, []any{&req.ID, &req.Action, &req.Path, &req.Title, &req.Reason, &req.By, &req.Name, &when, &req.State, &req.DecidedBy, &req.DecidedName, &decided,
		&req.Comment}, func() error {
		x := req
		x.At, x.Decided = when.UTC(), pgTimeOf(decided)
		d.Requests = append(d.Requests, x)
		return nil
	}); err != nil {
		return d, false, err
	}
	rows, err = q.Query(ctx, "SELECT correction_key, path, title, sent_by, sent_by_name, sent_at, state, run_key, note FROM wiki_corrections ORDER BY position")
	if err != nil {
		return d, false, err
	}
	var corr Correction
	if _, err := pgx.ForEachRow(rows, []any{&corr.ID, &corr.Path, &corr.Title, &corr.By, &corr.Name, &when, &corr.State, &corr.Run, &corr.Note}, func() error {
		x := corr
		x.At = when.UTC()
		d.Corrections = append(d.Corrections, x)
		return nil
	}); err != nil {
		return d, false, err
	}
	return d, len(d.Runs)+len(d.Requests)+len(d.Corrections) > 0, nil
}

func queueRun(b *pgx.Batch, r Run, position int) {
	id := db.NewID()
	b.Queue(`INSERT INTO wiki_runs (wiki_run_id, run_key, position, started_at, started_by, started_by_name, state, store_kind, reported, decided_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, id, r.ID, position, pgAt(r.Started), r.By, r.StartedBy, r.State, r.Store, pgList(r.Reported), pgAtOrNone(r.Decided))
	for i, p := range r.Pages {
		b.Queue(`INSERT INTO wiki_changes (wiki_run_id, position, path, title, kind, is_critical, is_new, base_version, diff, added_count, removed_count, is_clipped, state, reason)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`, id, i, p.Path, p.Title, p.Kind, p.Critical, p.New, p.Base, p.Diff, p.Added, p.Removed, p.Clipped, p.State, p.Reason)
		for n, s := range p.Sources {
			b.Queue("INSERT INTO wiki_change_sources (wiki_run_id, change_position, position, source_key, title, version, dated_at, url) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)",
				id, i, n, s.Key, s.Title, s.Version, pgAtOrNone(s.Date), s.URL)
		}
		for n, a := range p.Approvals {
			b.Queue("INSERT INTO wiki_change_approvals (wiki_run_id, change_position, position, approved_by, approved_by_name, approved_at) VALUES ($1, $2, $3, $4, $5, $6)",
				id, i, n, a.By, a.Name, pgAt(a.At))
		}
		for n, c := range p.Contradicts {
			b.Queue("INSERT INTO wiki_change_contradictions (wiki_run_id, change_position, position, fix_key, title, case_count, why) VALUES ($1, $2, $3, $4, $5, $6, $7)",
				id, i, n, c.Fix, c.Title, c.Cases, c.Why)
		}
	}
	for i, d := range r.Decisions {
		b.Queue("INSERT INTO wiki_run_decisions (wiki_run_id, position, decided_by, decided_by_name, decided_at, action, comment, pages) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)",
			id, i, d.By, d.Name, pgAt(d.At), d.Action, d.Comment, pgList(d.Pages))
	}
}

func writeDrafts(ctx context.Context, tx pgx.Tx, old, next draftsDoc) error {
	before, places := map[string]Run{}, map[string]int{}
	for i, r := range old.Runs {
		before[r.ID], places[r.ID] = r, i
	}
	b := &pgx.Batch{}
	kept := make([]string, 0, len(next.Runs))
	for i, r := range next.Runs {
		kept = append(kept, r.ID)
		if o, had := before[r.ID]; had && pgSame(o, r) {
			if places[r.ID] != i {
				b.Queue("UPDATE wiki_runs SET position = $2 WHERE run_key = $1", r.ID, i)
			}
			continue
		}
		// Whole: its page changes and decisions go with the row.
		b.Queue("DELETE FROM wiki_runs WHERE run_key = $1", r.ID)
		queueRun(b, r, i)
	}
	b.Queue("DELETE FROM wiki_runs WHERE NOT (run_key = ANY($1))", kept)
	// The requests and the corrections are short lists: each is written
	// again whole when it changes.
	if !pgSame(old.Requests, next.Requests) {
		b.Queue("DELETE FROM wiki_page_requests")
		for i, r := range next.Requests {
			b.Queue(`INSERT INTO wiki_page_requests (wiki_page_request_id, request_key, position, action, path, title, reason, requested_by, requested_by_name, requested_at, state,
					decided_by, decided_by_name, decided_at, comment)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
				db.NewID(), r.ID, i, r.Action, r.Path, r.Title, r.Reason, r.By, r.Name, pgAt(r.At), r.State, r.DecidedBy, r.DecidedName, pgAtOrNone(r.Decided), r.Comment)
		}
	}
	if !pgSame(old.Corrections, next.Corrections) {
		b.Queue("DELETE FROM wiki_corrections")
		for i, c := range next.Corrections {
			b.Queue(`INSERT INTO wiki_corrections (wiki_correction_id, correction_key, position, path, title, sent_by, sent_by_name, sent_at, state, run_key, note)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, db.NewID(), c.ID, i, c.Path, c.Title, c.By, c.Name, pgAt(c.At), c.State, c.Run, c.Note)
		}
	}
	return tx.SendBatch(ctx, b).Close()
}
