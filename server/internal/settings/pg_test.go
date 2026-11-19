package settings_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/acks"
	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/fleets"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/settings"
)

// keyed is a kv.Backend's key as the service's storage, as kube.KeyStorage
// is.
type keyed struct{ b kv.Backend }

func (k keyed) Kind() string { return "configmap" }
func (k keyed) Load(ctx context.Context) ([]byte, string, error) {
	b, v, err := k.b.LoadKey(ctx, settings.SettingsKey)
	if errors.Is(err, kv.ErrNotFound) {
		return nil, v, settings.ErrNotFound
	}
	return b, v, err
}
func (k keyed) Save(ctx context.Context, b []byte, version string) (string, error) {
	next, err := k.b.SaveKey(ctx, settings.SettingsKey, b, version)
	if errors.Is(err, kv.ErrConflict) {
		return "", settings.ErrConflict
	}
	return next, err
}

// The service works over the database as over a ConfigMap; each part is
// kept as what it is; and another replica's service reads the same.
func TestSettingsInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	svc := settings.NewService(ctx, keyed{settings.NewPG(d)}, settings.Settings{RefreshSeconds: 7}, quiet, nil)
	if got := svc.Current().RefreshSeconds; got != 7 {
		t.Fatalf("nothing saved yet: refresh %d", got)
	}
	if _, err := svc.Update(ctx, settings.Settings{RefreshSeconds: 5, TimeFormat: &settings.TimeFormat{Date: "eu", Clock: "24h"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateWatson(ctx, settings.Watson{TwoPerson: true, ApproveAllMinutes: 15}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	ack := acks.Ack{Key: "pod/shop/web-1", Fingerprint: "critical|CrashLoopBackOff", By: "Ann", At: at, Run: "run-12", Case: "C-7", Expected: true, Why: "a known deploy"}
	other := acks.Ack{Key: "node/n1", Fingerprint: "warning|DiskPressure", By: "Bob", At: at.Add(time.Minute)}
	if _, err := svc.UpdateAcks(ctx, "c1", func(l []acks.Ack) []acks.Ack { return append(l, ack, other) }); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateAcks(ctx, "c2", func(l []acks.Ack) []acks.Ack { return append(l, other) }); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateFleets(ctx, "c1", []fleets.Fleet{
		{ID: "gpu", Name: "GPU nodes", Color: 3, Desc: "the training pool", Labels: []string{"gpu=true"}, Taints: []string{"nvidia.com/gpu:NoSchedule"}, Deploy: "fleet=gpu"},
		{ID: "backup", Name: "Backup hosts", Labels: []string{}, Taints: []string{}, Kind: fleets.KindNodes, Nodes: []string{"backup-2", "backup-1"}},
	}); err != nil {
		t.Fatal(err)
	}
	// One acknowledgement lapses.
	if _, err := svc.UpdateAcks(ctx, "c1", func(l []acks.Ack) []acks.Ack { return l[:1] }); err != nil {
		t.Fatal(err)
	}
	want := svc.Current().Settings
	if want.RefreshSeconds != 5 || want.Watson == nil || !want.Watson.TwoPerson || len(want.Acks["c1"]) != 1 || len(want.Acks["c2"]) != 1 || len(want.Fleets["c1"]) != 2 {
		t.Fatalf("the service holds %+v", want)
	}
	again := settings.NewService(ctx, keyed{settings.NewPG(d)}, settings.Default, quiet, nil)
	if got := again.Current().Settings; !reflect.DeepEqual(got, want) {
		t.Fatalf("another replica reads\n%+v\nwant\n%+v", got, want)
	}
	// Each part where it belongs.
	var scopes []string
	var n int
	err := d.Do(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, "SELECT scope FROM settings ORDER BY scope")
		if err != nil {
			return err
		}
		var s string
		if _, err := pgxForEach(rows, &s, func() { scopes = append(scopes, s) }); err != nil {
			return err
		}
		return q.QueryRow(ctx, "SELECT count(*) FROM alert_acks a JOIN clusters c USING (cluster_id) WHERE a.is_expected AND a.job_run_id = 'run-12' AND a.case_id = 'C-7'").Scan(&n)
	})
	if err != nil || !reflect.DeepEqual(scopes, []string{"general", "watson"}) || n != 1 {
		t.Fatalf("scopes %v, %d acknowledgements with their run and case: %v", scopes, n, err)
	}
	// A listed fleet's members are hosts' rows, in the fleet's order; one
	// taken off the list leaves, and its row with it.
	members := func() (out []string) {
		_ = d.Do(ctx, func(q db.Querier) error {
			return q.QueryRow(ctx, `SELECT coalesce(array_agg(h.name ORDER BY m.position), '{}') FROM fleets f
				JOIN fleet_hosts m USING (fleet_id) JOIN hosts h USING (host_id) WHERE f.fleet_key = 'backup' AND f.kind = 'list'`).Scan(&out)
		})
		return out
	}
	if got := members(); !reflect.DeepEqual(got, []string{"backup-2", "backup-1"}) {
		t.Fatalf("the listed fleet's members are %v", got)
	}
	list := again.Current().Settings.Fleets["c1"]
	list[1].Nodes = []string{"backup-1"}
	if _, err := again.UpdateFleets(ctx, "c1", list[1:]); err != nil {
		t.Fatal(err)
	}
	var hosts int
	_ = d.Do(ctx, func(q db.Querier) error { return q.QueryRow(ctx, "SELECT count(*) FROM hosts").Scan(&hosts) })
	if got := members(); !reflect.DeepEqual(got, []string{"backup-1"}) || hosts != 1 {
		t.Fatalf("after taking one off: members %v, %d hosts' rows", got, hosts)
	}
	if kept := settings.NewService(ctx, keyed{settings.NewPG(d)}, settings.Default, quiet, nil).Current().Settings.Fleets["c1"]; len(kept) != 1 || kept[0].ID != "backup" {
		t.Fatalf("the fleets kept: %+v", kept)
	}
}
