package apps_test

import (
	"context"
	"testing"

	"github.com/ucesys/221b/server/internal/apps/appstest"
)

// An application keys to the fleet it is deployed to (DB-92): its row
// points at the fleet's row of its cluster, still does when the fleet is
// changed, and is emptied when the fleet is removed, while the record
// keeps the fleet's id as it was saved. A fleet the cluster has no row of
// is no key.
func TestAnApplicationKeysToItsFleet(t *testing.T) {
	pg, d, _ := appstest.PG(t, nil)
	ctx := context.Background()
	const fleet = "00000000-0000-7000-8000-0000000000f1"
	exec(t, d, "INSERT INTO clusters (cluster_id) VALUES ('local'), ('other') ON CONFLICT DO NOTHING")
	exec(t, d, "INSERT INTO fleets (fleet_id, cluster_id, fleet_key, name, kind) VALUES ($1, 'local', 'web-nodes', 'Web nodes', 'selector'), ('00000000-0000-7000-8000-0000000000f2', 'other', 'web-nodes', 'Web nodes', 'selector')", fleet)
	key := func(name string) string {
		t.Helper()
		var id string
		must(t, d.Pool().QueryRow(ctx, "SELECT coalesce(fleet_id::text, '') FROM applications WHERE name = $1", name).Scan(&id))
		return id
	}
	rec := shop("shop")
	rec.Fleet, rec.FleetLabel = "web-nodes", "221b.io/fleet=web"
	must(t, pg.Save(ctx, rec, nil))
	if got := key("shop"); got != fleet {
		t.Fatalf("the application points at fleet %q, want its cluster's %s", got, fleet)
	}
	exec(t, d, "UPDATE fleets SET name = 'The web nodes' WHERE fleet_id = $1", fleet)
	if got := key("shop"); got != fleet {
		t.Fatalf("after the fleet changed: %q", got)
	}
	exec(t, d, "DELETE FROM fleets WHERE fleet_id = $1", fleet)
	got, _ := read(t, pg, rec)
	if k := key("shop"); k != "" || got.Fleet != "web-nodes" {
		t.Fatalf("after the fleet was removed: key %q, the record's fleet %q", k, got.Fleet)
	}
	blog := shop("blog")
	blog.Fleet = "no-such-fleet"
	must(t, pg.Save(ctx, blog, nil))
	if k := key("blog"); k != "" {
		t.Fatalf("a fleet with no row: key %q", k)
	}
}
