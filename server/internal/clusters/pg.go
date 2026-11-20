package clusters

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// The cluster connections in the database (Phase 13, DB-64): each a row
// of clusters with is_kept true, every field of its Spec a column. The
// credential of each stays in the connection's Secret, whole, through
// SecretStore: the token, the kubeconfig, the client certificate and key,
// the OIDC client's secret and refresh token, and beside them what is read
// only together with them (the CA certificate, a kubeconfig's context, the
// OIDC issuer, client id and scopes). No column holds any of it, and
// nothing in this file logs it or puts it in an error.
//
// The two halves are written in one order. A save writes the row in a
// transaction, writes the Secret while the transaction is open, and
// commits: a Secret that cannot be written leaves the row as it was, no
// replica ever reads a row whose Secret is not written yet, and a console
// that stops between the two leaves a Secret without a connection (a new
// one) or a credential a save ahead of its row (a changed one), which the
// save that never answered, made again, puts right. A removal does the
// same: the row is set back to one with only its id, the Secret is
// deleted, and the transaction commits, so a Secret that cannot be deleted
// leaves the connection as it was. So a row may be met without its Secret
// (it reads with an empty credential, the connection says why it cannot
// sign in, and editing it gives it one) and a Secret without its row (it
// is not a connection, and is left where it is).
//
// The row of a removed connection is not deleted: applications and
// acknowledged alerts would go with it, and hosts and fleets would lose
// their cluster (the keys of steps 0001, 0004 and 0035). It is a row with
// only its id again, as it was before the connection was made.

// ImportID is the name the one-time copy of the connections out of their
// Secrets is recorded under.
const ImportID = "secret/clusters"

// Topic is what a save or a removal announces, so the other replicas
// sync at once (db.Subscribe) and not at their next tick.
const Topic = "clusters"

// PGStore keeps the connections in a database, and their credentials in
// their Secrets.
type PGStore struct {
	db      *db.DB
	secrets *SecretStore
	log     *slog.Logger
	// said are the connections whose missing Secret was logged, so each
	// is said once.
	said sync.Map
}

// NewPGStore keeps the connections in d and their credentials in secrets.
func NewPGStore(d *db.DB, secrets *SecretStore, log *slog.Logger) *PGStore {
	if log == nil {
		log = slog.Default()
	}
	return &PGStore{db: d, secrets: secrets, log: log}
}

func (s *PGStore) Kind() string { return "database" }

// clusterRow is a row of clusters, without is_kept and version, which the
// statements set themselves.
type clusterRow struct {
	ID          string `col:"cluster_id"`
	Name        string `col:"name"`
	Env         string `col:"environment"`
	Description string `col:"description"`
	Server      string `col:"server_url"`
	CA          string `col:"ca_mode"`
	CAInfo      string `col:"ca_info"`
	Auth        string `col:"auth_method"`
	Mode        string `col:"mode"`
	Local       bool   `col:"is_local"`

	Who      string     `col:"cred_who"`
	Context  string     `col:"cred_context"`
	Cluster  string     `col:"cred_cluster"`
	Issuer   string     `col:"cred_issuer"`
	ClientID string     `col:"cred_client_id"`
	Expires  *time.Time `col:"cred_expires_at"`
	Set      *time.Time `col:"cred_set_at"`

	// created_at is never null: a connection saved with no time of its
	// making keeps the zero time there, as its JSON did.
	Added   time.Time  `col:"created_at"`
	Updated *time.Time `col:"updated_at"`
}

func rowOf(s Spec) clusterRow {
	return clusterRow{ID: s.ID, Name: s.Name, Env: s.Env, Description: s.Description, Server: s.Server, CA: s.CA, CAInfo: s.CAInfo, Auth: s.Auth, Mode: s.Mode, Local: s.Local,
		Who: s.Cred.Who, Context: s.Cred.Context, Cluster: s.Cred.Cluster, Issuer: s.Cred.Issuer, ClientID: s.Cred.ClientID, Expires: nullableTime(s.Cred.Expires), Set: nullable(s.Cred.Set),
		Added: s.Added.UTC(), Updated: nullable(s.Updated)}
}

func specOf(r clusterRow) Spec {
	return Spec{ID: r.ID, Name: r.Name, Env: r.Env, Description: r.Description, Server: r.Server, CA: r.CA, CAInfo: r.CAInfo, Auth: r.Auth, Mode: r.Mode, Local: r.Local,
		Cred:  CredInfo{Who: r.Who, Context: r.Context, Cluster: r.Cluster, Issuer: r.Issuer, ClientID: r.ClientID, Expires: nullableTime(r.Expires), Set: utc(r.Set)},
		Added: r.Added.UTC(), Updated: utc(r.Updated)}
}

// utc is a nullable time as the records keep it: the zero time for none.
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

// nullableTime is a copy of a time that may be none, in UTC: the rows are
// read into the same variables one after another.
func nullableTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	return nullable(*t)
}

// upsertSQL writes a connection's row over whatever row has its id (one
// with only its id, or the connection as it was) and counts the write: a
// row another table points at is never deleted and made again.
var upsertSQL = func() string {
	cols := db.Columns(&clusterRow{})
	marks := make([]string, len(cols))
	sets := make([]string, 0, len(cols))
	for i, c := range cols {
		marks[i] = "$" + strconv.Itoa(i+1)
		if c != "cluster_id" {
			sets = append(sets, c+" = EXCLUDED."+c)
		}
	}
	return "INSERT INTO clusters (" + strings.Join(cols, ", ") + ", is_kept, version) VALUES (" + strings.Join(marks, ", ") + ", true, 1) " +
		"ON CONFLICT (cluster_id) DO UPDATE SET " + strings.Join(sets, ", ") + ", is_kept = true, version = clusters.version + 1"
}()

// emptySQL sets a connection's row back to one with only its id, and
// counts the write, so a connection made again under the id never has a
// revision it had before.
var emptySQL = func() string {
	var sets []string
	for _, c := range db.Columns(&clusterRow{}) {
		switch {
		case c == "cluster_id", c == "created_at":
		case c == "is_local":
			sets = append(sets, c+" = false")
		case strings.HasSuffix(c, "_at"):
			sets = append(sets, c+" = NULL")
		default:
			sets = append(sets, c+" = ''")
		}
	}
	return "UPDATE clusters SET " + strings.Join(sets, ", ") + ", is_kept = false, version = version + 1 WHERE cluster_id = $1 AND is_kept"
}()

// write puts a connection's row in, kept.
func write(ctx context.Context, q db.Querier, spec Spec) error {
	row := rowOf(spec)
	_, err := q.Exec(ctx, upsertSQL, db.Fields(&row)...)
	return err
}

// read is the connections that where picks, each with its row's version.
func read(ctx context.Context, q db.Querier, where string, args ...any) ([]Spec, []int, error) {
	var row clusterRow
	var version int
	rows, err := q.Query(ctx, "SELECT "+strings.Join(db.Columns(&row), ", ")+", version FROM clusters WHERE is_kept "+where, args...)
	if err != nil {
		return nil, nil, err
	}
	var specs []Spec
	var versions []int
	_, err = pgx.ForEachRow(rows, append(db.Fields(&row), &version), func() error {
		specs = append(specs, specOf(row))
		versions = append(versions, version)
		return nil
	})
	return specs, versions, err
}

// List is every connection that has a kept row. A Secret without one is
// not a connection.
func (s *PGStore) List(ctx context.Context) ([]Spec, error) {
	var specs []Spec
	if err := s.db.Do(ctx, func(q db.Querier) (err error) {
		specs, _, err = read(ctx, q, "")
		return err
	}); err != nil {
		return nil, fmt.Errorf("reading the connections: %w", err)
	}
	sortSpecs(specs)
	return specs, nil
}

// Get is a connection's row with the credential its Secret holds. A row
// whose Secret is not there reads with an empty credential, which is not
// an error: the connection is listed, says that it has nothing to sign in
// with, and is given a credential by editing it. A Secret that could not
// be asked for is an error, so nothing takes "not asked" for "empty".
func (s *PGStore) Get(ctx context.Context, id string) (Spec, Credential, error) {
	var specs []Spec
	if err := s.db.Do(ctx, func(q db.Querier) (err error) {
		specs, _, err = read(ctx, q, "AND cluster_id = $1", id)
		return err
	}); err != nil {
		return Spec{}, Credential{}, fmt.Errorf("reading the connection %s: %w", id, err)
	}
	if len(specs) == 0 {
		return Spec{}, Credential{}, ErrNotFound
	}
	spec := specs[0]
	cred, found, err := s.secrets.credential(ctx, id)
	if err != nil {
		return Spec{}, Credential{}, err
	}
	// The local cluster signs in as the console itself and never had a
	// credential kept.
	if !found && !spec.Local {
		if _, said := s.said.LoadOrStore(id, true); !said {
			s.log.Warn("a connection of the database has no Secret, so nothing to sign in with: edit the connection and give its credential again", "cluster", id, "secret", s.secrets.name(id))
		}
	} else {
		s.said.Delete(id)
	}
	return spec, cred, nil
}

// Save writes the row, then the Secret when a credential is given, then
// commits (see the top of the file), and tells the other replicas.
func (s *PGStore) Save(ctx context.Context, spec Spec, c *Credential) error {
	if spec.ID == "" {
		return errors.New("a connection is saved under its identifier, and this one has none")
	}
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		if err := write(ctx, tx, spec); err != nil {
			return err
		}
		if err := s.db.Notify(ctx, tx, Topic); err != nil {
			return err
		}
		if c == nil {
			return nil
		}
		// Safe to do again: the transaction is run again when another one
		// changed the same row first.
		return s.secrets.putCredential(ctx, spec.ID, *c)
	})
	if err != nil {
		return fmt.Errorf("saving the connection %s: %w", spec.ID, err)
	}
	return nil
}

// Delete sets the connection's row back to one with only its id, deletes
// its Secret (P3-08) and commits. A Secret that is not there is no error:
// the removal that deleted it and never answered is made again.
func (s *PGStore) Delete(ctx context.Context, id string) error {
	found := false
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, emptySQL, id)
		if err != nil {
			return err
		}
		if found = tag.RowsAffected() > 0; !found {
			// Not a connection: a Secret of that name is not this store's
			// to delete.
			return nil
		}
		if err := s.db.Notify(ctx, tx, Topic); err != nil {
			return err
		}
		if err := s.secrets.Delete(ctx, id); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("removing the connection %s: %w", id, err)
	}
	if !found {
		return ErrNotFound
	}
	s.said.Delete(id)
	return nil
}

// Revisions is each connection's revision: its row's version and its
// Secret's resourceVersion, so a replica notices a connection another one
// saved and a credential replaced by hand in the Secret alike. Either
// half that cannot be read is an error: a list that only looked shorter
// would stop connections that are still saved.
func (s *PGStore) Revisions(ctx context.Context) (map[string]string, error) {
	var specs []Spec
	var versions []int
	if err := s.db.Do(ctx, func(q db.Querier) (err error) {
		specs, versions, err = read(ctx, q, "")
		return err
	}); err != nil {
		return nil, fmt.Errorf("reading the connections: %w", err)
	}
	out := make(map[string]string, len(specs))
	if len(specs) == 0 {
		return out, nil
	}
	secrets, err := s.secrets.Revisions(ctx)
	if err != nil {
		return nil, err
	}
	for i, spec := range specs {
		out[spec.ID] = strconv.Itoa(versions[i]) + "/" + secrets[spec.ID]
	}
	return out, nil
}

// Unmatched compares the two halves at a start: the connections of the
// database whose Secret is not there, by id (the local cluster has none),
// and the connection Secrets no kept row is of, by name. Neither is
// changed.
func (s *PGStore) Unmatched(ctx context.Context) (noSecret, noRow []string, err error) {
	var specs []Spec
	if err := s.db.Do(ctx, func(q db.Querier) (err error) {
		specs, _, err = read(ctx, q, "")
		return err
	}); err != nil {
		return nil, nil, err
	}
	secs, err := s.secrets.all(ctx)
	if err != nil {
		return nil, nil, err
	}
	have := map[string]bool{}
	for i := range secs {
		have[secs[i].Name] = true
	}
	of := map[string]bool{}
	for _, spec := range specs {
		name := s.secrets.name(spec.ID)
		of[name] = true
		if !have[name] && !spec.Local {
			noSecret = append(noSecret, spec.ID)
		}
	}
	for name := range have {
		if !of[name] {
			noRow = append(noRow, name)
		}
	}
	sort.Strings(noSecret)
	sort.Strings(noRow)
	return noSecret, noRow, nil
}

// --- The one-time copy out of the Secrets, and what leaves them (DB-64) ---

// CopyFromSecrets copies the connections their Secrets keep under
// "spec.json" into the database, once, and says how many it copied: each
// becomes a kept row, an older format migrated as it is read
// (UnmarshalSpec). The credentials are not read into anything here. A
// connection the database already keeps stays as the database has it. A
// Secret whose record cannot be read (a newer console's format, or not a
// connection's), or is of another connection than its name says, is
// skipped, and said: it has no row, so nothing is ever taken out of it.
// Secrets that cannot be listed are an error: nothing is recorded, and
// the copy is tried again.
func CopyFromSecrets(ctx context.Context, d *db.DB, from *SecretStore, log *slog.Logger) (int, error) {
	if log == nil {
		log = slog.Default()
	}
	n := 0
	_, err := d.ImportOnce(ctx, ImportID, "secret", func(ctx context.Context, tx pgx.Tx) (int, error) {
		n = 0
		secs, err := from.all(ctx)
		if err != nil {
			return 0, err
		}
		for i := range secs {
			raw, has := secs[i].Data[keySpec]
			if !has {
				continue
			}
			// The error's text may quote what the key holds: it is not passed on.
			spec, err := UnmarshalSpec(raw)
			if err != nil || spec.ID == "" || from.name(spec.ID) != secs[i].Name {
				log.Warn("a connection's Secret is not copied into the database: its record cannot be read by this console, or is of another connection; the Secret is left as it is", "secret", secs[i].Name)
				continue
			}
			var kept bool
			switch err := tx.QueryRow(ctx, "SELECT is_kept FROM clusters WHERE cluster_id = $1 FOR UPDATE", spec.ID).Scan(&kept); {
			case errors.Is(err, pgx.ErrNoRows):
			case err != nil:
				return 0, err
			case kept:
				continue
			}
			if err := write(ctx, tx, spec); err != nil {
				return 0, err
			}
			n++
		}
		if n > 0 {
			if err := d.Notify(ctx, tx, Topic); err != nil {
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

// DropSpecs takes "spec.json" out of the connections' Secrets once the
// database keeps the connections: out of each Secret that still holds it
// and whose connection has a kept row, in one update over the version
// listed, every other key and every label as they were. It says how many
// it changed. No Secret is deleted, and no key of a credential is
// touched: the local cluster's Secret, which held nothing else, is left
// with no keys. A Secret with no kept row is left whole, and said: its
// record could not be read at the copy, or a console from before the move
// wrote it since. One that fails does not stop the others.
func DropSpecs(ctx context.Context, d *db.DB, from *SecretStore, log *slog.Logger) (int, error) {
	if log == nil {
		log = slog.Default()
	}
	secs, err := from.all(ctx)
	if err != nil {
		return 0, err
	}
	var with []*corev1.Secret
	for i := range secs {
		if _, has := secs[i].Data[keySpec]; has {
			with = append(with, &secs[i])
		}
	}
	if len(with) == 0 {
		return 0, nil
	}
	kept := map[string]bool{}
	if err := d.Do(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, "SELECT cluster_id FROM clusters WHERE is_kept")
		if err != nil {
			return err
		}
		var id string
		_, err = pgx.ForEachRow(rows, []any{&id}, func() error {
			kept[from.name(id)] = true
			return nil
		})
		return err
	}); err != nil {
		return 0, err
	}
	n := 0
	var failed, left []string
	for _, sec := range with {
		if err := ctx.Err(); err != nil {
			return n, fmt.Errorf("%d of the connections' Secrets still hold their record: %w", len(with)-n-len(left), err)
		}
		if !kept[sec.Name] {
			left = append(left, sec.Name)
			continue
		}
		if err := from.dropSpec(ctx, sec); err != nil {
			failed = append(failed, err.Error())
			continue
		}
		n++
	}
	if len(left) > 0 {
		log.Warn("connection Secrets that hold a record and are of no connection of the database are left as they are, and are not read", "secrets", left)
	}
	if len(failed) > 0 {
		return n, errors.New(strings.Join(failed, "; "))
	}
	return n, nil
}
