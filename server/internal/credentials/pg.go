package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/certpin"
	"github.com/ucesys/221b/server/internal/db"
)

// The credentials' list in the database (Phase 13, DB-60): each credential
// a row of credentials, every field a column, through db.Doc, an interim
// step until the service has a repository of its own. The secret part of
// each stays in the Secret under secret-<id>, read and written as before:
// nothing in this file is given a Secret, and no column holds one.
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

// NewPG keeps the credentials' list in d.
func NewPG(d *db.DB) *db.Doc[index] {
	return &db.Doc[index]{DB: d, Key: "credentials", ImportAs: ListImportID, Read: readList, Write: writeList}
}

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
	return r
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

func readList(ctx context.Context, q db.Querier) (index, bool, error) {
	var ix index
	b, _, err := db.LoadScope(ctx, q, migrationScope)
	found := err == nil
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return ix, false, err
	}
	if found {
		_ = json.Unmarshal(b, &ix.Migration)
	}
	// In the order they were made, as the Secret's list was.
	rows, err := q.Query(ctx, db.SelectSQL("credentials", &credRow{}, "WHERE is_kept ORDER BY created_at NULLS FIRST, credential_id"))
	if err != nil {
		return ix, false, err
	}
	var r credRow
	if _, err := pgx.ForEachRow(rows, db.Fields(&r), func() error {
		ix.Credentials = append(ix.Credentials, credentialOf(r))
		return nil
	}); err != nil {
		return ix, false, err
	}
	return ix, found || len(ix.Credentials) > 0, nil
}

func writeList(ctx context.Context, tx pgx.Tx, old, next index) error {
	a, _ := json.Marshal(old.Migration)
	b, _ := json.Marshal(next.Migration)
	if string(a) != string(b) {
		if _, err := db.SaveScope(ctx, tx, migrationScope, b, db.AnyVersion, ""); err != nil {
			return err
		}
	}
	// The removed ones first, so a name one of them had is free for another.
	kept := map[string]bool{}
	for _, c := range next.Credentials {
		kept[c.ID] = true
	}
	for _, c := range old.Credentials {
		if !kept[c.ID] {
			if err := removeRow(ctx, tx, c.ID); err != nil {
				return err
			}
		}
	}
	put := func(c Credential) error {
		if c.ID == "" {
			return errors.New("a credential without an id cannot be kept")
		}
		r := rowOf(c)
		if _, err := tx.Exec(ctx, upsertSQL, db.Fields(&r)...); err != nil {
			if db.IsUnique(err) {
				return fmt.Errorf("there is a credential named %s already", c.Name)
			}
			return err
		}
		return nil
	}
	return db.Diff(old.Credentials, next.Credentials, func(c Credential) string { return c.ID }, put,
		func(_, c Credential) error { return put(c) }, func(Credential) error { return nil })
}

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
// name a credential of the database has is copied under a name of its
// own. A list that cannot be read is an error: nothing is recorded, the
// key stays, and the next start tries again. The secrets are not read.
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
		have, _, err := readList(ctx, tx)
		if err != nil {
			return 0, err
		}
		next := index{Credentials: slices.Clone(have.Credentials), Migration: have.Migration}
		if _, _, err := db.LoadScope(ctx, tx, migrationScope); errors.Is(err, db.ErrNotFound) {
			next.Migration = ix.Migration
		} else if err != nil {
			return 0, err
		}
		for _, c := range ix.Credentials {
			if c.ID == "" || slices.ContainsFunc(next.Credentials, func(o Credential) bool { return o.ID == c.ID }) {
				continue
			}
			if taken(next.Credentials, c.Name) {
				c.Name = restoredName(next.Credentials, c.Name)
			}
			next.Credentials = append(next.Credentials, c)
			n++
		}
		return n, writeList(ctx, tx, have, next)
	})
	if err != nil {
		n = 0
	}
	return n, err
}

// Unmatched compares the two halves at a start: the ids of the secrets in
// the Secret that no credential of the list has, and of the credentials
// whose list says a secret part is set and whose secret is not there.
// Neither is changed: a secret without its credential is never used, and
// a credential without its secret says so where it is used. A store that
// cannot list its keys is not compared.
func Unmatched(ctx context.Context, list *db.Doc[index], secrets SecretStore) (noCredential, noSecret []string, err error) {
	all, ok := secrets.(interface {
		Keys(ctx context.Context) ([]string, error)
	})
	if !ok {
		return nil, nil, nil
	}
	var ix index
	if err := list.DB.Snapshot(ctx, func(q db.Querier) (err error) {
		ix, _, err = readList(ctx, q)
		return err
	}); err != nil {
		return nil, nil, err
	}
	keys, err := all.Keys(ctx)
	if err != nil {
		return nil, nil, err
	}
	listed := map[string]bool{}
	for _, c := range ix.Credentials {
		listed[c.ID] = true
		if len(c.Set) > 0 && !slices.Contains(keys, secretKey(c.ID)) {
			noSecret = append(noSecret, c.ID)
		}
	}
	for _, key := range keys {
		if id, isSecret := strings.CutPrefix(key, "secret-"); isSecret && !listed[id] {
			noCredential = append(noCredential, id)
		}
	}
	slices.Sort(noCredential)
	return noCredential, noSecret, nil
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
