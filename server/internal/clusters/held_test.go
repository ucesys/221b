package clusters_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ucesys/221b/server/internal/clusters"
	"github.com/ucesys/221b/server/internal/clusters/clusterstest"
)

// An edit of a connection is saved only over the version it read
// (DB-92): of two replicas editing one connection, the one that saves
// second is refused with ErrConflict and changes nothing; a save that
// carries no version (a new connection, a restore) is written whatever
// is there; a connection test is no edit and moves no version, though the
// other replicas still see it.
func TestAnEditOfAConnectionIsHeldToTheVersionItRead(t *testing.T) {
	test := func(t *testing.T, store clusters.Store) {
		ctx := context.Background()
		c := fullCred("staging")
		if err := store.Save(ctx, fullSpec("staging"), &c); err != nil {
			t.Fatal(err)
		}
		one, _, err := store.Get(ctx, "staging")
		if err != nil || one.RowVersion <= 0 {
			t.Fatalf("read back at version %d: %v", one.RowVersion, err)
		}
		two := one
		one.Description, two.Description = "the first edit", "the second edit"
		if err := store.Save(ctx, one, nil); err != nil {
			t.Fatalf("the first edit: %v", err)
		}
		if err := store.Save(ctx, two, nil); !errors.Is(err, clusters.ErrConflict) {
			t.Fatalf("the second edit over the same version: %v, want ErrConflict", err)
		}
		now, _, _ := store.Get(ctx, "staging")
		if now.Description != "the first edit" || now.RowVersion != one.RowVersion+1 {
			t.Fatalf("after the edits: %q at version %d (read at %d)", now.Description, now.RowVersion, one.RowVersion)
		}
		if keeper, ok := store.(clusters.TestKeeper); ok {
			before, _ := store.Revisions(ctx)
			if err := keeper.SaveTest(ctx, "staging", clusters.LastTest{At: at, By: "ann", Result: clusters.TestResult{Tone: "ok", Summary: "Connected."}}); err != nil {
				t.Fatal(err)
			}
			after, _ := store.Revisions(ctx)
			tested, _, _ := store.Get(ctx, "staging")
			if tested.RowVersion != now.RowVersion || after["staging"] == before["staging"] {
				t.Fatalf("a test moved the version to %d (was %d), or left the revision %q", tested.RowVersion, now.RowVersion, after["staging"])
			}
		}
		now.Description = "an edit read after the test"
		if err := store.Save(ctx, now, nil); err != nil {
			t.Fatalf("an edit read before the test and saved after it: %v", err)
		}
		blind := fullSpec("staging")
		if err := store.Save(ctx, blind, nil); err != nil {
			t.Fatalf("a save with no version: %v", err)
		}
	}
	t.Run("memory", func(t *testing.T) { test(t, clusters.NewMemoryStore()) })
	t.Run("database", func(t *testing.T) {
		s, _, _ := clusterstest.PG(t, nil)
		test(t, s)
	})
}

// The manager refuses an edit read at a version saved over since, and
// Held says so before anything of the edit is done (DB-92).
func TestTheManagerRefusesAnEditOfAnOldVersion(t *testing.T) {
	ctx := context.Background()
	store := clusters.NewMemoryStore()
	c := fullCred("staging")
	spec := fullSpec("staging")
	spec.Name, spec.CA, spec.CAInfo = "staging", "system", ""
	if err := store.Save(ctx, spec, &c); err != nil {
		t.Fatal(err)
	}
	m := clusters.New(store, quiet)
	if err := m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	sum, ok := m.Summary("staging")
	if !ok || sum.EditVersion != 1 {
		t.Fatalf("the summary: %v at version %d", ok, sum.EditVersion)
	}
	d := clusters.Draft{Spec: sum.Spec, EditVersion: sum.EditVersion}
	d.Description = "edited"
	edited, err := m.Update(ctx, "staging", d)
	if err != nil || edited.EditVersion != 2 {
		t.Fatalf("the edit: version %d: %v", edited.EditVersion, err)
	}
	if err := m.Held(ctx, "staging", 1); !errors.Is(err, clusters.ErrConflict) {
		t.Fatalf("Held at the old version: %v", err)
	}
	if err := m.Held(ctx, "staging", 2); err != nil {
		t.Fatalf("Held at the new version: %v", err)
	}
	d.Description = "edited again over the old version"
	if _, err := m.Update(ctx, "staging", d); !errors.Is(err, clusters.ErrConflict) {
		t.Fatalf("an edit of the old version: %v", err)
	}
	if got, _, _ := store.Get(ctx, "staging"); got.Description != "edited" {
		t.Fatalf("the refused edit was saved: %q", got.Description)
	}
}
