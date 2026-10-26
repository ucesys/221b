package settings_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/acks"
	"github.com/ucesys/221b/server/internal/fleets"
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
	_, _ = mem.Save(context.Background(), []byte(`{"refreshSeconds":120}`), "")
	again := settings.NewService(context.Background(), mem, settings.Default, quiet, nil)
	if again.Current().RefreshSeconds != 120 {
		t.Fatalf("loaded %+v", again.Current())
	}
	// Storage edited behind its back is picked up by Reload, nonsense is not.
	_, _ = mem.Save(context.Background(), []byte(`{"refreshSeconds":15}`), "")
	again.Reload(context.Background())
	if again.Current().RefreshSeconds != 15 || again.Current().ChangedAt == nil {
		t.Fatalf("reloaded %+v", again.Current())
	}
	_, _ = mem.Save(context.Background(), []byte(`{"refreshSeconds":99999}`), "")
	again.Reload(context.Background())
	_, _ = mem.Save(context.Background(), []byte(`not json`), "")
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

func (h *heldLoad) Load(ctx context.Context) ([]byte, string, error) {
	b, v, err := h.Memory.Load(ctx)
	if h.armed != nil {
		armed := h.armed
		h.armed = nil
		close(armed)
		<-h.release
	}
	return b, v, err
}

// A reload that read storage before a change was saved does not put the
// old settings back after it.
func TestReloadDoesNotUndoAChangeSavedWhileItRead(t *testing.T) {
	h := &heldLoad{}
	_, _ = h.Save(context.Background(), []byte(`{"refreshSeconds":15}`), "")
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
	_, v0, err := cm.Load(ctx)
	if !errors.Is(err, settings.ErrNotFound) || v0 == "" {
		t.Fatalf("empty ConfigMap: version %q %v", v0, err)
	}
	if _, err := cm.Save(ctx, []byte(`{"refreshSeconds":30}`), v0); err != nil {
		t.Fatal(err)
	}
	b, v1, err := cm.Load(ctx)
	if err != nil || string(b) != `{"refreshSeconds":30}` || v1 == v0 {
		t.Fatalf("loaded %q at %q %v", b, v1, err)
	}
	// A save based on what was read before another one is refused (P7-02).
	if _, err := cm.Save(ctx, []byte(`{"refreshSeconds":40}`), v0); !errors.Is(err, settings.ErrConflict) {
		t.Fatalf("a stale save: %v", err)
	}
	if b, _, _ := cm.Load(ctx); string(b) != `{"refreshSeconds":30}` {
		t.Fatalf("a stale save changed the ConfigMap: %q", b)
	}

	missing, _ := kube.NewConfigMapStorage(&rest.Config{Host: srv.URL}, "consoles/absent")
	if _, _, err := missing.Load(ctx); !errors.Is(err, settings.ErrNotFound) {
		t.Fatalf("absent ConfigMap on load: %v", err)
	}
	if _, err := missing.Save(ctx, []byte(`{}`), ""); err == nil || err.Error() != "the settings ConfigMap consoles/absent does not exist; the chart creates it" {
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
	if b, _, _ := cm.Load(ctx); string(b) != `{"version":1,"refreshSeconds":60}` {
		t.Fatalf("ConfigMap holds %q", b)
	}

	// Another key (Sherlock's spend) goes in a key of its own, leaving the
	// settings as they were.
	if _, err := cm.SaveKey(ctx, "sherlock-spend.json", []byte(`{"day":"2026-10-15"}`), ""); err != nil {
		t.Fatal(err)
	}
	if b, _, _ := cm.Load(ctx); string(b) != `{"version":1,"refreshSeconds":60}` {
		t.Fatalf("another key changed the settings: %q", b)
	}
	if b, _, err := cm.LoadKey(ctx, "sherlock-spend.json"); err != nil || string(b) != `{"day":"2026-10-15"}` {
		t.Fatalf("read back %q %v", b, err)
	}
	// The service, which read before that save, still saves: it reads again.
	if _, err := svc.Update(ctx, settings.Settings{RefreshSeconds: 45}); err != nil {
		t.Fatal(err)
	}
	if b, _, _ := cm.LoadKey(ctx, "sherlock-spend.json"); string(b) != `{"day":"2026-10-15"}` {
		t.Fatalf("the settings changed another key: %q", b)
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

// Two replicas over one ConfigMap: each change is made to what is saved,
// so neither puts back what the other changed, even before it reloads
// (P7-02).
func TestReplicasKeepEachOthersChanges(t *testing.T) {
	ctx := context.Background()
	mem := &settings.Memory{}
	a := settings.NewService(ctx, mem, settings.Default, quiet, nil)
	b := settings.NewService(ctx, mem, settings.Default, quiet, nil)
	if _, err := a.UpdateFleets(ctx, "local", []fleets.Fleet{{ID: "gpu", Name: "GPU", Color: 1, Labels: []string{"pool=gpu"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.UpdateWatson(ctx, settings.Watson{TwoPerson: true}); err != nil {
		t.Fatal(err)
	}
	// b has not reloaded: it has never seen the fleet or the rule.
	if _, err := b.UpdateAcks(ctx, "local", func(l []acks.Ack) []acks.Ack {
		return acks.Set(l, acks.Ack{Key: "pod:web/a", Fingerprint: "f", By: "ann", At: time.Now().UTC()})
	}); err != nil {
		t.Fatal(err)
	}
	st, err := b.Update(ctx, settings.Settings{RefreshSeconds: 9})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Fleets["local"]) != 1 || st.Watson == nil || !st.Watson.TwoPerson || len(st.Acks["local"]) != 1 || st.RefreshSeconds != 9 {
		t.Fatalf("b lost a's changes: %+v", st.Settings)
	}
	// a picks up b's changes on its next reload.
	a.Reload(ctx)
	if got := a.Current(); got.RefreshSeconds != 9 || len(got.Acks["local"]) != 1 || got.ChangedAt == nil {
		t.Fatalf("a after reload %+v", got.Settings)
	}
}

// racing is storage where another replica saves between a change's read
// and its save, once.
type racing struct {
	settings.Memory
	before func()
}

func (r *racing) Save(ctx context.Context, b []byte, version string) (string, error) {
	if f := r.before; f != nil {
		r.before = nil
		f()
	}
	return r.Memory.Save(ctx, b, version)
}

func TestAChangeThatMeetsAnotherIsAppliedToIt(t *testing.T) {
	ctx := context.Background()
	r := &racing{}
	svc := settings.NewService(ctx, r, settings.Default, quiet, nil)
	other := settings.NewService(ctx, &r.Memory, settings.Default, quiet, nil)
	r.before = func() {
		if _, err := other.UpdateWatson(ctx, settings.Watson{TwoPerson: true}); err != nil {
			t.Error(err)
		}
	}
	st, err := svc.Update(ctx, settings.Settings{RefreshSeconds: 20})
	if err != nil {
		t.Fatal(err)
	}
	if st.RefreshSeconds != 20 || st.Watson == nil || !st.Watson.TwoPerson {
		t.Fatalf("the other save was lost: %+v", st.Settings)
	}
}

// Formats (P7-03): a document from before formats were numbered loads and
// is saved in the current one; fields this console does not know are
// written back as they were.
func TestFormatMigratesAndKeepsUnknownFields(t *testing.T) {
	ctx := context.Background()
	mem := &settings.Memory{}
	_, _ = mem.Save(ctx, []byte(`{"refreshSeconds":15,"watson":{"twoPerson":true},"dashboards":{"home":["nodes"]}}`), "")
	svc := settings.NewService(ctx, mem, settings.Default, quiet, nil)
	if got := svc.Current(); got.RefreshSeconds != 15 || got.Watson == nil || !got.Watson.TwoPerson {
		t.Fatalf("loaded %+v", got.Settings)
	}
	if _, err := svc.Update(ctx, settings.Settings{RefreshSeconds: 30}); err != nil {
		t.Fatal(err)
	}
	b, _, _ := mem.Load(ctx)
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if string(doc["version"]) != "1" || string(doc["refreshSeconds"]) != "30" || string(doc["dashboards"]) != `{"home":["nodes"]}` || string(doc["watson"]) != `{"twoPerson":true}` {
		t.Fatalf("saved %s", b)
	}
	if settings.FormatVersion != 1 {
		t.Fatalf("format %d: add a migration test for the new format", settings.FormatVersion)
	}
}

// A newer 221B's document (after a rollback) is read for what this one
// knows and never saved over.
func TestNewerFormatIsReadButNotSaved(t *testing.T) {
	ctx := context.Background()
	mem := &settings.Memory{}
	newer := `{"version":7,"refreshSeconds":20,"dashboards":{}}`
	_, _ = mem.Save(ctx, []byte(newer), "")
	svc := settings.NewService(ctx, mem, settings.Default, quiet, nil)
	if svc.Current().RefreshSeconds != 20 {
		t.Fatalf("loaded %+v", svc.Current().Settings)
	}
	if _, err := svc.Update(ctx, settings.Settings{RefreshSeconds: 30}); !errors.Is(err, settings.ErrNewer) {
		t.Fatalf("saving over a newer format: %v", err)
	}
	if _, err := svc.UpdateAcks(ctx, "local", func(l []acks.Ack) []acks.Ack { return l }); !errors.Is(err, settings.ErrNewer) {
		t.Fatalf("acknowledging over a newer format: %v", err)
	}
	if b, _, _ := mem.Load(ctx); string(b) != newer {
		t.Fatalf("the newer document changed: %s", b)
	}
}

// fixedVersion is storage whose version does not change when its bytes
// do, to show a reload with the same version reads nothing more.
type fixedVersion struct {
	b []byte
}

func (f *fixedVersion) Load(context.Context) ([]byte, string, error) { return f.b, "7", nil }
func (f *fixedVersion) Save(_ context.Context, b []byte, _ string) (string, error) {
	f.b = b
	return "7", nil
}
func (f *fixedVersion) Kind() string { return "configmap" }

func TestReloadSkipsAnUnchangedVersion(t *testing.T) {
	ctx := context.Background()
	f := &fixedVersion{b: []byte(`{"refreshSeconds":15}`)}
	svc := settings.NewService(ctx, f, settings.Default, quiet, nil)
	f.b = []byte(`{"refreshSeconds":25}`)
	svc.Reload(ctx)
	if svc.Current().RefreshSeconds != 15 {
		t.Fatalf("a reload at the same version decoded the settings again: %+v", svc.Current().Settings)
	}
}

func TestPendingGrace(t *testing.T) {
	if g := settings.Default.PendingGrace(); g != 2*time.Minute {
		t.Errorf("default grace %s, want 2m", g)
	}
	for _, n := range []int{-1, 3601} {
		s := settings.Settings{RefreshSeconds: 3, PendingGraceSeconds: &n}
		if s.Validate() == nil {
			t.Errorf("a grace of %d seconds was accepted", n)
		}
	}
	svc := settings.NewService(context.Background(), &settings.Memory{}, settings.Default, quiet, nil)
	zero := 0
	if _, err := svc.Update(context.Background(), settings.Settings{RefreshSeconds: 3, PendingGraceSeconds: &zero}); err != nil {
		t.Fatal(err)
	}
	// A save that does not send it keeps it.
	st, err := svc.Update(context.Background(), settings.Settings{RefreshSeconds: 5})
	if err != nil || st.PendingGrace() != 0 {
		t.Errorf("after a refresh change the grace is %s (%v), want 0", st.PendingGrace(), err)
	}
}
