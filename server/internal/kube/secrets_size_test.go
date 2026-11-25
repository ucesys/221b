package kube

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// gzipped is a release record as Helm stores it, from its JSON text.
func gzipped(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	return []byte(base64.StdEncoding.EncodeToString(buf.Bytes()))
}

// keptBytes is about what the cache keeps of a release.
func keptBytes(r *HelmRelease) int {
	n := len(r.Notes) + len(r.Values)
	for _, m := range r.Masked {
		n += len(m)
	}
	for _, x := range r.Resources {
		n += len(x.Kind) + len(x.Name) + len(x.Namespace)
	}
	for _, i := range r.Images {
		n += len(i)
	}
	return n
}

// A small Secret must not make the cache keep, or the transform spend,
// far more than it holds (kube#3): a record that inflates past the limit
// is refused, and what is kept of one is bounded.
func TestASmallReleaseRecordKeepsLittle(t *testing.T) {
	// About 40 MiB of notes, which gzip makes about 40 KB.
	huge := []byte(`{"name":"big","info":{"notes":"` + strings.Repeat("a", 40<<20) + `"}}`)
	if r, err := DecodeHelmRelease(gzipped(t, huge)); err == nil {
		t.Fatalf("a record of %d MiB was read: %d bytes kept", len(huge)>>20, keptBytes(r))
	}

	// Long notes under the limit are not kept whole.
	var rec HelmRecord
	rec.Name, rec.Version = "notes", 1
	rec.Info.Notes = strings.Repeat("Read the docs. ", 1<<20/15)
	r, err := DecodeHelmRelease(EncodeHelmRelease(rec))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Notes) > maxNotes {
		t.Errorf("notes of %d bytes kept", len(r.Notes))
	}

	// Masking cannot make the notes grow past the limit.
	rec = HelmRecord{Name: "grow", Version: 1, Config: map[string]any{"password": "fake"}}
	rec.Info.Notes = strings.Repeat("fake", 60<<10/4)
	if r, err = DecodeHelmRelease(EncodeHelmRelease(rec)); err != nil {
		t.Fatal(err)
	}
	if len(r.Notes) > maxNotes || strings.Contains(r.Notes, "fake") {
		t.Errorf("masked notes of %d bytes kept", len(r.Notes))
	}

	// A million short values neither stall the transform nor leave the
	// notes, the values or the list of what was masked whole.
	config := map[string]any{}
	values := make([]any, 0, 1<<20)
	for i := range 1 << 20 {
		values = append(values, fmt.Sprintf("%x", i%4096))
	}
	config["passwords"] = values
	b, _ := json.Marshal(map[string]any{"name": "many", "info": map[string]any{"notes": "see " + strings.Repeat("x ", 1000)}, "config": config})
	start := time.Now()
	if r, err = DecodeHelmRelease(gzipped(t, b)); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("a record of a million values took %v", d)
	}
	if n := keptBytes(r); n > 1<<20 {
		t.Errorf("a record of a million values keeps %d bytes", n)
	}
	if len(r.Masked) > maxMasked+1 {
		t.Errorf("%d masked paths kept", len(r.Masked))
	}
	if len(r.Values) > maxValues {
		t.Errorf("values of %d bytes kept", len(r.Values))
	}

	// Many objects in the manifest: the list of resources is bounded too.
	var m strings.Builder
	for i := range 100000 {
		fmt.Fprintf(&m, "---\nkind: ConfigMap\nmetadata:\n  name: c%d\n", i)
	}
	rec = HelmRecord{Name: "objects", Version: 1, Manifest: m.String()}
	if r, err = DecodeHelmRelease(EncodeHelmRelease(rec)); err != nil {
		t.Fatal(err)
	}
	if len(r.Resources) > maxResources {
		t.Errorf("%d resources kept", len(r.Resources))
	}
}
