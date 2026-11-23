package knowledge

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/db"
)

// Metadata and weighting (phase-13.1-ai, part B; KN-56 to KN-58). Each
// passage is tagged with its source, and with the product and version its
// document is about: the document's front matter (product:, version:),
// else a version in its path or address (docs/v1.33/…) where its source
// names a product or follows the clusters, else its source's settings.
// A source that follows the clusters' versions keeps to the passages of a
// version a cluster runs (and those of no version). A source's trust
// level, high, normal or low, set per source by SuperAdmins and by
// default by its kind, weights its passages' rank; an approved page
// weighs more than any excerpt, which ask_knowledge marks unreviewed.

// The trust levels of a source (KN-57).
const (
	TrustHigh   = "high"
	TrustNormal = "normal"
	TrustLow    = "low"
)

// ActionIndexSettings is a change of a source's trust level, product,
// version or following of the clusters' versions (KN-56, KN-57).
const ActionIndexSettings = "knowledge.trust"

// trustLevel is a source's trust level: its own, or its kind's: the
// organisation's own (Confluence spaces, git repositories of runbooks,
// uploaded documents and notes, the console's records, the fixes) high;
// web sites and buckets, where vendor documentation is read, normal.
func (x Source) trustLevel() string {
	if x.Trust != "" {
		return x.Trust
	}
	switch x.Kind {
	case SourceConfluence, SourceGit, SourceUpload, SourceConsole, SourceFixes:
		return TrustHigh
	}
	return TrustNormal
}

// The weights of a passage's rank: an approved page's, and an excerpt's
// by its source's trust level.
const (
	weightPage   = 2.0
	weightHigh   = 1.5
	weightNormal = 1.0
	weightLow    = 0.5
)

func trustWeight(level string) float64 {
	switch level {
	case TrustHigh:
		return weightHigh
	case TrustLow:
		return weightLow
	}
	return weightNormal
}

var (
	minorOf     = regexp.MustCompile(`^v?(\d{1,3})\.(\d{1,3})(?:\.\d+)?(?:[-+].*)?$`)
	versionPath = regexp.MustCompile(`(?:^|[/_=-])v?(\d{1,2})\.(\d{1,3})(?:\.\d+|\.x)?(?:[/_#?-]|$)`)
	productName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,59}$`)
)

// versionMinor is a version as major.minor ("v1.33.5+rke2r1" is 1.33),
// "" when it is none.
func versionMinor(v string) string {
	m := minorOf.FindStringSubmatch(strings.TrimSpace(strings.ToLower(v)))
	if m == nil {
		return ""
	}
	return m[1] + "." + m[2]
}

// pathVersion is the version a document's path or address names, as
// major.minor: "docs/v1.33/install.md", "/1.31/upgrade".
func pathVersion(texts ...string) string {
	for _, t := range texts {
		if m := versionPath.FindStringSubmatch(strings.ToLower(t)); m != nil {
			return m[1] + "." + m[2]
		}
	}
	return ""
}

// frontTags are the product and version a document's front matter
// names: lines "product: rke2" and "version: 1.33" between the lines
// "---" that open it.
func frontTags(text string) (product, version string) {
	rest, ok := strings.CutPrefix(strings.TrimPrefix(text, "\ufeff"), "---\n")
	if !ok {
		return "", ""
	}
	fm, _, ok := strings.Cut(rest, "\n---")
	if !ok || len(fm) > 4096 {
		return "", ""
	}
	for _, line := range strings.Split(fm, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "product":
			if p := strings.ToLower(v); productName.MatchString(p) {
				product = p
			}
		case "version":
			version = versionMinor(v)
		}
	}
	return product, version
}

// tagsOf are an excerpt's product and version: its front matter's, its
// path's where its source names a product or follows the clusters, its
// source's.
func tagsOf(e Excerpt, x Source) (product, version string) {
	product, version = e.Product, e.ProductVersion
	if product == "" {
		product = x.Product
	}
	if version == "" && (x.Product != "" || x.FollowsClusters) {
		_, id, _ := strings.Cut(e.Key, ":")
		id, _, _ = strings.Cut(id, "#")
		version = pathVersion(id, e.URL)
	}
	if version == "" {
		version = versionMinor(x.ProductVersion)
	}
	return product, version
}

// SourceIndex are a source's index settings, as a SuperAdmin sets them.
type SourceIndex struct {
	Trust           string `json:"trust"`
	Product         string `json:"product"`
	ProductVersion  string `json:"productVersion"`
	FollowsClusters bool   `json:"followsClusterVersions"`
}

// normalized are index settings as they are kept: the trust and the
// product in lower case, the version as major.minor; an error says what
// is wrong.
func (in SourceIndex) normalized() (SourceIndex, error) {
	in.Trust = strings.TrimSpace(strings.ToLower(in.Trust))
	in.Product = strings.TrimSpace(strings.ToLower(in.Product))
	in.ProductVersion = strings.TrimSpace(in.ProductVersion)
	switch {
	case in.Trust != "" && in.Trust != TrustHigh && in.Trust != TrustNormal && in.Trust != TrustLow:
		return in, invalid("a source's trust is high, normal or low, or its kind's default")
	case in.Product != "" && !productName.MatchString(in.Product):
		return in, invalid("a product is a short name in lower case: rke2, kubernetes, longhorn")
	case in.ProductVersion != "" && versionMinor(in.ProductVersion) == "":
		return in, invalid("a version is major.minor, such as 1.33")
	}
	in.ProductVersion = versionMinor(in.ProductVersion)
	return in, nil
}

// SetSourceIndex sets a source's trust level, product, version and
// whether it follows the clusters' versions: a SuperAdmin's, audited
// with what changed.
func (s *Service) SetSourceIndex(ctx context.Context, c Caller, id string, in SourceIndex) (Source, error) {
	if !c.SuperAdmin {
		return Source{}, wrapf(ErrDenied, "only a SuperAdmin sets a source's trust")
	}
	if err := s.leads(); err != nil {
		return Source{}, err
	}
	in, err := in.normalized()
	if err != nil {
		return Source{}, err
	}
	var was, saved Source
	if _, err := s.editSettings(ctx, func(set *Settings) error {
		i := slices.IndexFunc(set.Sources, func(x Source) bool { return x.ID == id })
		if i < 0 {
			return wrapf(ErrNotFound, "no source %q", id)
		}
		if set.Sources[i].Kind == SourceLive {
			return invalid("a live source is looked up, never indexed")
		}
		was = set.Sources[i]
		set.Sources[i].Trust, set.Sources[i].Product, set.Sources[i].ProductVersion, set.Sources[i].FollowsClusters = in.Trust, in.Product, in.ProductVersion, in.FollowsClusters
		saved = set.Sources[i]
		return nil
	}); err != nil {
		return Source{}, err
	}
	var changes []string
	level := func(x Source) string {
		if x.Trust == "" {
			return x.trustLevel() + " (its kind's)"
		}
		return x.Trust
	}
	if was.Trust != saved.Trust {
		changes = append(changes, fmt.Sprintf("trust %s, was %s", level(saved), level(was)))
	}
	if was.Product != saved.Product || was.ProductVersion != saved.ProductVersion {
		changes = append(changes, fmt.Sprintf("about %s, was %s", aboutWords(saved), aboutWords(was)))
	}
	if was.FollowsClusters != saved.FollowsClusters {
		changes = append(changes, map[bool]string{true: "kept to the versions the clusters run", false: "no longer kept to the versions the clusters run"}[saved.FollowsClusters])
	}
	if len(changes) > 0 {
		s.audit(audit.Event{Action: ActionIndexSettings, Who: c.who(), Target: id, Outcome: audit.OutcomeOK, From: c.From, Detail: saved.Name + ": " + strings.Join(changes, "; ")})
		s.kickIndex()
	}
	return saved, nil
}

func aboutWords(x Source) string {
	switch {
	case x.Product == "" && x.ProductVersion == "":
		return "nothing named"
	case x.ProductVersion == "":
		return x.Product
	case x.Product == "":
		return "version " + x.ProductVersion
	}
	return x.Product + " " + x.ProductVersion
}

// clusterMinors are the versions the clusters run, as major.minor.
func (s *Service) clusterMinors(ctx context.Context) []string {
	if s.o.ClusterVersions == nil {
		return nil
	}
	var out []string
	for _, v := range s.o.ClusterVersions(ctx) {
		if m := versionMinor(v); m != "" && !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	slices.Sort(out)
	return out
}

// SourceSize is what the index holds of a source (KN-58).
type SourceSize struct {
	ID        string `json:"id"`
	Documents int    `json:"documents"`
	Passages  int    `json:"passages"`
	Embedded  int    `json:"embedded"`
	// Bytes are its passages' and embeddings' (their text, words and
	// vectors as stored, without the indexes).
	Bytes int64 `json:"bytes"`
}

// sizes are what the index holds of each source ("" is the pages), and
// the database's size.
func (x *PGIndex) sizes(ctx context.Context) (map[string]SourceSize, int64, error) {
	out := map[string]SourceSize{}
	var dbBytes int64
	err := x.d.Do(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, `SELECT coalesce(d.knowledge_source_id, ''), count(DISTINCT d.knowledge_document_id), count(p.knowledge_passage_id), count(e.knowledge_passage_id),
				coalesce(sum(coalesce(pg_column_size(p.body), 0) + coalesce(pg_column_size(p.squashed), 0) + coalesce(pg_column_size(p.search), 0) + coalesce(pg_column_size(e.embedding), 0)), 0)
			FROM knowledge_documents d LEFT JOIN knowledge_passages p USING (knowledge_document_id) LEFT JOIN knowledge_embeddings e USING (knowledge_passage_id)
			GROUP BY 1`)
		if err != nil {
			return err
		}
		var z SourceSize
		if _, err := pgx.ForEachRow(rows, []any{&z.ID, &z.Documents, &z.Passages, &z.Embedded, &z.Bytes}, func() error { out[z.ID] = z; return nil }); err != nil {
			return err
		}
		return q.QueryRow(ctx, "SELECT pg_database_size(current_database())").Scan(&dbBytes)
	})
	return out, dbBytes, err
}
