// Package db is the console's connection to PostgreSQL (Phase 13, DB-01):
// the pool, transactions, the schema's migrations, the locks and notices the
// replicas coordinate through, and whether the database answers. It is the
// only package that imports the driver; each domain keeps its records
// through a repository of its own, written on what is here (ADR-162).
//
// The database is the chart's bundled one or one the operator runs
// (database.mode); to this package both are an address. Nothing here keeps
// a password: it is read from a file, a mounted Secret, each time a
// connection is opened, so rotating it needs no restart (DB-04).
package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MinServerVersion is the oldest PostgreSQL the console runs on (DB-03),
// as server_version_num gives it.
const MinServerVersion = 160000

// DefaultPoll is how often what a notice would have said is looked for
// anyway, so a lost notice delays a change and never loses it (DB-12).
const DefaultPoll = 30 * time.Second

// ErrNotFound is what a repository returns for a record that is not there.
var ErrNotFound = errors.New("not found")

// ErrConflict is what a repository returns when a record changed since the
// version a save was made against.
var ErrConflict = errors.New("changed since it was read")

// ErrUnavailable is what a call returns while the database cannot be
// reached. Its text is the sentence a page shows, and the whole of it:
// what the driver said (the database's user, its name, its address) is
// logged, and is in no error's text (unavailableError).
var ErrUnavailable = errors.New("the database is unavailable; sign-in and the pages read live from the clusters still work")

// Config is where the database is and how to sign in to it.
type Config struct {
	// Host, Port, Name and User are the read-write endpoint, the database
	// and the role that owns its tables. An empty Host means no database:
	// the console keeps its records in ConfigMaps as before.
	Host string
	Port int
	Name string
	User string
	// PasswordFile holds the role's password; empty when the server takes
	// a client certificate alone, or an IAM token.
	PasswordFile string
	// IAMAuth is how the console signs in with a token of the cloud's IAM
	// in place of a password (DB-83): IAMNone, IAMAWS (RDS and Aurora) or
	// IAMGCP (Cloud SQL). Tokens makes the token; AWSRegion is the RDS
	// database's region.
	IAMAuth   string
	AWSRegion string
	Tokens    TokenSource
	// SSLMode is verify-full, verify-ca, require or disable, as libpq
	// means them. CAFile verifies the server; ClientCertFile and
	// ClientKeyFile sign the console in where the server asks for one.
	SSLMode        string
	CAFile         string
	ClientCertFile string
	ClientKeyFile  string
	// MaxConns bounds this replica's pool.
	MaxConns int
	// ListenerHost is a direct address for the connections that must stay
	// one session, the listener's and the locks', when Host is a pooler in
	// transaction mode (DB-06). Empty uses Host.
	ListenerHost string
	// Poll is DefaultPoll unless set.
	Poll time.Duration
	// AppName is what pg_stat_activity shows for this replica.
	AppName string
}

// Enabled says whether a database is configured at all.
func (c Config) Enabled() bool { return c.Host != "" }

// Validate refuses what cannot work, in the words of the chart's values.
func (c Config) Validate() error {
	if !c.Enabled() {
		return nil
	}
	switch c.SSLMode {
	case "verify-full", "verify-ca", "require", "disable":
	default:
		return fmt.Errorf("database sslMode %q: give verify-full, verify-ca, require or disable", c.SSLMode)
	}
	if c.Name == "" || c.User == "" {
		return errors.New("the database needs a name and a user")
	}
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("database port %d: give 1 to 65535", c.Port)
	}
	if (c.ClientCertFile == "") != (c.ClientKeyFile == "") {
		return errors.New("a database client certificate needs both its certificate and its key")
	}
	if c.MaxConns < 2 {
		return fmt.Errorf("database maxConnections %d: give 2 or more", c.MaxConns)
	}
	switch c.IAMAuth {
	case "", IAMNone:
	case IAMAWS, IAMGCP:
		if c.PasswordFile != "" {
			return fmt.Errorf("database iamAuth %s signs in with a token in place of the password: give no password Secret", c.IAMAuth)
		}
		if c.SSLMode == "disable" {
			return fmt.Errorf("database iamAuth %s sends its token over TLS only: give sslMode verify-full, verify-ca or require", c.IAMAuth)
		}
		if c.IAMAuth == IAMAWS && c.AWSRegion == "" {
			return errors.New("database iamAuth aws needs the database's region")
		}
	default:
		return fmt.Errorf("database iamAuth %q: give none, aws or gcp", c.IAMAuth)
	}
	return nil
}

// IAM reports whether the console signs in with an IAM token.
func (c Config) IAM() bool { return c.IAMAuth == IAMAWS || c.IAMAuth == IAMGCP }

// dsn is the connection string for one host, in libpq's keyword form. The
// password is not in it; BeforeConnect reads it.
func (c Config) dsn(host string) string {
	kv := []string{"host", host, "port", strconv.Itoa(c.Port), "dbname", c.Name, "user", c.User, "sslmode", c.SSLMode, "connect_timeout", "5"}
	if c.CAFile != "" {
		kv = append(kv, "sslrootcert", c.CAFile)
	}
	if c.ClientCertFile != "" {
		kv = append(kv, "sslcert", c.ClientCertFile, "sslkey", c.ClientKeyFile)
	}
	if c.AppName != "" {
		kv = append(kv, "application_name", c.AppName)
	}
	var b strings.Builder
	for i := 0; i < len(kv); i += 2 {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(kv[i])
		b.WriteString("='")
		b.WriteString(strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(kv[i+1]))
		b.WriteByte('\'')
	}
	return b.String()
}

// DB is an open database. Its methods are safe for use by every goroutine.
type DB struct {
	cfg  Config
	log  *slog.Logger
	pool *pgxpool.Pool
	// direct holds the sessions that must not pass through a pooler: the
	// listener's and the advisory locks'.
	direct *pgxpool.Pool

	mu     sync.Mutex
	health Health
	onUp   []func()
	subs   map[string][]func()
	// probing says a probe is finding out whether the database is there,
	// answered when one last found that it is, and closed that the handle
	// is closed (away.go).
	probing  bool
	answered time.Time
	closed   bool
	done     chan struct{}

	// conns are the connections open to the database and dialing the ones
	// being made, so that both can be ended when a probe finds the
	// database away; dropped says it did, and none is made until it is
	// back.
	connMu  sync.Mutex
	conns   map[*trackedConn]struct{}
	dialing map[uint64]context.CancelFunc
	dialSeq uint64
	dropped bool
}

// Health is whether the database answered the last probe (away.go).
type Health struct {
	Available bool `json:"available"`
	// Since is when it last changed between answering and not.
	Since time.Time `json:"since"`
	// Error is why not, in the console's words, when it is not: never the
	// driver's text, which is logged.
	Error string `json:"error,omitempty"`
}

// Open connects to the database and checks its version. It does not
// migrate; Migrate does. A database that does not answer is an error here;
// the console tries for a while and then does not start (DB-02).
func Open(ctx context.Context, cfg Config, log *slog.Logger) (*DB, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Poll <= 0 {
		cfg.Poll = DefaultPoll
	}
	if log == nil {
		log = slog.Default()
	}
	if cfg.IAM() && cfg.Tokens == nil {
		return nil, fmt.Errorf("database iamAuth %s: nothing makes its tokens", cfg.IAMAuth)
	}
	d := newDB(cfg, log)
	var err error
	if d.pool, err = d.open(ctx, cfg.Host, int32(cfg.MaxConns)); err != nil {
		return nil, err
	}
	direct := cfg.ListenerHost
	if direct == "" {
		direct = cfg.Host
	}
	// One session for the listener and a few for the locks held at once.
	if d.direct, err = d.open(ctx, direct, 4); err != nil {
		d.pool.Close()
		return nil, err
	}
	var version int
	if err := d.pool.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil {
		d.Close()
		return nil, fmt.Errorf("connecting to the database at %s:%d: %w", cfg.Host, cfg.Port, describe(err))
	}
	if version < MinServerVersion {
		d.Close()
		return nil, fmt.Errorf("the database runs PostgreSQL %d, and the console needs %d or later", version/10000, MinServerVersion/10000)
	}
	var stamps string
	if err := d.pool.QueryRow(ctx, "SELECT current_setting('track_commit_timestamp')").Scan(&stamps); err == nil && stamps != "on" {
		log.Warn("the database has track_commit_timestamp off; turn it on before running two sites against it")
	}
	d.health = Health{Available: true, Since: time.Now()}
	return d, nil
}

func newDB(cfg Config, log *slog.Logger) *DB {
	return &DB{cfg: cfg, log: log, subs: map[string][]func(){}, done: make(chan struct{}), conns: map[*trackedConn]struct{}{}, dialing: map[uint64]context.CancelFunc{}}
}

func (d *DB) open(ctx context.Context, host string, max int32) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(d.cfg.dsn(host))
	if err != nil {
		return nil, fmt.Errorf("the database settings: %w", err)
	}
	// Every connection is one the console can end when a probe finds the
	// database away (away.go).
	pc.ConnConfig.DialFunc = d.dial(pc.ConnConfig.DialFunc)
	pc.MaxConns = max
	pc.MaxConnIdleTime = 5 * time.Minute
	pc.HealthCheckPeriod = 30 * time.Second
	pc.BeforeConnect = d.password
	return pgxpool.NewWithConfig(ctx, pc)
}

// password reads the role's password into a connection's settings, each
// time a connection is opened (DB-04), or, with iamAuth, makes the IAM
// token that stands for it (DB-83). Neither is logged or put in an error.
func (d *DB) password(ctx context.Context, cc *pgx.ConnConfig) error {
	if d.cfg.IAM() {
		t, err := d.cfg.Tokens.Token(ctx)
		if err != nil {
			return fmt.Errorf("making the database's %s IAM token: %w", d.cfg.IAMAuth, err)
		}
		cc.Password = t.Value
		return nil
	}
	file := d.cfg.PasswordFile
	if file == "" {
		return nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("reading the database password: %w", err)
	}
	cc.Password = strings.TrimRight(string(b), "\r\n")
	return nil
}

// Close ends every connection.
func (d *DB) Close() {
	d.mu.Lock()
	if !d.closed {
		d.closed = true
		close(d.done)
	}
	d.mu.Unlock()
	if d.direct != nil {
		d.direct.Close()
	}
	if d.pool != nil {
		d.pool.Close()
	}
}

// Pool is the pool repositories query through.
func (d *DB) Pool() *pgxpool.Pool { return d.pool }

// Querier is what a repository's read or write runs on: the pool, or the
// transaction it is part of.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Tx runs fn in one transaction: committed when fn returns nil, rolled
// back otherwise. A transaction PostgreSQL ended because another one
// changed the same rows first is run again, a few times, so fn must read
// what it writes from inside it.
func (d *DB) Tx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	if err := d.gate(); err != nil {
		return err
	}
	defer d.waiting()()
	var err error
	for try := 0; try < 4; try++ {
		err = pgx.BeginFunc(ctx, d.pool, fn)
		var pg *pgconn.PgError
		if errors.As(err, &pg) && (pg.Code == "40001" || pg.Code == "40P01") {
			continue
		}
		break
	}
	return d.note(err)
}

// Do runs one read or write that needs no transaction. While the database
// is away it returns ErrUnavailable at once, as every call does (gate).
func (d *DB) Do(ctx context.Context, fn func(q Querier) error) error {
	if err := d.gate(); err != nil {
		return err
	}
	defer d.waiting()()
	return d.note(fn(d.pool))
}

// Snapshot runs a read made of several statements on one snapshot of the
// database: a read-only transaction in which every statement sees what
// was committed when the first one ran. Without it each statement sees
// what is committed when it runs, so a read of a parent's rows and its
// children's can return the children of one write beside the parent of
// the next.
func (d *DB) Snapshot(ctx context.Context, fn func(q Querier) error) error {
	if err := d.gate(); err != nil {
		return err
	}
	defer d.waiting()()
	return d.note(pgx.BeginTxFunc(ctx, d.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		return fn(tx)
	}))
}

// note is given what a call met. A failure to reach the database is
// returned as ErrUnavailable, without the driver's text, and has a probe
// find out whether the database is there (doubt): the call's failure alone
// does not say so, since a slow query on a database that answers runs out
// of its time the same way.
func (d *DB) note(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrUnavailable):
		return err
	case unreachable(err):
		d.doubt(describe(err))
		return unavailable(describe(err))
	}
	return err
}

// Health is how the database last answered.
func (d *DB) Health() Health {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.health
}

// OnAvailable calls fn each time the database answers again after not
// answering: what waited for it starts over.
func (d *DB) OnAvailable(fn func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onUp = append(d.onUp, fn)
}

// set is the probe's: what it found, why in the console's words, and the
// driver's own text for the log.
func (d *DB) set(ok bool, why, detail string) {
	d.mu.Lock()
	changed := d.health.Available != ok
	if changed {
		d.health = Health{Available: ok, Since: time.Now(), Error: why}
	} else if !ok {
		d.health.Error = why
	}
	up := append([]func(){}, d.onUp...)
	d.mu.Unlock()
	if !changed {
		return
	}
	if ok {
		d.log.Info("the database answers again")
		for _, fn := range up {
			go fn()
		}
		return
	}
	d.log.Error("the database is unavailable; pages that need it say so at once, and the rest go on", "why", why, "err", detail)
}

// Watch asks the database whether it is there every interval until ctx
// ends, so that an outage is found also while nobody uses the console, and
// a call left waiting with no time of its own is ended. While the database
// is away the probe asks, by itself (away.go).
func (d *DB) Watch(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !d.Health().Available {
				continue
			}
			pctx, cancel := context.WithTimeout(ctx, probeTime)
			err := d.pool.Ping(pctx)
			cancel()
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				d.doubt(describe(err))
			}
		}
	}
}

// unreachable says whether err is the database not answering, rather than
// the database refusing what was asked.
func unreachable(err error) bool {
	// A connection that could not be made is the database not reached,
	// whatever the server said while refusing it (a password changed under
	// the console): the driver's sentence for it names the user, the
	// database and the address, and is never a person's to read.
	var ce *pgconn.ConnectError
	if errors.As(err, &ce) {
		return true
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		// Class 08 is a connection exception, 57P a shutdown, and 53300
		// too many connections; 25006 is a write sent to a standby during
		// a failover.
		return strings.HasPrefix(pg.Code, "08") || strings.HasPrefix(pg.Code, "57P") || pg.Code == "53300" || pg.Code == "25006"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, pgx.ErrNoRows) || errors.Is(err, pgx.ErrTxClosed) {
		return false
	}
	return pgconn.SafeToRetry(err) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, net.ErrClosed) || errors.Is(err, errAwayDial) || strings.Contains(err.Error(), "conn closed") || strings.Contains(err.Error(), "connection reset") || strings.Contains(err.Error(), "broken pipe") || strings.Contains(err.Error(), "unexpected EOF")
}

// describe shortens the driver's errors to what an operator needs.
func describe(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return fmt.Errorf("%s (PostgreSQL %s)", pg.Message, pg.Code)
	}
	return err
}

// NewID is a new key: a UUIDv7, which sorts by when it was made and needs
// no sequence, so two sites can make keys at once (model rule 3).
func NewID() string {
	id, err := uuid.NewV7()
	if err != nil {
		// The system's random source failed; nothing sensible can follow.
		panic("making an id: " + err.Error())
	}
	return id.String()
}

// IsUnique says whether err is a row refused because its key or a unique
// column is taken.
func IsUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// IsForeignKey says whether err is a row refused because what it points to
// is not there, or a delete refused because rows still point to it.
func IsForeignKey(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23503"
}
