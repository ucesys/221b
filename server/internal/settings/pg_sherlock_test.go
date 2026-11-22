package settings_test

import (
	"context"
	"testing"

	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/settings"
)

// Sherlock turned on for a cluster stays on: another replica's service,
// and one started later, read it from the database.
func TestSherlockTurnedOnIsKeptInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	svc := settings.NewService(ctx, keyed{settings.NewPG(d)}, settings.Default, quiet, nil)
	if svc.Current().SherlockOrDefault().Clusters["local"] {
		t.Fatal("on before it was turned on")
	}
	sh := svc.Current().SherlockOrDefault()
	sh.Clusters = map[string]bool{"local": true}
	st, err := svc.UpdateSherlock(ctx, sh)
	if err != nil {
		t.Fatal(err)
	}
	if !st.SherlockOrDefault().Clusters["local"] || !svc.Current().SherlockOrDefault().Clusters["local"] {
		t.Fatalf("not on after the save: %+v", st.SherlockOrDefault())
	}
	again := settings.NewService(ctx, keyed{settings.NewPG(d)}, settings.Default, quiet, nil)
	if !again.Current().SherlockOrDefault().Clusters["local"] {
		t.Fatalf("not on for another replica: %+v", again.Current().SherlockOrDefault())
	}
	// And off again.
	sh.Clusters = map[string]bool{}
	if _, err := again.UpdateSherlock(ctx, sh); err != nil {
		t.Fatal(err)
	}
	if settings.NewService(ctx, keyed{settings.NewPG(d)}, settings.Default, quiet, nil).Current().SherlockOrDefault().Clusters["local"] {
		t.Fatal("still on after it was turned off")
	}
}
