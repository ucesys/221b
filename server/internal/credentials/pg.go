package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ucesys/221b/server/internal/certpin"
	"github.com/ucesys/221b/server/internal/db"
)

// The credentials' list in the database (Phase 13, DB-60): each credential
// a row of credentials, every field a column, read and written one row at
// a time by PGRepo (repo.go, DB-75). The secret part of each stays in the
// Secret under secret-<id>, read and written as before: nothing in this
// file is given a Secret, and no column holds one.
//
// The table also holds rows made with only their id, for a repository, an
// application or a source that names a credential whose own record is not
// here (is_kept false). The list is the kept rows. A row something points
// at is never deleted and inserted again, which would empty the key that
// points at it: a credential that changes is updated in place, one copied
// or restored over a row with only its id fills that row, and one removed
// leaves its row with only its id while anything still points at it.

// ListImportID is the name the one-time copy of the list out of the
// Secret's "index" key is recorded under.
const ListImportID = "secret/credentials"

// migrationScope is the settings row with the note of the upgrade to
// 0.12.1 (CR-12), read whole.
const migrationScope = "credentials-migration"

// pointAtCredentials are the tables with a key to credentials: a row one
// of them points at is kept when its credential is removed.
var pointAtCredentials = []string{"applications", "knowledge_sources", "repositories"}

// credRow is a row of credentials.
type credRow struct {
	ID          string     `col:"credential_id"`
	Kept        bool       `col:"is_kept"`
	Name        string     `col:"name"`
	Kind        string     `col:"kind"`
	Endpoint    string     `col:"endpoint"`
	Description string     `col:"description"`
	Expires     *time.Time `col:"expires_at"`
	CAPEM       string     `col:"ca_pem"`

	PinSHA256   string     `col:"pinned_cert_sha256"`
	PinSubject  string     `col:"pinned_cert_subject"`
	PinIssuer   string     `col:"pinned_cert_issuer"`
	PinNotAfter *time.Time `col:"pinned_cert_not_after"`
	PinBy       string     `col:"pinned_by"`
	PinAt       *time.Time `col:"pinned_at"`

	SendMethod   string `col:"send_method"`
	SendUsername string `col:"send_username"`
	SendHeader   string `col:"send_header"`
	SendPrefix   string `col:"send_prefix"`
	SendParam    string `col:"send_param"`
	SendCookie   string `col:"send_cookie"`

	Username       string   `col:"username"`
	Region         string   `col:"region"`
	HostKey        string   `col:"host_key"`
	AppID          string   `col:"app_id"`
	InstallationID string   `col:"installation_id"`
	TokenURL       string   `col:"token_url"`
	ClientID       string   `col:"client_id"`
	Scope          string   `col:"scope"`
	CertPEM        string   `col:"cert_pem"`
	Parts          []string `col:"secret_parts"`

	TestedAt    *time.Time `col:"tested_at"`
	TestOK      bool       `col:"is_test_ok"`
	TestTarget  string     `col:"test_target"`
	TestMethod  string     `col:"test_method"`
	TestStatus  int        `col:"test_status"`
	TestText    string     `col:"test_text"`
	TestSuggest string     `col:"test_suggest"`
	TestedBy    string     `col:"tested_by"`

	CertSHA256     string     `col:"test_cert_sha256"`
	CertSubject    string     `col:"test_cert_subject"`
	CertIssuer     string     `col:"test_cert_issuer"`
	CertNotBefore  *time.Time `col:"test_cert_not_before"`
	CertNotAfter   *time.Time `col:"test_cert_not_after"`
	CertNames      []string   `col:"test_cert_names"`
	CertSelfSigned bool       `col:"is_test_cert_self_signed"`
	CertExpired    bool       `col:"is_test_cert_expired"`
	CertTrusted    bool       `col:"is_test_cert_trusted"`
	CertPinned     bool       `col:"is_test_cert_pinned"`
	CertPinnedWas  string     `col:"test_cert_pinned_was"`
	CertTrustable  bool       `col:"is_test_cert_trustable"`
	CertProblem    string     `col:"test_cert_problem"`

	Created   *time.Time `col:"created_at"`
	CreatedBy string     `col:"created_by"`
	Changed   *time.Time `col:"updated_at"`
	ChangedBy string     `col:"updated_by"`
	From      []string   `col:"migrated_from"`
}

// at is a time as a nullable column takes it: none for the zero time.
func at(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// timeOf is a nullable column's time, zero when it is null.
func timeOf(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

// list is a list as a column that is never null takes it, and listOf a
// column's list as the document has it: none, not an empty one. It is a
// copy: the rows are read into the same variables one after another.
func list(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func listOf(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return slices.Clone(s)
}

// rowOf is a credential as its row. A pinned certificate is told by its
// fingerprint, a last test by its time and the certificate it saw by its
// fingerprint, which each of them always has.
func rowOf(c Credential) credRow {
	r := credRow{ID: c.ID, Kept: true, Name: c.Name, Kind: c.Kind, Endpoint: c.Endpoint, Description: c.Description, Expires: at(c.Expires), CAPEM: c.CAPEM,
		SendMethod: c.SendAs.Method, SendUsername: c.SendAs.Username, SendHeader: c.SendAs.Header, SendPrefix: c.SendAs.Prefix, SendParam: c.SendAs.Param, SendCookie: c.SendAs.Cookie,
		Username: c.Username, Region: c.Region, HostKey: c.HostKey, AppID: c.AppID, InstallationID: c.InstallationID, TokenURL: c.TokenURL, ClientID: c.ClientID, Scope: c.Scope,
		CertPEM: c.CertPEM, Parts: list(c.Set), CertNames: []string{},
		Created: at(c.Created), CreatedBy: c.CreatedBy, Changed: at(c.Changed), ChangedBy: c.ChangedBy, From: list(c.From)}
	if p := c.PinnedCert; p != nil {
		r.PinSHA256, r.PinSubject, r.PinIssuer, r.PinNotAfter, r.PinBy, r.PinAt = p.SHA256, p.Subject, p.Issuer, at(p.NotAfter), p.By, at(p.At)
	}
	if t := c.LastTest; t != nil {
		// A test always has its time; one without would read back as none,
		// so it is kept at the time's zero, which is not null.
		when := t.At.UTC()
		r.TestedAt, r.TestOK, r.TestTarget, r.TestMethod, r.TestStatus, r.TestText, r.TestSuggest, r.TestedBy = &when, t.OK, t.Target, t.Method, t.Status, t.Text, t.Suggest, t.By
		if x := t.Certificate; x != nil {
			r.CertSHA256, r.CertSubject, r.CertIssuer, r.CertNotBefore, r.CertNotAfter, r.CertNames = x.SHA256, x.Subject, x.Issuer, at(x.NotBefore), at(x.NotAfter), list(x.Names)
			r.CertSelfSigned, r.CertExpired, r.CertTrusted, r.CertPinned, r.CertPinnedWas, r.CertTrustable, r.CertProblem = x.SelfSigned, x.Expired, x.Trusted, x.Pinned, x.PinnedWas, x.Trustable, x.Problem
		}
	}
	noNUL(&r)
	return r
}

// noNUL replaces the NUL characters of a row's text, which Postgres's
// text refuses, by U+FFFD (credentials#5): a tested server's status line
// may hold one, and a list of before 0.13 kept them. The id is left as it
// is: a row is never kept under another id.
func noNUL(r *credRow) {
	v := reflect.ValueOf(r).Elem()
	for i := 1; i < v.NumField(); i++ {
		switch f := v.Field(i); f.Kind() {
		case reflect.String:
			f.SetString(strings.ReplaceAll(f.String(), "\x00", "\uFFFD"))
		case reflect.Slice:
			if s, ok := f.Interface().([]string); ok {
				out := make([]string, len(s))
				for j, x := range s {
					out[j] = strings.ReplaceAll(x, "\x00", "\uFFFD")
				}
				f.Set(reflect.ValueOf(out))
			}
		}
	}
}

// credentialOf is a kept row as its credential.
func credentialOf(r credRow) Credential {
	c := Credential{ID: r.ID, Name: r.Name, Kind: r.Kind, Endpoint: r.Endpoint, Description: r.Description, Expires: timeOf(r.Expires), CAPEM: r.CAPEM,
		SendAs:   SendAs{Method: r.SendMethod, Username: r.SendUsername, Header: r.SendHeader, Prefix: r.SendPrefix, Param: r.SendParam, Cookie: r.SendCookie},
		Username: r.Username, Region: r.Region, HostKey: r.HostKey, AppID: r.AppID, InstallationID: r.InstallationID, TokenURL: r.TokenURL, ClientID: r.ClientID, Scope: r.Scope,
		CertPEM: r.CertPEM, Set: listOf(r.Parts),
		Created: timeOf(r.Created), CreatedBy: r.CreatedBy, Changed: timeOf(r.Changed), ChangedBy: r.ChangedBy, From: listOf(r.From)}
	if r.PinSHA256 != "" {
		c.PinnedCert = &certpin.Pin{SHA256: r.PinSHA256, Subject: r.PinSubject, Issuer: r.PinIssuer, NotAfter: timeOf(r.PinNotAfter), By: r.PinBy, At: timeOf(r.PinAt)}
	}
	if r.TestedAt != nil {
		t := Test{At: r.TestedAt.UTC(), OK: r.TestOK, Target: r.TestTarget, Method: r.TestMethod, Status: r.TestStatus, Text: r.TestText, Suggest: r.TestSuggest, By: r.TestedBy}
		if r.CertSHA256 != "" {
			t.Certificate = &certpin.Cert{SHA256: r.CertSHA256, Subject: r.CertSubject, Issuer: r.CertIssuer, NotBefore: timeOf(r.CertNotBefore), NotAfter: timeOf(r.CertNotAfter),
				Names: listOf(r.CertNames), SelfSigned: r.CertSelfSigned, Expired: r.CertExpired, Trusted: r.CertTrusted, Pinned: r.CertPinned, PinnedWas: r.CertPinnedWas,
				Trustable: r.CertTrustable, Problem: r.CertProblem}
		}
		c.LastTest = &t
	}
	return c
}

// The statements, made once from the row's columns so they cannot fall
// out of step with it: upsertSQL writes a credential over whatever row
// has its id, and idOnlySQL sets a row back to one with only its id.
var upsertSQL, idOnlySQL = func() (string, string) {
	cols := db.Columns(&credRow{})[1:]
	set, back := make([]string, len(cols)), make([]string, len(cols))
	for i, c := range cols {
		set[i], back[i] = c+" = excluded."+c, c+" = DEFAULT"
	}
	return db.InsertSQL("credentials", &credRow{}) + " ON CONFLICT (credential_id) DO UPDATE SET " + strings.Join(set, ", "),
		"UPDATE credentials SET " + strings.Join(back, ", ") + " WHERE credential_id = $1"
}()

// removeRow takes a credential out of the list: its row is deleted when
// nothing points at it, and set back to one with only its id otherwise,
// so what names it still does (it then reads as a credential that is not
// there, as it did when the list was in the Secret).
func removeRow(ctx context.Context, tx pgx.Tx, id string) error {
	free := make([]string, len(pointAtCredentials))
	for i, table := range pointAtCredentials {
		free[i] = "NOT EXISTS (SELECT 1 FROM " + table + " p WHERE p.credential_id = c.credential_id)"
	}
	tag, err := tx.Exec(ctx, "DELETE FROM credentials c WHERE c.credential_id = $1 AND "+strings.Join(free, " AND "), id)
	if err != nil || tag.RowsAffected() > 0 {
		return err
	}
	_, err = tx.Exec(ctx, idOnlySQL, id)
	return err
}

// --- The one-time copy out of the Secret (DB-61) ---

// CopyList copies the list the Secret kept under its "index" key into the
// database, once, and says how many credentials it copied. A row with
// only its id is filled; a credential the database already keeps under
// the same id stays as the database has it, which is the newer; one whose
// name a credential of the database has is copied as "NAME (copied at
// upgrade)". A list that cannot be read is an error: nothing is recorded,
// the key stays, and the next start tries again. The secrets are not
// read.
func CopyList(ctx context.Context, d *db.DB, from SecretStore) (int, error) {
	n := 0
	_, err := d.ImportOnce(ctx, ListImportID, "secret", func(ctx context.Context, tx pgx.Tx) (int, error) {
		n = 0
		b, _, found, err := from.LoadKey(ctx, indexKey)
		if err != nil {
			return 0, fmt.Errorf("reading the credentials' Secret: %w", err)
		}
		if !found || len(b) == 0 {
			return 0, nil
		}
		var ix index
		if err := json.Unmarshal(b, &ix); err != nil {
			// The text of the error may quote the list: it is not passed on.
			return 0, errors.New("the credentials' list in the Secret cannot be read: it is left there")
		}
		if _, _, err := db.LoadScope(ctx, tx, migrationScope); errors.Is(err, db.ErrNotFound) {
			m, _ := json.Marshal(ix.Migration)
			if _, err := db.SaveScope(ctx, tx, migrationScope, m, db.AnyVersion, ""); err != nil {
				return 0, err
			}
		} else if err != nil {
			return 0, err
		}
		have, err := listRows(ctx, tx)
		if err != nil {
			return 0, err
		}
		// One record Postgres refuses does not stop the others
		// (credentials#5): each is written under a savepoint; a name the
		// unique index takes for another's (lower(), which folds more
		// than EqualFold) is told apart, and one that still cannot be
		// written is left out and said in the note of the upgrade.
		var left []string
		for _, c := range ix.Credentials {
			if c.ID == "" || slices.ContainsFunc(have, func(o Credential) bool { return o.ID == c.ID }) {
				continue
			}
			tried := have
			if taken(tried, c.Name) {
				tried = append(tried, Credential{Name: c.Name})
				c.Name = suffixedName(have, c.Name, copiedSuffix)
			}
			err := copyRow(ctx, tx, c)
			for range 5 {
				if !db.IsUnique(err) {
					break
				}
				tried = append(tried, Credential{Name: c.Name})
				c.Name = suffixedName(tried, strings.TrimSpace(strings.Split(c.Name, " ("+copiedSuffix)[0]), copiedSuffix)
				err = copyRow(ctx, tx, c)
			}
			if err != nil {
				left = append(left, fmt.Sprintf("The credential %q (id %q) of before 0.13 could not be copied into the database, and is left out: %s. Its secret stays in the Secret; make it again in Administration › Credentials.", c.Name, c.ID, pgReason(err)))
				continue
			}
			have = append(have, c)
			n++
		}
		if len(left) > 0 {
			if err := noteInMigration(ctx, tx, left); err != nil {
				return 0, err
			}
		}
		return n, nil
	})
	if err != nil {
		n = 0
	}
	return n, err
}

// copyRow writes a credential of the copy under a savepoint: one that
// fails leaves the copy's transaction as it was.
func copyRow(ctx context.Context, tx pgx.Tx, c Credential) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	row := rowOf(c)
	if _, err := sp.Exec(ctx, upsertSQL, db.Fields(&row)...); err != nil {
		_ = sp.Rollback(ctx)
		return err
	}
	return sp.Commit(ctx)
}

// pgReason is why Postgres refused a row, without what it held.
func pgReason(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Message
	}
	return err.Error()
}

// noteInMigration adds notes to the note of the upgrade, which the page
// shows until a SuperAdmin dismisses it.
func noteInMigration(ctx context.Context, tx pgx.Tx, notes []string) error {
	var m Migration
	b, _, err := db.LoadScope(ctx, tx, migrationScope)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return err
	}
	if err == nil {
		if err := json.Unmarshal(b, &m); err != nil {
			return err
		}
	}
	for _, n := range notes {
		if !containsString(m.Notes, n) {
			m.Notes = append(m.Notes, n)
		}
	}
	m.Dismissed = false
	next, _ := json.Marshal(m)
	_, err = db.SaveScope(ctx, tx, migrationScope, next, db.AnyVersion, "")
	return err
}

// Unmatched compares the two halves at a start: the ids of the secrets in
// the Secret that no credential of the list has, and of the credentials
// whose list says a secret part is set and whose secret is not there.
// Neither is changed: a secret without its credential is never used, and
// a credential without its secret says so where it is used. A store that
// cannot list its keys is not compared.
//
// No secret is said to have no credential until the list of before 0.13
// is copied into the database and has left the Secret (credentials#4):
// until then every secret-<id> may be a credential not copied yet. A row
// with only its id (what an application, a repository or a source names)
// names its secret too.
func Unmatched(ctx context.Context, list Repo, secrets SecretStore) (noCredential, noSecret []string, err error) {
	all, ok := secrets.(interface {
		Keys(ctx context.Context) ([]string, error)
	})
	if !ok {
		return nil, nil, nil
	}
	creds, err := list.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	keys, err := all.Keys(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, c := range creds {
		if len(c.Set) > 0 && !slices.Contains(keys, secretKey(c.ID)) {
			noSecret = append(noSecret, c.ID)
		}
	}
	named, settled, err := namedSecrets(ctx, list, secrets, creds)
	if err != nil || !settled {
		return nil, noSecret, err
	}
	for _, key := range keys {
		if id, isSecret := strings.CutPrefix(key, "secret-"); isSecret && !named[id] {
			noCredential = append(noCredential, id)
		}
	}
	slices.Sort(noCredential)
	return noCredential, noSecret, nil
}

// rowKeeper is a list with rows of its own beside the credentials it
// keeps: the database's, whose rows with only their id something points
// at, and whose copy of the list of before 0.13 is recorded.
type rowKeeper interface {
	rowIDs(ctx context.Context) (map[string]bool, error)
	listCopied(ctx context.Context) (bool, error)
}

func (r *PGRepo) rowIDs(ctx context.Context) (map[string]bool, error) {
	out := map[string]bool{}
	err := r.d.Do(ctx, func(q db.Querier) error {
		clear(out)
		rows, err := q.Query(ctx, "SELECT credential_id FROM credentials")
		if err != nil {
			return err
		}
		var id string
		_, err = pgx.ForEachRow(rows, []any{&id}, func() error { out[id] = true; return nil })
		return err
	})
	return out, err
}

func (r *PGRepo) listCopied(ctx context.Context) (bool, error) {
	return r.d.Imported(ctx, ListImportID)
}

// namedSecrets are the ids whose secret a credential names (creds, the
// list's credentials, and the rows with only their id), and whether the
// list is settled: copied out of the Secret's index, which is gone. A
// list that is not settled may name any secret.
func namedSecrets(ctx context.Context, list Repo, secrets SecretStore, creds []Credential) (map[string]bool, bool, error) {
	named := map[string]bool{}
	for _, c := range creds {
		named[c.ID] = true
	}
	if rk, ok := list.(rowKeeper); ok {
		copied, err := rk.listCopied(ctx)
		if err != nil || !copied {
			return nil, false, err
		}
		ids, err := rk.rowIDs(ctx)
		if err != nil {
			return nil, false, err
		}
		for id := range ids {
			named[id] = true
		}
	}
	b, _, found, err := secrets.LoadKey(ctx, indexKey)
	if err != nil || found && len(b) > 0 {
		return nil, false, err
	}
	return named, true, nil
}

// RemoveList takes the list out of the Secret once the database keeps it:
// the "index" key alone. The Secret itself and every secret-<id> key stay
// as they are. It says whether there was a list to remove.
func RemoveList(ctx context.Context, from SecretStore) (bool, error) {
	var err error
	for range 10 {
		var version string
		var found bool
		var b []byte
		if b, version, found, err = from.LoadKey(ctx, indexKey); err != nil || !found {
			return false, err
		}
		del, canDelete := from.(keyDeleter)
		if !canDelete && len(b) == 0 {
			return false, nil
		}
		if canDelete {
			err = del.DeleteKeyIf(ctx, indexKey, version)
		} else {
			// A store that cannot remove a key keeps it with nothing in it.
			err = from.SaveKeyIf(ctx, indexKey, nil, version)
		}
		if !conflict(err) {
			return err == nil, err
		}
	}
	return false, err
}
