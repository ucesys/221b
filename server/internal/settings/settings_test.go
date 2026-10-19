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

func TestTimeFormat(t *testing.T) {
	for _, c := range []struct {
		f  settings.TimeFormat
		ok bool
	}{
		{settings.TimeFormat{Date: "iso", Clock: "12h"}, true},
		{settings.TimeFormat{Date: "long", Clock: "24h"}, true},
		{settings.TimeFormat{Date: "us", Clock: "12"}, false},
		{settings.TimeFormat{Date: "", Clock: "12h"}, false},
	} {
		f := c.f
		if err := (settings.Settings{RefreshSeconds: 3, TimeFormat: &f}).Validate(); (err == nil) != c.ok {
			t.Errorf("%+v: %v", c.f, err)
		}
	}
	// An update that leaves the format out keeps the one in force.
	svc := settings.NewService(context.Background(), &settings.Memory{}, settings.Default, quiet, nil)
	if _, err := svc.Update(context.Background(), settings.Settings{RefreshSeconds: 3, TimeFormat: &settings.TimeFormat{Date: "eu", Clock: "24h"}}); err != nil {
		t.Fatal(err)
	}
	st, err := svc.Update(context.Background(), settings.Settings{RefreshSeconds: 5})
	if err != nil || st.TimeFormat == nil || st.TimeFormat.Date != "eu" || st.TimeFormat.Clock != "24h" {
		t.Fatalf("after a refresh change %+v %v", st.TimeFormat, err)
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

// heldLoad is storage whose next Load, once armed, reads and then waits.
type heldLoad struct {
	settings.Memory
	armed   chan struct{} // closed by the Load it holds, once it has read
	release chan struct{}
}

func (h *heldLoad) Load(ctx context.Context) ([]byte, error) {
	b, err := h.Memory.Load(ctx)
	if h.armed != nil {
		armed := h.armed
		h.armed = nil
		close(armed)
		<-h.release
	}
	return b, err
}

// A reload that read storage before a change was saved does not put the
// old settings back after it.
func TestReloadDoesNotUndoAChangeSavedWhileItRead(t *testing.T) {
	h := &heldLoad{}
	_ = h.Save(context.Background(), []byte(`{"refreshSeconds":15}`))
	svc := settings.NewService(context.Background(), h, settings.Default, quiet, nil)
	h.armed, h.release = make(chan struct{}), make(chan struct{})
	armed := h.armed
	reloaded := make(chan struct{})
	go func() {
		svc.Reload(context.Background())
		close(reloaded)
	}()
	<-armed
	updated := make(chan error)
	go func() {
		_, err := svc.Update(context.Background(), settings.Settings{RefreshSeconds: 30})
		updated <- err
	}()
	time.Sleep(50 * time.Millisecond)
	close(h.release)
	<-reloaded
	if err := <-updated; err != nil {
		t.Fatal(err)
	}
	if got := svc.Current().RefreshSeconds; got != 30 {
		t.Fatalf("the change was undone: refresh %d", got)
	}
}

// The ConfigMap storage reads and patches one key through the API server,
// here the simulator's.
func TestConfigMapStorage(t *testing.T) {
	s := sim.New(sim.Options{Nodes: 1, Pods: 1, SettingsConfigMap: "consoles/k8s-console-221b"})
	srv := httptest.NewServer(s)
	defer srv.Close()
	ctx := context.Background()

	cm, err := kube.NewConfigMapStorage(&rest.Config{Host: srv.URL}, "consoles/k8s-console-221b")
	if err != nil {
		t.Fatal(err)
	}
	if cm.Kind() != "configmap" || cm.Ref() != "consoles/k8s-console-221b" {
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

	// Another key (Sherlock's spend) goes in a key of its own, leaving the
	// settings as they were.
	if err := cm.SaveKey(ctx, "sherlock-spend.json", []byte(`{"day":"2026-10-15"}`)); err != nil {
		t.Fatal(err)
	}
	if b, _ := cm.Load(ctx); string(b) != `{"refreshSeconds":60}` {
		t.Fatalf("another key changed the settings: %q", b)
	}
	if b, err := cm.LoadKey(ctx, "sherlock-spend.json"); err != nil || string(b) != `{"day":"2026-10-15"}` {
		t.Fatalf("read back %q %v", b, err)
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
