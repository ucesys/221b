package knowledge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// What a passage is about: its document's front matter, a version in its
// path where its source names a product or follows the clusters, its
// source's settings; and a source's trust by its kind (KN-56, KN-57).
func TestTagsAndTrust(t *testing.T) {
	for v, want := range map[string]string{"v1.33.5+rke2r1": "1.33", "1.31": "1.31", "v1.30.2": "1.30", "latest": "", "": ""} {
		if got := versionMinor(v); got != want {
			t.Errorf("versionMinor(%q) = %q, want %q", v, got, want)
		}
	}
	for p, want := range map[string]string{"docs/v1.33/install.md": "1.33", "https://docs.rke2.io/1.31/upgrade": "1.31", "release-1.32.x/notes": "1.32", "docs/install.md": "", "notes-2026.11.md": ""} {
		if got := pathVersion(p); got != want {
			t.Errorf("pathVersion(%q) = %q, want %q", p, got, want)
		}
	}
	if p, v := frontTags("---\ntitle: Etcd\nproduct: RKE2\nversion: \"v1.33\"\n---\n# Etcd"); p != "rke2" || v != "1.33" {
		t.Errorf("front matter: %q %q", p, v)
	}
	if p, v := frontTags("# No front matter\n\nproduct: rke2"); p != "" || v != "" {
		t.Errorf("no front matter: %q %q", p, v)
	}
	web := Source{ID: "docs", Kind: SourceWeb, Product: "rke2", ProductVersion: "1.30"}
	if p, v := tagsOf(Excerpt{Key: "docs:https://docs.rke2.io/v1.32/etcd#backups", URL: "https://docs.rke2.io/v1.32/etcd"}, web); p != "rke2" || v != "1.32" {
		t.Errorf("by path: %q %q", p, v)
	}
	if p, v := tagsOf(Excerpt{Key: "docs:about", Product: "longhorn", ProductVersion: "1.7"}, web); p != "longhorn" || v != "1.7" {
		t.Errorf("by front matter: %q %q", p, v)
	}
	if p, v := tagsOf(Excerpt{Key: "docs:about"}, web); p != "rke2" || v != "1.30" {
		t.Errorf("by the source: %q %q", p, v)
	}
	if _, v := tagsOf(Excerpt{Key: "notes:docs/v1.33/x.md"}, Source{Kind: SourceUpload}); v != "" {
		t.Errorf("a source that names nothing takes no version from a path: %q", v)
	}
	for kind, want := range map[string]string{SourceGit: TrustHigh, SourceUpload: TrustHigh, SourceConsole: TrustHigh, SourceFixes: TrustHigh, SourceConfluence: TrustHigh, SourceWeb: TrustNormal, SourceS3: TrustNormal} {
		if got := (Source{Kind: kind}).trustLevel(); got != want {
			t.Errorf("%s's default trust: %s, want %s", kind, got, want)
		}
	}
	if got := (Source{Kind: SourceGit, Trust: TrustLow}).trustLevel(); got != TrustLow {
		t.Errorf("a source's own trust: %s", got)
	}
}

// A source's trust weighs its passages, a source that follows the
// clusters keeps to their versions, a SuperAdmin sets both, audited; the
// index says its size per source and warns at 80 % of the volume (KN-56
// to KN-58).
func TestTrustVersionsAndSize(t *testing.T) {
	d := dbtest.Open(t)
	store := NewMemoryStore()
	s := pgReplica(t, d, store, true)
	var audits []audit.Event
	s.o.Audit = func(e audit.Event) { audits = append(audits, e) }
	s.o.ClusterVersions = func(context.Context) []string { return []string{"v1.33.5+rke2r1", "v1.33.2+rke2r1"} }
	s.o.VolumeBytes = 1 << 40
	if _, err := s.editSettings(ctx, func(set *Settings) error {
		set.Sources = append(set.Sources,
			Source{ID: "runbooks", Name: "Runbooks", Kind: SourceGit, URL: "https://git.example.com/ops/runbooks", Branch: "main"},
			Source{ID: "rke2", Name: "RKE2 docs", Kind: SourceWeb, URL: "https://docs.rke2.io/"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	text := "To restore an etcd snapshot, stop the server on every node and run the cluster reset with the snapshot's path."
	exc := func(key, title, url string) Change {
		c := excerptFile(key, title, text)
		c.Body = []byte(strings.Replace(string(c.Body), `"chunks"`, `"url":"`+url+`","chunks"`, 1))
		return c
	}
	if err := store.Write(ctx, []Change{
		exc("rke2:https://docs.rke2.io/v1.31/backup_restore", "Etcd backup and restore (1.31)", "https://docs.rke2.io/v1.31/backup_restore"),
		exc("rke2:https://docs.rke2.io/v1.33/backup_restore", "Etcd backup and restore (1.33)", "https://docs.rke2.io/v1.33/backup_restore"),
		exc("runbooks:etcd/restore.md", "Restore etcd", ""),
	}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	titles := func() string {
		t.Helper()
		hits, err := s.Ask(ctx, "restore an etcd snapshot", 6)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, h := range hits {
			out = append(out, h.Title+" ["+h.Trust+"]")
		}
		return strings.Join(out, ", ")
	}
	// Equal text: the runbook's source is trusted high by its kind.
	if got := titles(); !strings.HasPrefix(got, "Restore etcd [high]") || !strings.Contains(got, "(1.31)") {
		t.Fatalf("by trust: %s", got)
	}
	if _, err := s.SetSourceIndex(ctx, bob, "rke2", SourceIndex{Trust: TrustHigh}); !errors.Is(err, ErrDenied) {
		t.Fatalf("an Admin sets a source's trust: %v", err)
	}
	if _, err := s.SetSourceIndex(ctx, admin, "rke2", SourceIndex{Trust: "max"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a trust level of none: %v", err)
	}
	// Low, about RKE2, kept to the clusters' versions.
	if _, err := s.SetSourceIndex(ctx, admin, "rke2", SourceIndex{Trust: TrustLow, Product: "RKE2", FollowsClusters: true}); err != nil {
		t.Fatal(err)
	}
	if e := audits[len(audits)-1]; e.Action != ActionIndexSettings || e.Target != "rke2" || e.Detail != "RKE2 docs: trust low, was normal (its kind's); about rke2, was nothing named; kept to the versions the clusters run" {
		t.Fatalf("audited: %+v", e)
	}
	s.Wait()
	if _, err := s.SyncIndex(ctx); err != nil {
		t.Fatal(err)
	}
	if got := titles(); got != "Restore etcd [high], Etcd backup and restore (1.33) [low]" {
		t.Fatalf("kept to the clusters' versions: %s", got)
	}
	// A source's dialog keeps its index settings.
	set, _ := s.Settings(ctx)
	x, _ := set.source("rke2")
	if x.Trust != TrustLow || x.Product != "rke2" || !x.FollowsClusters {
		t.Fatalf("kept: %+v", x)
	}
	// Its size, and the volume's.
	v, err := s.GetView(ctx, admin)
	if err != nil || v.Index == nil {
		t.Fatal(err)
	}
	var rke2, runbooks *SourceIndexView
	for i := range v.Index.Sources {
		switch v.Index.Sources[i].ID {
		case "rke2":
			rke2 = &v.Index.Sources[i]
		case "runbooks":
			runbooks = &v.Index.Sources[i]
		}
	}
	if rke2 == nil || rke2.Documents != 2 || rke2.Passages != 2 || rke2.Bytes <= 0 || rke2.Trust != TrustLow || rke2.TrustDefault || !rke2.FollowsClusters {
		t.Fatalf("rke2: %+v", rke2)
	}
	if runbooks == nil || runbooks.Documents != 1 || runbooks.Trust != TrustHigh || !runbooks.TrustDefault {
		t.Fatalf("runbooks: %+v", runbooks)
	}
	if v.Index.Full || v.Index.DatabaseBytes <= 0 || strings.Join(v.Index.ClusterVersions, ",") != "1.33" {
		t.Fatalf("the volume: %+v", v.Index)
	}
	s.o.VolumeBytes = v.Index.DatabaseBytes
	if v, _ = s.GetView(ctx, admin); !v.Index.Full {
		t.Fatal("no warning at the volume's size")
	}
}

// A document's front matter tags its excerpts (KN-56).
func TestFrontMatterTagsADocument(t *testing.T) {
	d := dbtest.Open(t)
	s := pgReplica(t, d, NewMemoryStore(), true)
	start := s.o.Now()
	doc := "---\nproduct: longhorn\nversion: 1.7.2\n---\n# Volumes\n\nA degraded volume rebuilds its replica on another node."
	if _, err := s.Upload(ctx, admin, "documents", "longhorn.md", []byte(doc)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		hits := s.Search("degraded volume replica", "excerpt", 3)
		if len(hits) == 1 {
			if hits[0].Product != "longhorn" || hits[0].ProductVersion != "1.7" {
				t.Fatalf("its tags: %+v", hits[0])
			}
			break
		}
		if time.Now().After(deadline) {
			st, _ := s.Status(ctx)
			t.Fatalf("not indexed since %v: %+v", start, st.Last)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
