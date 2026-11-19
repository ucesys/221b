package apps

import (
	"context"
	"errors"
	"time"

	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/certpin"
	"github.com/ucesys/221b/server/internal/db"
)

// The repositories in the database (Phase 13, DB-27): each a row of
// repositories, every field a column, through db.Doc, an interim step
// until Repos has a repository of its own. Their passwords and tokens stay
// in the Secret, as before.

// migrationScope is the settings row with the note of the one move of the
// git repositories into the list (RP-06), read whole.
const migrationScope = "repositories-migration"

// NewReposPG keeps the repositories in d.
func NewReposPG(d *db.DB) *db.Doc[repoRecord] {
	return &db.Doc[repoRecord]{DB: d, Key: RepositoriesKey, Read: readRepos, Write: writeRepos}
}

func readRepos(ctx context.Context, q db.Querier) (repoRecord, bool, error) {
	var rec repoRecord
	b, _, err := db.LoadScope(ctx, q, migrationScope)
	found := err == nil
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return rec, false, err
	}
	if found {
		_ = json.Unmarshal(b, &rec.Migration)
	}
	rows, err := q.Query(ctx, `SELECT name, kind, url, default_branch, coalesce(credential_id, ''), is_credential_migrated, auth_method, username, is_secret_set,
			pinned_cert_sha256, pinned_cert_subject, pinned_cert_issuer, pinned_cert_not_after, pinned_by, pinned_at,
			tested_at, is_test_ok, test_result, updated_by, updated_at
		FROM repositories ORDER BY position, name`)
	if err != nil {
		return rec, false, err
	}
	rec.Repositories, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Repository, error) {
		var r Repository
		var pin certpin.Pin
		var notAfter, pinnedAt, tested, set *time.Time
		err := row.Scan(&r.Name, &r.Kind, &r.URL, &r.DefaultBranch, &r.Credential, &r.CredMigrated, &r.Auth, &r.Username, &r.HasSecret,
			&pin.SHA256, &pin.Subject, &pin.Issuer, &notAfter, &pin.By, &pinnedAt, &tested, &r.TestOK, &r.Tested, &r.SetBy, &set)
		if pin.SHA256 != "" {
			pin.NotAfter, pin.At = utc(notAfter), utc(pinnedAt)
			r.PinnedCert = &pin
		}
		r.TestedAt, r.SetAt = utc(tested), utc(set)
		return r, err
	})
	if len(rec.Repositories) == 0 {
		rec.Repositories = nil
	}
	return rec, found || len(rec.Repositories) > 0, err
}

// utc is a nullable column's time, zero when it is NULL.
func utc(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

// nullable is a time as a nullable column takes it.
func nullable(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func writeRepos(ctx context.Context, tx pgx.Tx, old, next repoRecord) error {
	a, _ := json.Marshal(old.Migration)
	b, _ := json.Marshal(next.Migration)
	if string(a) != string(b) {
		if _, err := db.SaveScope(ctx, tx, migrationScope, b, db.AnyVersion, ""); err != nil {
			return err
		}
	}
	// The list is short and its order is the page's: written anew when
	// anything in it changed, each repository keeping its row's key.
	if string(mustJSON(old.Repositories)) == string(mustJSON(next.Repositories)) {
		return nil
	}
	ids := map[string]string{}
	rows, err := tx.Query(ctx, "SELECT name, repository_id::text FROM repositories")
	if err != nil {
		return err
	}
	var name, id string
	if _, err := pgx.ForEachRow(rows, []any{&name, &id}, func() error { ids[name] = id; return nil }); err != nil {
		return err
	}
	keep := make([]string, 0, len(next.Repositories))
	for i, r := range next.Repositories {
		id := ids[r.Name]
		if id == "" {
			id = db.NewID()
		}
		keep = append(keep, id)
		var cred *string
		if r.Credential != "" {
			cred = &r.Credential
			// Known by its id until the credentials' own records move here.
			if _, err := tx.Exec(ctx, "INSERT INTO credentials (credential_id) VALUES ($1) ON CONFLICT (credential_id) DO NOTHING", r.Credential); err != nil {
				return err
			}
		}
		pin := certpin.Pin{}
		if r.PinnedCert != nil {
			pin = *r.PinnedCert
		}
		if _, err := tx.Exec(ctx, `INSERT INTO repositories (repository_id, credential_id, position, name, kind, url, default_branch, auth_method, username,
				is_secret_set, is_credential_migrated, pinned_cert_sha256, pinned_cert_subject, pinned_cert_issuer, pinned_cert_not_after, pinned_by, pinned_at,
				is_test_ok, test_result, tested_at, updated_by, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)
			ON CONFLICT (repository_id) DO UPDATE SET credential_id = excluded.credential_id, position = excluded.position, name = excluded.name,
				kind = excluded.kind, url = excluded.url, default_branch = excluded.default_branch, auth_method = excluded.auth_method,
				username = excluded.username, is_secret_set = excluded.is_secret_set, is_credential_migrated = excluded.is_credential_migrated,
				pinned_cert_sha256 = excluded.pinned_cert_sha256, pinned_cert_subject = excluded.pinned_cert_subject,
				pinned_cert_issuer = excluded.pinned_cert_issuer, pinned_cert_not_after = excluded.pinned_cert_not_after,
				pinned_by = excluded.pinned_by, pinned_at = excluded.pinned_at, is_test_ok = excluded.is_test_ok,
				test_result = excluded.test_result, tested_at = excluded.tested_at, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
			id, cred, i, r.Name, r.Kind, r.URL, r.DefaultBranch, r.Auth, r.Username, r.HasSecret, r.CredMigrated,
			pin.SHA256, pin.Subject, pin.Issuer, nullable(pin.NotAfter), pin.By, nullable(pin.At),
			r.TestOK, r.Tested, nullable(r.TestedAt), r.SetBy, nullable(r.SetAt)); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, "DELETE FROM repositories WHERE NOT (repository_id::text = ANY($1))", keep)
	return err
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
