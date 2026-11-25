package knowledge

import (
	"context"
	"time"
)

// IndexView is the search index as Administration › AI › Knowledge shows
// it (KN-53 to KN-58).
type IndexView struct {
	Documents int `json:"documents"`
	Passages  int `json:"passages"`
	// Fuzzy and Semantic say the database has pg_trgm and pgvector.
	Fuzzy    bool `json:"fuzzy"`
	Semantic bool `json:"semantic"`
	// Embedded are the passages with an embedding by the chosen model;
	// EmbedError why the last pass stopped, EmbeddedAt when it ran.
	Embedded   int       `json:"embedded"`
	EmbedError string    `json:"embedError,omitempty"`
	EmbeddedAt time.Time `json:"embeddedAt,omitzero"`
	// Providers are the providers an embedding model may be chosen on.
	Providers []EmbeddingProvider `json:"providers"`
	// --- Metadata, weighting and size (KN-56 to KN-58) ---
	// Sources are each source's index settings and what the index holds
	// of it, in the settings' order; Pages what it holds of the pages.
	Sources []SourceIndexView `json:"sources"`
	Pages   SourceSize        `json:"pageSize"`
	// DatabaseBytes is the database's size, VolumeBytes its volume's as
	// the chart says (0: not known); Full says the database holds 80 % of
	// it or more.
	DatabaseBytes int64 `json:"databaseBytes"`
	VolumeBytes   int64 `json:"volumeBytes,omitempty"`
	Full          bool  `json:"full,omitempty"`
	// ClusterVersions are the versions the clusters run, major.minor.
	ClusterVersions []string `json:"clusterVersions"`
}

// SourceIndexView is a source's index settings and size.
type SourceIndexView struct {
	SourceSize
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Trust is its level, TrustDefault says it is its kind's.
	Trust           string `json:"trust"`
	TrustDefault    bool   `json:"trustDefault,omitempty"`
	Product         string `json:"product,omitempty"`
	ProductVersion  string `json:"productVersion,omitempty"`
	FollowsClusters bool   `json:"followsClusterVersions,omitempty"`
}

// fullAt is the share of the volume the database may hold before the
// index warns (KN-58).
const fullAt = 0.8

// indexView is the index's view; nil without the index in the database.
func (s *Service) indexView(ctx context.Context, set Settings) *IndexView {
	if s.pg == nil {
		return nil
	}
	v := &IndexView{Providers: []EmbeddingProvider{}}
	f := s.pg.features(ctx)
	v.Fuzzy, v.Semantic = f.trgm, f.vector
	if d, p, err := s.pg.Count(ctx); err == nil {
		v.Documents, v.Passages = d, p
	}
	if set.Embeddings.on() {
		v.Embedded, _ = s.pg.embedded(ctx, set.Embeddings.Model)
		v.EmbeddedAt, v.EmbedError = s.embedded.get()
	}
	if s.o.Embedder != nil {
		v.Providers = append(v.Providers, s.o.Embedder.Providers(ctx)...)
	}
	sizes, dbBytes, err := s.pg.sizes(ctx)
	if err != nil {
		s.o.Logger.Warn("the knowledge index's size could not be read", "err", err)
	}
	v.Sources, v.Pages = []SourceIndexView{}, sizes[""]
	v.Pages.ID = ""
	for _, x := range set.Sources {
		if x.Kind == SourceLive {
			continue
		}
		z := sizes[x.ID]
		z.ID = x.ID
		v.Sources = append(v.Sources, SourceIndexView{SourceSize: z, Name: x.Name, Kind: x.Kind, Trust: x.trustLevel(), TrustDefault: x.Trust == "",
			Product: x.Product, ProductVersion: x.ProductVersion, FollowsClusters: x.FollowsClusters})
	}
	v.DatabaseBytes, v.VolumeBytes = dbBytes, s.o.VolumeBytes
	v.Full = v.VolumeBytes > 0 && float64(dbBytes) >= fullAt*float64(v.VolumeBytes)
	v.ClusterVersions = s.clusterMinors(ctx)
	if v.ClusterVersions == nil {
		v.ClusterVersions = []string{}
	}
	return v
}
