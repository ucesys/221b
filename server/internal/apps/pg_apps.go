package apps

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// The applications in the database (Phase 13, DB-62): each a row of
// applications, every field a column, and its commands rows of
// application_commands. The secret part of each (its values file, its
// parameters and its commands' parameters) stays in the application's
// Secret, through SecretStore: no column holds any of it, and nothing in
// this file logs it or puts it in an error.
//
// The two halves are written in one order. A save changes the row in a
// transaction, writes the Secret while the transaction is open, and
// commits: a Secret that cannot be written leaves the row as it was, and a
// console that stops between the two leaves the Secret a save ahead of a
// row whose version did not move, so the save that never answered is made
// again over the same version. A removal deletes the row, then the Secret.
// So a row may be met without its Secret (it reads with no values, and is
// said once in the log) and a Secret without its row (it is not an
// application, and is left where it is).

// ImportID is the name the one-time copy of the applications out of their
// Secrets is recorded under.
const ImportID = "secret/applications"

// PGStore keeps the applications in a database, and their secret part in
// their Secrets.
type PGStore struct {
	db      *db.DB
	secrets *SecretStore
	log     *slog.Logger
	// said are the applications whose missing or unreadable Secret was
	// logged, so each is said once.
	said sync.Map
}

// NewPGStore keeps the applications in d and their secret part in secrets.
func NewPGStore(d *db.DB, secrets *SecretStore, log *slog.Logger) *PGStore {
	if log == nil {
		log = slog.Default()
	}
	return &PGStore{db: d, secrets: secrets, log: log}
}

func (s *PGStore) Kind() string { return "database" }

// appRow is a row of applications.
type appRow struct {
	ID        string `col:"application_id"`
	Cluster   string `col:"cluster_id"`
	Namespace string `col:"namespace"`
	Name      string `col:"name"`

	SourceKind string `col:"source_kind"`
	SourceURL  string `col:"source_url"`
	SourcePath string `col:"source_path"`
	Chart      string `col:"chart"`
	Ref        string `col:"ref"`
	RefKind    string `col:"ref_kind"`

	RepositoryID   *string `col:"repository_id"`
	RepositoryName string  `col:"repository_name"`
	Credential     *string `col:"credential_id"`
	CredMigrated   bool    `col:"is_credential_migrated"`

	Commit        string     `col:"commit"`
	CommitDate    *time.Time `col:"commit_at"`
	CommitSubject string     `col:"commit_subject"`
	ChartVersion  string     `col:"chart_version"`

	ValuesFile      string `col:"values_file"`
	CreateNamespace bool   `col:"is_namespace_created"`
	IgnoreDefaults  bool   `col:"is_defaults_ignored"`
	Fleet           string `col:"fleet_key"`
	FleetLabel      string `col:"fleet_label"`

	MatchedRevision int    `col:"matched_revision"`
	SourceDiff      string `col:"source_diff"`
	AdoptedRevision int    `col:"adopted_revision"`

	CreatorNamespaces []string   `col:"creator_namespaces"`
	CreatedBy         string     `col:"created_by"`
	Created           *time.Time `col:"created_at"`
	UpdatedBy         string     `col:"updated_by"`
	Updated           *time.Time `col:"updated_at"`
	Version           int        `col:"version"`
	// FleetID is the row of the fleet Fleet names in the cluster, which a
	// write works out (DB-92); emptied when the fleet is removed, while
	// Fleet keeps its id.
	FleetID *string `col:"fleet_id"`
}

// rowOf is a record as its row, without its keys to the repository and
// the credential, which a write works out.
func rowOf(r Record) appRow {
	ns := r.CreatorNamespaces
	if ns == nil {
		ns = []string{}
	}
	return appRow{Cluster: r.Cluster, Namespace: r.Namespace, Name: r.Name,
		SourceKind: r.Source.Kind, SourceURL: r.Source.Repo, SourcePath: r.Source.Path, Chart: r.Source.Chart, Ref: r.Source.Ref, RefKind: r.Source.RefKind,
		RepositoryName: r.Source.Repository, CredMigrated: r.CredMigrated,
		Commit: r.Commit, CommitDate: nullable(r.CommitDate), CommitSubject: r.CommitSubject, ChartVersion: r.ChartVersion,
		ValuesFile: r.ValuesFile, CreateNamespace: r.CreateNamespace, IgnoreDefaults: r.IgnoreDefaults, Fleet: r.Fleet, FleetLabel: r.FleetLabel,
		MatchedRevision: r.MatchedRevision, SourceDiff: r.SourceDiff, AdoptedRevision: r.AdoptedRevision,
		CreatorNamespaces: ns, CreatedBy: r.CreatedBy, Created: nullable(r.Created), UpdatedBy: r.UpdatedBy, Updated: nullable(r.Updated)}
}

// recordOf is a row as its record, without its secret part. repository is
// the name the repository the row points at has now, nil when it points at
// none: the record then reads the name as it was last saved.
func recordOf(row appRow, repository *string) Record {
	r := Record{Version: RecordVersion, Cluster: row.Cluster, Namespace: row.Namespace, Name: row.Name,
		Source: Source{Kind: row.SourceKind, Repo: row.SourceURL, Path: row.SourcePath, Chart: row.Chart, Repository: row.RepositoryName, Ref: row.Ref, RefKind: row.RefKind},
		Commit: row.Commit, CommitDate: utc(row.CommitDate), CommitSubject: row.CommitSubject, ChartVersion: row.ChartVersion,
		ValuesFile: row.ValuesFile, CreateNamespace: row.CreateNamespace, IgnoreDefaults: row.IgnoreDefaults, Fleet: row.Fleet, FleetLabel: row.FleetLabel,
		MatchedRevision: row.MatchedRevision, SourceDiff: row.SourceDiff, AdoptedRevision: row.AdoptedRevision,
		CredMigrated: row.CredMigrated, CreatedBy: row.CreatedBy, Created: utc(row.Created), UpdatedBy: row.UpdatedBy, Updated: utc(row.Updated)}
	if repository != nil {
		r.Source.Repository = *repository
	}
	if row.Credential != nil {
		r.Credential = *row.Credential
	}
	// A copy: the rows are read into the same variables one after another.
	if len(row.CreatorNamespaces) > 0 {
		r.CreatorNamespaces = slices.Clone(row.CreatorNamespaces)
	}
	return r
}

// selectApps reads every column of applications, the two uuid keys as
// text, and the name the repository a row points at has now.
var selectApps = func() string {
	cols := db.Columns(&appRow{})
	for i, c := range cols {
		if c == "application_id" || c == "repository_id" || c == "fleet_id" {
			c += "::text"
		}
		cols[i] = "a." + c
	}
	return "SELECT " + strings.Join(cols, ", ") + ", r.name FROM applications a LEFT JOIN repositories r ON r.repository_id = a.repository_id "
}()

// read is the applications that where picks, with their commands, each
// with the version of its row, and without their secret part.
func read(ctx context.Context, q db.Querier, where string, args ...any) ([]Record, error) {
	rows, err := q.Query(ctx, selectApps+where, args...)
	if err != nil {
		return nil, err
	}
	var row appRow
	var repository *string
	var out []Record
	at := map[string]int{}
	if _, err := pgx.ForEachRow(rows, append(db.Fields(&row), &repository), func() error {
		r := recordOf(row, repository)
		r.Rev = strconv.Itoa(row.Version)
		at[row.ID] = len(out)
		out = append(out, r)
		return nil
	}); err != nil || len(out) == 0 {
		return nil, err
	}
	ids := make([]string, 0, len(at))
	for id := range at {
		ids = append(ids, id)
	}
	rows, err = q.Query(ctx, `SELECT application_id::text, name, note, created_at, created_by, updated_at, updated_by
		FROM application_commands WHERE application_id = ANY($1::uuid[]) ORDER BY application_id, position`, ids)
	if err != nil {
		return nil, err
	}
	var id string
	var c Command
	var created, updated *time.Time
	if _, err := pgx.ForEachRow(rows, []any{&id, &c.Name, &c.Note, &created, &c.CreatedBy, &updated, &c.UpdatedBy}, func() error {
		c.Created, c.Updated = utc(created), utc(updated)
		out[at[id]].Commands = append(out[at[id]].Commands, c)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

const oneApp = "WHERE a.cluster_id = $1 AND a.namespace = $2 AND a.name = $3"

// join puts a record's secret part into it, from its Secret; sec is nil
// when the application has none. A row without a Secret it can read is an
// application with no values, said once: a save gives it its values again.
func (s *PGStore) join(r *Record, sec *corev1.Secret) {
	var part secretPart
	ok := false
	if sec != nil {
		part, ok = readPart(sec)
		r.HasToken = len(sec.Data[keyToken]) > 0
	}
	part.into(r)
	if ok {
		return
	}
	if _, said := s.said.LoadOrStore(ID(r.Cluster, r.Namespace, r.Name), true); !said {
		why := "its Secret is not there"
		if sec != nil {
			why = "its Secret holds no values this console can read"
		}
		s.log.Warn("an application of the database reads with no values, parameters or commands' parameters: "+why+"; saving the application gives them again",
			"cluster", r.Cluster, "namespace", r.Namespace, "name", r.Name, "secret", s.secrets.name(r.Cluster, r.Namespace, r.Name))
	}
}

// List is every application that has a row, each with the secret part its
// Secret holds. A Secret without a row is not an application.
func (s *PGStore) List(ctx context.Context) ([]Record, error) {
	var recs []Record
	if err := s.db.Snapshot(ctx, func(q db.Querier) (err error) {
		recs, err = read(ctx, q, "")
		return err
	}); err != nil {
		return nil, fmt.Errorf("reading the applications: %w", err)
	}
	if len(recs) == 0 {
		return nil, nil
	}
	secs, err := s.secrets.all(ctx)
	if err != nil {
		return nil, err
	}
	for i := range recs {
		recs[i].Rev = ""
		s.join(&recs[i], secs[s.secrets.name(recs[i].Cluster, recs[i].Namespace, recs[i].Name)])
	}
	sortRecords(recs)
	return recs, nil
}

func (s *PGStore) Get(ctx context.Context, cluster, namespace, name string) (Record, string, error) {
	var recs []Record
	if err := s.db.Snapshot(ctx, func(q db.Querier) (err error) {
		recs, err = read(ctx, q, oneApp, cluster, namespace, name)
		return err
	}); err != nil {
		return Record{}, "", fmt.Errorf("reading the application: %w", err)
	}
	if len(recs) == 0 {
		return Record{}, "", ErrNotFound
	}
	sec, err := s.secrets.one(ctx, cluster, namespace, name)
	if err != nil {
		return Record{}, "", err
	}
	rec := recs[0]
	s.join(&rec, sec)
	token := ""
	if sec != nil {
		token = string(sec.Data[keyToken])
	}
	return rec, token, nil
}

// Save writes the row, then the Secret, then commits (see the top of the
// file). A record that came from Get is saved only over the version Get
// read; one that did not replaces what is there, as in the other stores.
func (s *PGStore) Save(ctx context.Context, r Record, token *string) error {
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		fresh, err := writeApp(ctx, tx, r, true)
		if err != nil {
			return err
		}
		return s.secrets.putPart(ctx, r, token, fresh)
	})
	if err != nil && !errors.Is(err, ErrConflict) {
		return fmt.Errorf("saving the application: %w", err)
	}
	if err == nil {
		s.said.Delete(ID(r.Cluster, r.Namespace, r.Name))
	}
	return err
}

// writeApp writes a record's row and its commands' rows, without its
// secret part, and says whether the row is new. The row is locked first,
// so two saves of one application take turns, the Secret's write with
// them. held says the record's Rev is what the row must be at; the copy
// from the Secrets, which has none, only adds.
func writeApp(ctx context.Context, tx pgx.Tx, r Record, held bool) (fresh bool, err error) {
	row := rowOf(r)
	var was struct {
		repository     *string
		repositoryName string
	}
	err = tx.QueryRow(ctx, `SELECT application_id::text, version, repository_id::text, repository_name FROM applications
		WHERE cluster_id = $1 AND namespace = $2 AND name = $3 FOR UPDATE`, r.Cluster, r.Namespace, r.Name).Scan(&row.ID, &row.Version, &was.repository, &was.repositoryName)
	fresh = errors.Is(err, pgx.ErrNoRows)
	if err != nil && !fresh {
		return false, err
	}
	if held && r.Rev != "" && (fresh || r.Rev != strconv.Itoa(row.Version)) {
		return false, ErrConflict
	}
	row.Version++
	// The repository it names, by its row, so a repository that is given a
	// new name is followed there with nothing written here. A name no
	// repository has is kept as it is; when it is the name this row last
	// saved and the row still points at a repository, that repository was
	// renamed after the record was read, and the row goes on pointing at it.
	if name := r.Source.Repository; name != "" {
		var id string
		switch err := tx.QueryRow(ctx, "SELECT repository_id::text FROM repositories WHERE name = $1 ORDER BY position LIMIT 1", name).Scan(&id); {
		case err == nil:
			row.RepositoryID = &id
		case !errors.Is(err, pgx.ErrNoRows):
			return false, err
		case !fresh && was.repository != nil && name == was.repositoryName:
			row.RepositoryID = was.repository
		}
	}
	// The fleet it is deployed to, by its row (DB-92): none for a fleet the
	// cluster has no row of.
	if r.Fleet != "" {
		var id string
		switch err := tx.QueryRow(ctx, "SELECT fleet_id::text FROM fleets WHERE coalesce(cluster_id, '') = $1 AND fleet_key = $2 ORDER BY fleet_id LIMIT 1", r.Cluster, r.Fleet).Scan(&id); {
		case err == nil:
			row.FleetID = &id
		case !errors.Is(err, pgx.ErrNoRows):
			return false, err
		}
	}
	// The cluster's row, and the credential's, with only their ids when
	// their own records are not in the database: the application names them.
	if _, err := tx.Exec(ctx, "INSERT INTO clusters (cluster_id) VALUES ($1) ON CONFLICT (cluster_id) DO NOTHING", r.Cluster); err != nil {
		return false, err
	}
	if r.Credential != "" {
		row.Credential = &r.Credential
		if _, err := tx.Exec(ctx, "INSERT INTO credentials (credential_id) VALUES ($1) ON CONFLICT (credential_id) DO NOTHING", r.Credential); err != nil {
			return false, err
		}
	}
	if fresh {
		row.ID = db.NewID()
		if _, err := tx.Exec(ctx, db.InsertSQL("applications", &row), db.Fields(&row)...); err != nil {
			if db.IsUnique(err) {
				// Another replica made it in this instant.
				return false, ErrConflict
			}
			return false, err
		}
	} else {
		fields := db.Fields(&row)
		if _, err := tx.Exec(ctx, db.UpdateSQL("applications", &row, "application_id = $"+strconv.Itoa(len(fields)+1)), append(fields, row.ID)...); err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM application_commands WHERE application_id = $1", row.ID); err != nil {
			return false, err
		}
	}
	if len(r.Commands) > 0 {
		b := &pgx.Batch{}
		for i, c := range r.Commands {
			b.Queue(`INSERT INTO application_commands (application_id, position, name, note, created_at, created_by, updated_at, updated_by)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, row.ID, i, c.Name, c.Note, nullable(c.Created), c.CreatedBy, nullable(c.Updated), c.UpdatedBy)
		}
		if err := tx.SendBatch(ctx, b).Close(); err != nil {
			return false, err
		}
	}
	return fresh, nil
}

// Delete removes the row, then the Secret, as a person's removal of an
// application always did. A Secret that could not be deleted is an error,
// and holds nothing that is read: asking again removes it.
func (s *PGStore) Delete(ctx context.Context, cluster, namespace, name string) error {
	if err := s.db.Do(ctx, func(q db.Querier) error {
		_, err := q.Exec(ctx, "DELETE FROM applications WHERE cluster_id = $1 AND namespace = $2 AND name = $3", cluster, namespace, name)
		return err
	}); err != nil {
		return fmt.Errorf("removing the application's record: %w", err)
	}
	s.said.Delete(ID(cluster, namespace, name))
	return s.secrets.Delete(ctx, cluster, namespace, name)
}

// Unmatched compares the two halves at a start: the applications of the
// database whose Secret is not there, as cluster/namespace/name, and the
// application Secrets no row is of, by name. Neither is changed.
func (s *PGStore) Unmatched(ctx context.Context) (noSecret, noRow []string, err error) {
	var recs []Record
	if err := s.db.Snapshot(ctx, func(q db.Querier) (err error) {
		recs, err = read(ctx, q, "")
		return err
	}); err != nil {
		return nil, nil, err
	}
	secs, err := s.secrets.all(ctx)
	if err != nil {
		return nil, nil, err
	}
	of := map[string]bool{}
	for _, r := range recs {
		name := s.secrets.name(r.Cluster, r.Namespace, r.Name)
		of[name] = true
		if secs[name] == nil {
			noSecret = append(noSecret, r.Cluster+"/"+r.Key())
		}
	}
	for name := range secs {
		if !of[name] {
			noRow = append(noRow, name)
		}
	}
	sort.Strings(noSecret)
	sort.Strings(noRow)
	return noSecret, noRow, nil
}

// --- The one-time copy out of the Secrets, and their rewrite (DB-63) ---

// CopyFromSecrets copies the records the applications' Secrets keep under
// "spec.json" into the database, once, and says how many it copied: each
// becomes a row and its commands' rows, without its values, parameters and
// commands' parameters, which are not read into anything here. An
// application the database already has stays as the database has it. A
// Secret whose record cannot be read is skipped, and said: it has no row,
// so it is never rewritten. Secrets that cannot be listed are an error:
// nothing is recorded, and the next start tries again.
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
		names := make([]string, 0, len(secs))
		for name := range secs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			spec, has := secs[name].Data[keySpec]
			if !has {
				continue
			}
			// The error's text may quote the record: it is not passed on.
			r, err := unmarshal(spec)
			if err != nil || from.name(r.Cluster, r.Namespace, r.Name) != name {
				log.Warn("an application's Secret is not copied into the database: its record cannot be read, or is of another application; the Secret is left as it is", "secret", name)
				continue
			}
			var one int
			if err := tx.QueryRow(ctx, "SELECT 1 FROM applications a "+oneApp, r.Cluster, r.Namespace, r.Name).Scan(&one); err == nil {
				continue
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return 0, err
			}
			if _, err := writeApp(ctx, tx, r, false); err != nil {
				return 0, err
			}
			n++
		}
		return n, nil
	})
	if err != nil {
		n = 0
	}
	return n, err
}

// RewriteSecrets takes the records out of the applications' Secrets once
// the database keeps them: each Secret that still holds "spec.json" and
// whose application has a row is rewritten once (SecretStore.dropSpec),
// its values, parameters and commands' parameters kept under "set.json",
// its token and its labels as they were. It says how many it rewrote. No
// Secret is deleted. A Secret with no row is left whole, and said: its
// record could not be read at the copy, or a console from before the move
// wrote it since. One that fails does not stop the others.
func RewriteSecrets(ctx context.Context, d *db.DB, from *SecretStore, log *slog.Logger) (int, error) {
	if log == nil {
		log = slog.Default()
	}
	secs, err := from.all(ctx)
	if err != nil {
		return 0, err
	}
	var names []string
	for name, sec := range secs {
		if _, has := sec.Data[keySpec]; has {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return 0, nil
	}
	sort.Strings(names)
	rows := map[string]bool{}
	if err := d.Do(ctx, func(q db.Querier) error {
		found, err := q.Query(ctx, "SELECT cluster_id, namespace, name FROM applications")
		if err != nil {
			return err
		}
		var cluster, namespace, name string
		_, err = pgx.ForEachRow(found, []any{&cluster, &namespace, &name}, func() error {
			rows[from.name(cluster, namespace, name)] = true
			return nil
		})
		return err
	}); err != nil {
		return 0, err
	}
	n := 0
	var failed []string
	var left []string
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return n, fmt.Errorf("%d of the applications' Secrets are not rewritten yet: %w", len(names)-n-len(left), err)
		}
		if !rows[name] {
			left = append(left, name)
			continue
		}
		switch done, err := from.dropSpec(ctx, secs[name]); {
		case errors.Is(err, errLeftWhole):
			left = append(left, name)
		case err != nil:
			failed = append(failed, err.Error())
		case done:
			n++
		}
	}
	if len(left) > 0 {
		log.Warn("application Secrets that hold a record and are of no application of the database are left as they are, and are not read", "secrets", left)
	}
	if len(failed) > 0 {
		return n, errors.New(strings.Join(failed, "; "))
	}
	return n, nil
}
