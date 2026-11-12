package knowledge

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/awsv4"
	"github.com/ucesys/221b/server/internal/s3"
)

// The local copy: every replica keeps the approved files on its own disk
// (an emptyDir in the chart) and builds its index from them, at start and
// whenever the store changed. ask_knowledge reads this copy, never the
// network.

// openStore makes the client of a store from its settings and keys.
func (s *Service) openStore(st StoreSettings, c Creds) (Store, error) {
	switch st.Kind {
	case StoreGit:
		return NewGitStore(s.dir(), st.URL, st.Branch, st.Username, c.Token, st.CAPEM), nil
	case StoreS3:
		creds := func(ctx context.Context) (awsv4.Credentials, error) {
			if st.Credentials == CredentialsIdentity {
				return s.o.Identity.Credentials(ctx, st.Region)
			}
			if c.AccessKeyID == "" || c.SecretAccessKey == "" {
				return awsv4.Credentials{}, errors.New("no access key is saved for the wiki's bucket: give one, or use the pod's identity")
			}
			return awsv4.Credentials{AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey}, nil
		}
		var hc = s.o.HTTP
		if st.CAPEM != "" {
			hc = nil
		}
		client, err := s3.New(s3.Config{Endpoint: st.Endpoint, Region: st.Region, Bucket: st.Bucket, PathStyle: st.PathStyle, CAPEM: st.CAPEM, Credentials: creds, HTTP: hc, Now: s.o.Now})
		if err != nil {
			return nil, invalid("%v", err)
		}
		return NewS3Store(client, st.Prefix, st.SSE, st.KMSKeyID), nil
	case StoreConfigMap:
		if s.cmStore == nil {
			return nil, invalid("this console has no ConfigMap for the wiki (the chart's <release>-wiki, -wiki-configmap): choose Git or S3")
		}
		return s.cmStore, nil
	}
	return nil, ErrNoStore
}

// storeKind is the kind of store the settings mean: the one they name, or
// the temporary ConfigMap store when they name none and the chart made
// its ConfigMap (CF-57); "" is none.
func (s *Service) storeKind(st StoreSettings) string {
	if st.Kind == "" && s.cmStore != nil {
		return StoreConfigMap
	}
	return st.Kind
}

// dir is where the console keeps what it holds on disk.
func (s *Service) dir() string { return s.o.Dir }

// currentStore is the store the settings name, made again when they or
// its keys changed.
func (s *Service) currentStore(ctx context.Context) (Store, StoreSettings, error) {
	set, err := s.Settings(ctx)
	if err != nil {
		return nil, StoreSettings{}, err
	}
	if s.o.Store != nil {
		return s.o.Store, set.Store, nil
	}
	if s.cmStore != nil && s.storeKind(set.Store) == StoreConfigMap {
		// The temporary store needs no keys (CF-57).
		return s.cmStore, set.Store, nil
	}
	if set.Store.Kind == "" {
		return nil, set.Store, ErrNoStore
	}
	c, err := s.creds(ctx, storeKey)
	if err != nil {
		return nil, set.Store, err
	}
	fp := set.Store.fingerprint() + "\x00" + hashOf([]byte(c.Token+"\x00"+c.AccessKeyID+"\x00"+c.SecretAccessKey))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store != nil && s.storeFor == fp {
		return s.store, set.Store, nil
	}
	st, err := s.openStore(set.Store, c)
	if err != nil {
		return nil, set.Store, err
	}
	s.store, s.storeFor = st, fp
	return st, set.Store, nil
}

// Start reads the local copy left on disk, if any, and builds the index
// from it, so ask_knowledge answers before the store is reached.
func (s *Service) Start(ctx context.Context) {
	files := map[string]File{}
	root := filepath.Join(s.dir(), "copy")
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !wanted(rel) {
			return nil
		}
		if b, err := os.ReadFile(p); err == nil {
			files[rel] = File{Path: rel, Body: b}
		}
		return nil
	})
	if len(files) > 0 {
		s.setFiles(ctx, files, false)
	}
	if err := s.Refresh(ctx); err != nil && !errors.Is(err, ErrNoStore) {
		s.o.Logger.Warn("reading the wiki failed; the agents ask the copy on disk until it is read", "err", err)
	}
}

// Refresh reads the store again into the local copy and the index.
func (s *Service) Refresh(ctx context.Context) error {
	st, _, err := s.currentStore(ctx)
	if err != nil {
		return err
	}
	files, err := st.Pull(ctx)
	if err != nil {
		return err
	}
	s.setFiles(ctx, files, true)
	return nil
}

// setFiles takes a new local copy: in memory, on disk when it came from
// the store, and in the index.
func (s *Service) setFiles(ctx context.Context, files map[string]File, mirror bool) {
	m := newManifest()
	if f, ok := files[ManifestPath]; ok {
		m = parseManifest(f.Body)
	}
	s.mu.Lock()
	s.files, s.manifest, s.pulled = files, m, s.o.Now()
	s.mu.Unlock()
	if mirror {
		s.mirror(files)
	}
	s.reindexWith(ctx)
}

// mirror writes the local copy to disk, removing what the store no
// longer holds.
func (s *Service) mirror(files map[string]File) {
	root := filepath.Join(s.dir(), "copy")
	keep := map[string]bool{}
	for p, f := range files {
		dst := filepath.Join(root, filepath.FromSlash(p))
		keep[dst] = true
		if old, err := os.ReadFile(dst); err == nil && string(old) == string(f.Body) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			s.o.Logger.Warn("the wiki's copy on disk could not be written", "err", err)
			return
		}
		if err := os.WriteFile(dst, f.Body, 0o600); err != nil {
			s.o.Logger.Warn("the wiki's copy on disk could not be written", "err", err)
			return
		}
	}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !keep[p] {
			_ = os.Remove(p)
		}
		return nil
	})
}

// reindex builds the index again from the local copy, as the settings now
// mark stale pages.
func (s *Service) reindex() { s.reindexWith(context.Background()) }

func (s *Service) reindexWith(ctx context.Context) {
	set, err := s.Settings(ctx)
	days := DefaultStaleDays
	if err == nil {
		days = set.staleDays()
	}
	s.mu.RLock()
	files, m := s.files, s.manifest
	s.mu.RUnlock()
	idx := NewIndex(files, m, days, s.o.Now())
	s.mu.Lock()
	s.index = idx
	s.mu.Unlock()
}

// refreshEvery is how often the local copy is read again.
func (s *Service) refreshEvery(ctx context.Context) time.Duration {
	set, err := s.Settings(ctx)
	if err == nil && set.Store.RefreshMinutes > 0 {
		return time.Duration(set.Store.RefreshMinutes) * time.Minute
	}
	if err == nil && (set.Store.Kind == StoreS3 || s.storeKind(set.Store) == StoreConfigMap) {
		return s.o.RefreshS3
	}
	return s.o.RefreshGit
}

// Run keeps the local copy current on every replica, and on the leader
// runs the publisher's outbox and nightly check. It returns when ctx ends.
func (s *Service) Run(ctx context.Context) {
	if s.o.Publisher != nil {
		go s.o.Publisher.Run(ctx, s.o.Leads)
	}
	for {
		wait := s.refreshEvery(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		if err := s.Refresh(rctx); err != nil && !errors.Is(err, ErrNoStore) && ctx.Err() == nil {
			s.o.Logger.Warn("reading the wiki again failed; the agents ask the copy as it was", "err", err)
		}
		cancel()
	}
}

// pageFiles are the local copy's pages.
func pageFiles(files map[string]File) []File {
	var out []File
	for p, f := range files {
		if strings.HasPrefix(p, PagesDir) {
			out = append(out, f)
		}
	}
	return out
}
