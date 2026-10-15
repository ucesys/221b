package settings_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/prefs"
	"github.com/ucesys/221b/server/internal/settings"
	"github.com/ucesys/221b/server/internal/sim"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestValidation(t *testing.T) {
	for secs, ok := range map[int]bool{0: false, 1: true, 5: true, 600: true, 601: false, -5: false} {
		if err := (settings.Settings{RefreshSeconds: secs}).Validate(); (err == nil) != ok {
			t.Errorf("refreshSeconds %d: %v", secs, err)
		}
	}
	if settings.Default.Validate() != nil {
		t.Fatal("the defaults are not valid")
	}
}

func TestServiceInMemory(t *testing.T) {
	var seen []settings.State
	svc := settings.NewService(context.Background(), &settings.Memory{}, settings.Settings{RefreshSeconds: 7}, quiet, func(st settings.State) { seen = append(seen, st) })
	if len(seen) != 1 || seen[0].RefreshSeconds != 7 || seen[0].Storage != "memory" || seen[0].ChangedAt != nil {
		t.Fatalf("initial %+v", seen)
	}
	if _, err := svc.Update(context.Background(), settings.Settings{RefreshSeconds: 0}); err == nil {
		t.Fatal("an invalid update was accepted")
	}
	st, err := svc.Update(context.Background(), settings.Settings{RefreshSeconds: 30})
	if err != nil || st.RefreshSeconds != 30 || st.ChangedAt == nil {
		t.Fatalf("update %+v %v", st, err)
	}
	if len(seen) != 2 || seen[1].RefreshSeconds != 30 || svc.Current().RefreshSeconds != 30 {
		t.Fatalf("after update %+v", seen)
	}
	// A second service over the same storage starts from what was saved.
	mem := &settings.Memory{}
	_ = mem.Save(context.Background(), []byte(`{"refreshSeconds":120}`))
	again := settings.NewService(context.Background(), mem, settings.Default, quiet, nil)
	if again.Current().RefreshSeconds != 120 {
		t.Fatalf("loaded %+v", again.Current())
	}
	// Storage edited behind its back is picked up by Reload, nonsense is not.
	_ = mem.Save(context.Background(), []byte(`{"refreshSeconds":15}`))
	again.Reload(context.Background())
	if again.Current().RefreshSeconds != 15 || again.Current().ChangedAt == nil {
		t.Fatalf("reloaded %+v", again.Current())
	}
	_ = mem.Save(context.Background(), []byte(`{"refreshSeconds":99999}`))
	again.Reload(context.Background())
	_ = mem.Save(context.Background(), []byte(`not json`))
	again.Reload(context.Background())
	if again.Current().RefreshSeconds != 15 {
		t.Fatalf("bad storage changed the settings: %+v", again.Current())
	}
}

// The ConfigMap storage reads and patches one key through the API server,
// here the simulator's.
func TestConfigMapStorage(t *testing.T) {
	s := sim.New(sim.Options{Nodes: 1, Pods: 1, SettingsConfigMap: "consoles/console-221b"})
	srv := httptest.NewServer(s)
	defer srv.Close()
	ctx := context.Background()

	cm, err := kube.NewConfigMapStorage(&rest.Config{Host: srv.URL}, "consoles/console-221b")
	if err != nil {
		t.Fatal(err)
	}
	if cm.Kind() != "configmap" || cm.Ref() != "consoles/console-221b" {
		t.Fatalf("kind %q ref %q", cm.Kind(), cm.Ref())
	}
	if _, err := cm.Load(ctx); !errors.Is(err, settings.ErrNotFound) {
		t.Fatalf("empty ConfigMap: %v", err)
	}
	if err := cm.Save(ctx, []byte(`{"refreshSeconds":30}`)); err != nil {
		t.Fatal(err)
	}
	b, err := cm.Load(ctx)
	if err != nil || string(b) != `{"refreshSeconds":30}` {
		t.Fatalf("loaded %q %v", b, err)
	}

	missing, _ := kube.NewConfigMapStorage(&rest.Config{Host: srv.URL}, "consoles/absent")
	if _, err := missing.Load(ctx); !errors.Is(err, settings.ErrNotFound) {
		t.Fatalf("absent ConfigMap on load: %v", err)
	}
	if err := missing.Save(ctx, []byte(`{}`)); err == nil || err.Error() != "the settings ConfigMap consoles/absent does not exist; the chart creates it" {
		t.Fatalf("absent ConfigMap on save: %v", err)
	}

	// And through the service: saved settings load, changes are written.
	svc := settings.NewService(ctx, cm, settings.Default, quiet, nil)
	if svc.Current().RefreshSeconds != 30 || svc.Current().Storage != "configmap" {
		t.Fatalf("loaded %+v", svc.Current())
	}
	if _, err := svc.Update(ctx, settings.Settings{RefreshSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	if b, _ := cm.Load(ctx); string(b) != `{"refreshSeconds":60}` {
		t.Fatalf("ConfigMap holds %q", b)
	}

	// A person's preferences go in a key of their own, leaving the
	// settings as they were.
	people := prefs.New(cm)
	if err := people.Put(ctx, "oidc:dana", prefs.Prefs{"221b.pods.hidden": []byte(`["ip"]`)}); err != nil {
		t.Fatal(err)
	}
	if b, _ := cm.Load(ctx); string(b) != `{"refreshSeconds":60}` {
		t.Fatalf("preferences changed the settings: %q", b)
	}
	if b, err := cm.LoadKey(ctx, prefs.KeyFor("oidc:dana")); err != nil || string(b) != `{"221b.pods.hidden":["ip"]}` {
		t.Fatalf("preferences %q %v", b, err)
	}
	if p, err := prefs.New(cm).Get(ctx, "oidc:dana"); err != nil || string(p["221b.pods.hidden"]) != `["ip"]` {
		t.Fatalf("read back %v %v", p, err)
	}

	// Without the API server, saving fails with a message, not a hang.
	srv.Close()
	down := settings.NewService(ctx, cm, settings.Default, quiet, nil)
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := down.Update(tctx, settings.Settings{RefreshSeconds: 10}); err == nil {
		t.Fatal("saving without the API server worked")
	} else if down.Current().RefreshSeconds != settings.Default.RefreshSeconds {
		t.Fatalf("a failed save changed the settings: %+v", down.Current())
	}
}
