package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/imagecheck"
	"github.com/ucesys/221b/server/internal/live"
	"github.com/ucesys/221b/server/internal/views"
)

// Newer images (HA-21 to HA-23, ADR-154): whether a registry publishes a
// newer image than one a release runs. The leader asks the registries in
// the background (imagecheck); these reads answer from what it kept and
// never wait for a registry.

// newerImage is one image of a release with a newer one published.
type newerImage struct {
	Image      string `json:"image"`
	Registry   string `json:"registry,omitempty"`
	Repository string `json:"repository"`
	Tag        string `json:"tag,omitempty"`
	// Newer is the newer tag; for a tag that moved, the tag, with Digest
	// the digest it points at now.
	Newer  string `json:"newer"`
	Digest string `json:"digest,omitempty"`
}

// newerImagesView answers GET helm/newer-images.
type newerImagesView struct {
	// Enabled is false when Settings turned the check off.
	Enabled bool `json:"enabled"`
	// Releases are those with a newer image, by namespace/name.
	Releases map[string][]newerImage `json:"releases"`
	// Pending counts the images whose registries are still being asked:
	// the page asks again soon while there are any.
	Pending int `json:"pending"`
}

// checksImages says whether the check is on: a checker, and the setting
// not off (HA-23).
func (s *Server) checksImages() bool {
	if s.opts.ImageChecks == nil {
		return false
	}
	return s.opts.Settings == nil || s.opts.Settings.Current().ChecksImages()
}

// checkImage checks one image from its reference.
func (s *Server) checkImage(ref string) imagecheck.Result {
	im := apps.ParseImage(ref)
	return s.opts.ImageChecks.Check(im.Registry, im.Repository, im.Tag, im.Digest)
}

// getNewerImages lists, for each release the person may see, the images
// it runs that have a newer one published (HA-22): the Helm tab's rows and
// the release's panel read it.
func (s *Server) getNewerImages(w http.ResponseWriter, r *http.Request) {
	_, cl, ok := s.lookup(w, r)
	if !ok {
		return
	}
	out := newerImagesView{Enabled: s.checksImages(), Releases: map[string][]newerImage{}}
	if !out.Enabled || !cl.SecretsRead() {
		b, _ := json.Marshal(out)
		writeJSON(w, r, b)
		return
	}
	a := s.access(r, cl)
	if _, err := s.prepare(r.Context(), a, cl, []string{live.ViewHelm}); err != nil {
		jsonError(w, http.StatusBadGateway, "asking Kubernetes what you may see failed: "+err.Error())
		return
	}
	for _, key := range views.ReleaseKeys(cl) {
		if a != nil && !a.Allow(live.ViewHelm, key) {
			continue
		}
		ns, name, _ := strings.Cut(key, "/")
		for _, ref := range views.ReleaseImages(cl, ns, name) {
			res := s.checkImage(ref)
			switch res.State {
			case imagecheck.StatePending:
				out.Pending++
			case imagecheck.StateNewer:
				im := apps.ParseImage(ref)
				out.Releases[key] = append(out.Releases[key], newerImage{Image: ref, Registry: im.Registry, Repository: im.Repository, Tag: im.Tag, Newer: res.Newer, Digest: res.Digest})
			}
		}
	}
	for _, list := range out.Releases {
		sort.Slice(list, func(i, j int) bool { return list[i].Image < list[j].Image })
	}
	b, _ := json.Marshal(out)
	writeJSON(w, r, b)
}

// imageChecks checks each distinct image of a revision for the Images
// tab, or nil when the check is off.
func (s *Server) imageChecks(images []apps.Image) map[string]imagecheck.Result {
	if !s.checksImages() {
		return nil
	}
	out := map[string]imagecheck.Result{}
	for _, im := range images {
		if _, done := out[im.Image]; !done {
			out[im.Image] = s.opts.ImageChecks.Check(im.Registry, im.Repository, im.Tag, im.Digest)
		}
	}
	return out
}
