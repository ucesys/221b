package db_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// What a call returns when the database is not reached is the console's
// sentence and nothing of the driver's (the ninth cluster test's F4: the
// sentence a person was shown, and the audit record of a refused change,
// carried the database's user, its name and its Service's address). The
// call here is the one that meets the outage itself: nothing has found
// the database away before it.
func TestWhatACallReturnsNeverCarriesTheDriversText(t *testing.T) {
	sentence := db.ErrUnavailable.Error()
	shown := func(t *testing.T, cfg db.Config, err error) {
		t.Helper()
		if !errors.Is(err, db.ErrUnavailable) {
			t.Fatalf("the call returned %v", err)
		}
		if err.Error() != sentence {
			t.Errorf("the call's error reads %q, want the sentence alone", err.Error())
		}
		// As a service wraps it, and as the JSON of an answer carries it.
		text := fmt.Errorf("reading the credentials: %w", err).Error() + fmt.Sprintf(" %v %+v %q", err, err, err)
		for what, s := range map[string]string{"the user": cfg.User, "the database's name": cfg.Name, "the host": cfg.Host, "the port": strconv.Itoa(cfg.Port),
			"the driver's words": "failed to connect", "a dial": "dial", "a timeout": "deadline", "a closed connection": "closed", "a refusal": "refused"} {
			if strings.Contains(text, s) {
				t.Errorf("what is shown holds %s: %q", what, text)
			}
		}
	}
	t.Run("a database that is not there", func(t *testing.T) {
		away, d, relayed, _ := awayHandle(t)
		away.Cut()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shown(t, relayed, one(ctx, d))
		shown(t, relayed, d.Tx(ctx, func(tx pgx.Tx) error { _, err := tx.Exec(ctx, "SELECT 1"); return err }))
	})
	t.Run("a database that swallows what it is sent", func(t *testing.T) {
		away, d, relayed, _ := awayHandle(t)
		away.Hang()
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		err := one(ctx, d)
		shown(t, relayed, err)
		// Why can still be asked, by code: the call's time ran out.
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("the cause cannot be asked about: %v", err)
		}
	})
	t.Run("a sign-in the server refuses", func(t *testing.T) {
		// A password changed under the console, a database dropped: the
		// driver's sentence for it names the user, the database and the
		// address, with the server's own.
		cfg := dbtest.Config(t)
		d := dbtest.OpenConfig(t, cfg)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, cause := pgx.Connect(ctx, fmt.Sprintf("host=%s port=%d user=zq7_no_such_user dbname=zq7_no_such_database sslmode=disable", cfg.Host, cfg.Port))
		if cause == nil || !strings.Contains(cause.Error(), "zq7_no_such_user") {
			t.Fatalf("the driver's error for a refused sign-in: %v", cause)
		}
		if !db.Unreachable(cause) {
			t.Error("a connection the server refused to make is not taken for the database not reached")
		}
		refused := cfg
		refused.User, refused.Name = "zq7_no_such_user", "zq7_no_such_database"
		shown(t, refused, d.Note(cause))
	})
	t.Run("what the database refuses is shown as it is", func(t *testing.T) {
		d := dbtest.Open(t)
		ctx := ctxT(t)
		err := d.Do(ctx, func(q db.Querier) error { _, err := q.Exec(ctx, "SELECT 1/0"); return err })
		if err == nil || errors.Is(err, db.ErrUnavailable) || !strings.Contains(err.Error(), "division by zero") {
			t.Errorf("a refusal of what was asked returned %v", err)
		}
		if !d.Health().Available {
			t.Error("a refusal of what was asked changed the health")
		}
	})
}
