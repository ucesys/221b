package knowledge

import (
	"context"
	"fmt"
	"strings"

	"github.com/ucesys/221b/server/internal/confluence"
)

// WikiPages are the approved pages as the Confluence publisher shows
// them, each with a link back to where it is edited.
func (s *Service) WikiPages(ctx context.Context) ([]confluence.WikiPage, error) {
	set, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	idx := s.index
	s.mu.RUnlock()
	var out []confluence.WikiPage
	for _, p := range idx.Pages() {
		wp := confluence.WikiPage{Path: p.Path, Title: p.Title, Markdown: p.Body, Updated: p.Updated, Stale: p.Stale, EditURL: editURL(set.Store, p.Path)}
		for _, r := range p.Sources {
			line := r.Title
			if r.Version != "" && len(r.Version) <= 12 {
				line += ", version " + r.Version
			}
			if !r.Date.IsZero() {
				line += ", " + r.Date.Format("2006-01-02")
			}
			wp.Sources = append(wp.Sources, line)
		}
		out = append(out, wp)
	}
	return out, nil
}

// editURL is where a page is edited: its file in the repository, or its
// object in the bucket.
func editURL(st StoreSettings, path string) string {
	switch st.Kind {
	case StoreGit:
		return strings.TrimSuffix(st.URL, ".git") + "/blob/" + st.Branch + "/" + path
	case StoreS3:
		return fmt.Sprintf("s3://%s/%s%s", st.Bucket, st.Prefix, path)
	}
	return ""
}
