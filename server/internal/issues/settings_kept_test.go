package issues

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ucesys/221b/server/internal/kv"
)

// What was saved before 0.12.1 (the report page's address and the status
// page) is kept as stored when the settings are saved, never read nor
// shown.
func TestSavingTheSettingsKeepsWhatWasSavedBefore(t *testing.T) {
	mem := &kv.Memory{}
	if _, err := mem.SaveKey(ctx, ConfigKey, []byte(`{"settings":{"company":"Example IT","statusPage":"https://wiki.example.com/known","reportURL":"https://old.example.com/report","matchThreshold":80}}`), ""); err != nil {
		t.Fatal(err)
	}
	s := New(Options{Backend: mem})
	set, err := s.Settings(ctx)
	if err != nil || set.ReportURL != "" || set.StatusPage != "" {
		t.Fatalf("shown: %+v %v", set, err)
	}
	set.MatchThreshold = 70
	out, err := s.SaveSettings(ctx, admin, set)
	if err != nil || out.ReportURL != "" || out.MatchThreshold != 70 {
		t.Fatalf("saved: %+v %v", out, err)
	}
	if b, _ := json.Marshal(out); strings.Contains(string(b), "old.example.com") {
		t.Fatalf("answered: %s", b)
	}
	raw, _, err := mem.LoadKey(ctx, ConfigKey)
	if err != nil {
		t.Fatal(err)
	}
	var doc configDoc
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Settings == nil || doc.Settings.ReportURL != "https://old.example.com/report" || doc.Settings.StatusPage != "https://wiki.example.com/known" || doc.Settings.MatchThreshold != 70 {
		t.Fatalf("stored: %s %v", raw, err)
	}
	if s.ReportURL() != "/report" {
		t.Fatalf("read: %s", s.ReportURL())
	}
	// A save that names them changes nothing of them.
	set.ReportURL, set.StatusPage = "https://new.example.com", "x"
	if _, err := s.SaveSettings(ctx, admin, set); err != nil {
		t.Fatal(err)
	}
	raw, _, _ = mem.LoadKey(ctx, ConfigKey)
	if strings.Contains(string(raw), "new.example.com") || !strings.Contains(string(raw), "old.example.com") {
		t.Fatalf("stored: %s", raw)
	}
}
