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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
	return "UPDATE clusters SET " + strings.Join(sets, ", ") + ", " + noTestSQL + ", is_kept = false, version = version + 1 WHERE cluster_id = $1 AND is_kept"
}()

// The last test (step 0041): columns of the connection's row beside its
// spec, written only by SaveTest, and its steps in cluster_test_steps.

// testRow is the last test's columns of a row of clusters.
type testRow struct {
	At          *time.Time `col:"tested_at"`
	By          string     `col:"tested_by"`
	Tone        string     `col:"test_tone"`
	Summary     string     `col:"test_summary"`
	Error       string     `col:"test_error"`
	Version     string     `col:"test_version"`
	Nodes       int        `col:"test_nodes"`
	NodesMore   bool       `col:"is_test_nodes_more"`
	Who         string     `col:"test_who"`
	Impersonate bool       `col:"is_test_impersonate"`
}

// noTestSQL empties the last test's columns, with a removal.
const noTestSQL = "tested_at = NULL, tested_by = '', test_tone = '', test_summary = '', test_error = '', test_version = '', test_nodes = 0, is_test_nodes_more = false, test_who = '', is_test_impersonate = false"

// saveTestSQL writes the last test's columns of a kept row. It does not
// count the write: a test is no edit of the connection, and an editor
// open meanwhile can still save over the version it read (DB-92). The
// other replicas see the test by its time, which Revisions carries.
var saveTestSQL = func() string {
	cols := db.Columns(&testRow{})
	sets := make([]string, len(cols))
	for i, c := range cols {
		sets[i] = c + " = $" + strconv.Itoa(i+2)
	}
	return "UPDATE clusters SET " + strings.Join(sets, ", ") + " WHERE cluster_id = $1 AND is_kept"
}()

// SaveTest keeps a connection's last test (DB-64): its row's columns and
// its steps, in one transaction, and tells the other replicas. It never
// touches the spec, and ErrNotFound is a connection with no kept row.
func (s *PGStore) SaveTest(ctx context.Context, id string, t LastTest) error {
	r := t.Result
	row := testRow{At: nullable(t.At), By: t.By, Tone: r.Tone, Summary: r.Summary, Error: r.Error, Version: r.Version, Nodes: r.Nodes, NodesMore: r.NodesMore, Who: r.Who, Impersonate: r.Impersonate}
	found := false
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, saveTestSQL, append([]any{id}, db.Fields(&row)...)...)
		if err != nil {
			return err
		}
		if found = tag.RowsAffected() > 0; !found {
			return nil
		}
		b := &pgx.Batch{}
		b.Queue("DELETE FROM cluster_test_steps WHERE cluster_id = $1", id)
		for i, st := range r.Steps {
			b.Queue("INSERT INTO cluster_test_steps (cluster_id, position, step_key, tone, text) VALUES ($1, $2, $3, $4, $5)", id, i, st.Key, st.Tone, st.Text)
		}
		if err := tx.SendBatch(ctx, b).Close(); err != nil {
			return err
		}
		return s.db.Notify(ctx, tx, Topic)
	})
	if err != nil {
		return fmt.Errorf("keeping the test of the connection %s: %w", id, err)
	}
	if !found {
		return ErrNotFound
	}
	return nil
}

// readTests puts each connection's last test into its spec.
func readTests(ctx context.Context, q db.Querier, specs []Spec) error {
	if len(specs) == 0 {
		return nil
	}
	ids := make([]string, len(specs))
	at := map[string]int{}
	for i, spec := range specs {
		ids[i], at[spec.ID] = spec.ID, i
	}
	var id string
	var row testRow
	rows, err := q.Query(ctx, "SELECT cluster_id, "+strings.Join(db.Columns(&row), ", ")+" FROM clusters WHERE cluster_id = ANY($1) AND tested_at IS NOT NULL", ids)
	if err != nil {
		return err
	}
	if _, err := pgx.ForEachRow(rows, append([]any{&id}, db.Fields(&row)...), func() error {
		specs[at[id]].Tested = &LastTest{At: utc(row.At), By: row.By, Result: TestResult{Steps: []Step{}, Tone: row.Tone, Summary: row.Summary, Error: row.Error, Version: row.Version,
			Nodes: row.Nodes, NodesMore: row.NodesMore, Who: row.Who, Impersonate: row.Impersonate}}
		return nil
	}); err != nil {
		return err
	}
	var st Step
	rows, err = q.Query(ctx, "SELECT cluster_id, step_key, tone, text FROM cluster_test_steps WHERE cluster_id = ANY($1) ORDER BY cluster_id, position", ids)
	if err != nil {
		return err
	}
	_, err = pgx.ForEachRow(rows, []any{&id, &st.Key, &st.Tone, &st.Text}, func() error {
		if t := specs[at[id]].Tested; t != nil {
			t.Result.Steps = append(t.Result.Steps, st)
		}
		return nil
	})
	return err
}

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
		spec := specOf(row)
		spec.RowVersion = version
		specs = append(specs, spec)
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
		if specs, _, err = read(ctx, q, ""); err != nil {
			return err
		}
		return readTests(ctx, q, specs)
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
		if specs, _, err = read(ctx, q, "AND cluster_id = $1", id); err != nil {
			return err
		}
		return readTests(ctx, q, specs)
	}); err != nil {
		return Spec{}, Credential{}, fmt.Errorf("reading the connection %s: %w", id, err)
	}
	if len(specs) == 0 {
		return Spec{}, Credential{}, ErrNotFound
	}
	spec := specs[0]
	// The local cluster signs in as the console itself and never had a
	// credential kept: its row is read without its Secret, so a Secrets
	// API that fails never hides its saved name and environment.
	if spec.Local {
		return spec, Credential{}, nil
	}
	h, err := s.secrets.credential(ctx, id)
	if err != nil {
		return Spec{}, Credential{}, err
	}
	cred, found := h.Credential, h.found
	// A credential written for another server than the row's is of a save
	// that did not finish: paired, it would go to a server it is not for.
	if why := unsettled(spec, h); why != "" {
		spec.Unsettled = why
		return spec, Credential{}, nil
	}
	if !found {
		if _, said := s.said.LoadOrStore(id, true); !said {
			s.log.Warn("a connection of the database has no Secret, so nothing to sign in with: edit the connection and give its credential again", "cluster", id, "secret", s.secrets.name(id))
		}
	} else {
		s.said.Delete(id)
	}
	return spec, cred, nil
}

// unsettled says why a row and the credential its Secret holds do not go
// together, or "": the Secret was written for another API server
// (AnnotationServer) by a save whose row never committed, or it holds a
// record for another server (recordFor), which a console from before the
// move wrote with that credential after a rollback.
func unsettled(spec Spec, h held) string {
	if !h.found {
		return ""
	}
	if h.server != "" && h.server != serverOrigin(spec.Server) {
		return fmt.Sprintf("Its credential was written for %s, and the connection is saved for %s: a save did not finish. Edit the connection and give its credential again.", h.server, spec.Server)
	}
	if other := recordFor(spec, h.record); other != "" {
		return fmt.Sprintf("Its Secret holds a record of the connection for %s, which a console from before 0.13 wrote with its credential after a rollback, and the database's is for %s. Edit the connection and give its credential again.", other, spec.Server)
	}
	return ""
}

// recordFor is the server of a record a Secret holds when it is another
// server than the row's (by scheme, host and port), or "": the credential
// beside such a record is that server's, not the row's. A record this
// console cannot read says nothing here, and is left as it is.
func recordFor(spec Spec, record []byte) string {
	if record == nil {
		return ""
	}
	rec, err := UnmarshalSpec(record)
	if err != nil || rec.ID != spec.ID || serverOrigin(rec.Server) == serverOrigin(spec.Server) {
		return ""
	}
	return rec.Server
}

// Save writes the row, then the Secret when a credential is given, then
// commits (see the top of the file), and tells the other replicas. A spec
// with a RowVersion is an edit of what was read at that version: the row
// is locked and the save refused with ErrConflict when its version moved
// since (DB-92), so of two replicas editing one connection the second is
// told rather than writing over the first. One without (a new
// connection, a restore) is written whatever is there.
func (s *PGStore) Save(ctx context.Context, spec Spec, c *Credential) error {
	if spec.ID == "" {
		return errors.New("a connection is saved under its identifier, and this one has none")
	}
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		if spec.RowVersion > 0 {
			var now int
			err := tx.QueryRow(ctx, "SELECT version FROM clusters WHERE cluster_id = $1 AND is_kept FOR UPDATE", spec.ID).Scan(&now)
			if errors.Is(err, pgx.ErrNoRows) || err == nil && now != spec.RowVersion {
				return ErrConflict
			}
			if err != nil {
				return err
			}
		}
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
		return s.secrets.putCredential(ctx, spec.ID, spec.Server, *c)
	})
	if errors.Is(err, ErrConflict) {
		return err
	}
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
	// The Secret as it was, to put back when the removal does not commit
	// after the Secret went: a removal said to have failed keeps the
	// connection whole, its credential with it.
	var gone *corev1.Secret
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
		if _, err := tx.Exec(ctx, "DELETE FROM cluster_test_steps WHERE cluster_id = $1", id); err != nil {
			return err
		}
		if err := s.db.Notify(ctx, tx, Topic); err != nil {
			return err
		}
		was, err := s.secrets.secrets.Get(ctx, s.secrets.name(id), metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("reading the connection Secret %s: %w", s.secrets.name(id), err)
		}
		if err := s.secrets.Delete(ctx, id); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		// A try run again after its Secret went keeps the first one's.
		if was != nil && was.Name != "" {
			gone = was
		}
		return nil
	})
	if err != nil {
		if gone != nil {
			s.putBack(ctx, gone)
		}
		return fmt.Errorf("removing the connection %s: %w", id, err)
	}
	if !found {
		return ErrNotFound
	}
	s.said.Delete(id)
	return nil
}

// putBack makes a Secret a removal deleted again, as it was, when the
// removal did not commit. Its request may be what ended: it is given a
// moment of its own.
func (s *PGStore) putBack(ctx context.Context, sec *corev1.Secret) {
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	again := sec.DeepCopy()
	again.ResourceVersion, again.UID, again.CreationTimestamp = "", "", metav1.Time{}
	if _, err := s.secrets.secrets.Create(pctx, again, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		s.log.Error("a removal that did not commit deleted the connection's Secret, and it could not be made again: the connection has nothing to sign in with until it is edited", "secret", sec.Name, "err", err)
	}
}

// Revisions is each connection's revision: its row's version, the time of
// its last test and its Secret's resourceVersion, so a replica notices a
// connection another one saved, a test another one ran and a credential
// replaced by hand in the Secret alike. Either
// half that cannot be read is an error: a list that only looked shorter
// would stop connections that are still saved.
func (s *PGStore) Revisions(ctx context.Context) (map[string]string, error) {
	var specs []Spec
	var versions []int
	if err := s.db.Do(ctx, func(q db.Querier) (err error) {
		if specs, versions, err = read(ctx, q, ""); err != nil {
			return err
		}
		return readTests(ctx, q, specs)
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
		tested := ""
		if spec.Tested != nil {
			tested = strconv.FormatInt(spec.Tested.At.UnixMicro(), 10)
		}
		out[spec.ID] = strconv.Itoa(versions[i]) + "/" + tested + "/" + secrets[spec.ID]
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
// and whose connection has a kept row of the same API server (a record
// for another server is left whole, see recordFor), in one update over the version
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
	// Each kept row, by its Secret's name: the record of a Secret is
	// dropped only beside the row of the same server.
	kept := map[string]Spec{}
	if err := d.Do(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, "SELECT cluster_id, server_url FROM clusters WHERE is_kept")
		if err != nil {
			return err
		}
		var id, server string
		_, err = pgx.ForEachRow(rows, []any{&id, &server}, func() error {
			kept[from.name(id)] = Spec{ID: id, Server: server}
			return nil
		})
		return err
	}); err != nil {
		return 0, err
	}
	n := 0
	var failed, left, other []string
	for _, sec := range with {
		if err := ctx.Err(); err != nil {
			return n, fmt.Errorf("%d of the connections' Secrets still hold their record: %w", len(with)-n-len(left)-len(other), err)
		}
		row, ok := kept[sec.Name]
		if !ok {
			left = append(left, sec.Name)
			continue
		}
		// A record for another server than the row's: a console from
		// before the move made the connection again after a rollback, and
		// the credential beside it is that server's. It is left whole, and
		// the connection does not start until it is edited (unsettled).
		if recordFor(row, sec.Data[keySpec]) != "" {
			other = append(other, sec.Name)
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
	if len(other) > 0 {
		log.Warn("connection Secrets that hold a record for another API server than the database's are left as they are: a console from before 0.13 made the connection again after a rollback. Each does not start until it is edited and given its credential", "secrets", other)
	}
	if len(failed) > 0 {
		return n, errors.New(strings.Join(failed, "; "))
	}
	return n, nil
}
