package knowledge

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/ucesys/221b/server/internal/s3"
)

// S3Store keeps the wiki in a bucket, under a prefix: the approved files
// at their paths, each run's changes under drafts/<run>/ until approved,
// when they are copied over with a conditional write (If-Match on the
// ETag read), so a page someone uploaded meanwhile is not overwritten and
// waits for a person.
type S3Store struct {
	c        *s3.Client
	prefix   string
	sse, kms string
	mu       sync.Mutex
	// cache is what was last read, by path, so an unchanged object is not
	// read again.
	cache map[string]File
}

// NewS3Store opens a bucket for the wiki.
func NewS3Store(c *s3.Client, prefix, sse, kms string) *S3Store {
	return &S3Store{c: c, prefix: prefix, sse: sse, kms: kms, cache: map[string]File{}}
}

func (s *S3Store) Kind() string { return StoreS3 }

const draftsDir = "drafts/"

func (s *S3Store) put() s3.PutOptions {
	return s3.PutOptions{SSE: s.sse, KMSKeyID: s.kms}
}

func contentType(p string) string {
	if strings.HasSuffix(p, ".json") {
		return "application/json"
	}
	return "text/markdown; charset=utf-8"
}

func (s *S3Store) Pull(ctx context.Context) (map[string]File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.c.List(ctx, s.prefix)
	if err != nil {
		return nil, err
	}
	out := map[string]File{}
	for _, o := range list {
		p := strings.TrimPrefix(o.Key, s.prefix)
		if strings.HasPrefix(p, draftsDir) || !wanted(p) {
			continue
		}
		if f, ok := s.cache[p]; ok && o.ETag != "" && f.Version == o.ETag {
			out[p] = f
			continue
		}
		b, etag, err := s.c.GetObject(ctx, o.Key)
		if s3.NotFound(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", p, err)
		}
		out[p] = File{Path: p, Body: b, Version: etag}
	}
	s.cache = copyFiles(out)
	return out, nil
}

func (s *S3Store) Write(ctx context.Context, files []Change, _ string) error {
	for _, f := range files {
		if f.Delete {
			if err := s.c.Delete(ctx, s.prefix+f.Path); err != nil {
				return fmt.Errorf("deleting %s: %w", f.Path, err)
			}
			continue
		}
		o := s.put()
		o.ContentType = contentType(f.Path)
		if _, err := s.c.PutObject(ctx, s.prefix+f.Path, f.Body, o); err != nil {
			return fmt.Errorf("writing %s: %w", f.Path, err)
		}
	}
	return nil
}

func (s *S3Store) Stage(ctx context.Context, run string, changes []Change, _ string) error {
	for _, c := range changes {
		o := s.put()
		o.ContentType = contentType(c.Path)
		if _, err := s.c.PutObject(ctx, s.prefix+draftsDir+run+"/"+c.Path, c.Body, o); err != nil {
			return fmt.Errorf("staging %s: %w", c.Path, err)
		}
	}
	return nil
}

func (s *S3Store) Publish(ctx context.Context, run string, changes []Change, _ string) ([]string, error) {
	var conflicts []string
	for _, c := range changes {
		body, _, err := s.c.GetObject(ctx, s.prefix+draftsDir+run+"/"+c.Path)
		if s3.NotFound(err) {
			return nil, fmt.Errorf("%w: %s is not staged for run %s", ErrNotFound, c.Path, run)
		}
		if err != nil {
			return nil, err
		}
		o := s.put()
		o.ContentType = contentType(c.Path)
		if c.Base == "" {
			o.IfNoneMatch = true
		} else {
			o.IfMatch = c.Base
		}
		if _, err := s.c.PutObject(ctx, s.prefix+c.Path, body, o); s3.Conflict(err) {
			conflicts = append(conflicts, c.Path)
			continue
		} else if err != nil {
			return nil, fmt.Errorf("publishing %s: %w", c.Path, err)
		}
		if err := s.c.Delete(ctx, s.prefix+draftsDir+run+"/"+c.Path); err != nil {
			return nil, err
		}
	}
	sort.Strings(conflicts)
	return conflicts, nil
}

func (s *S3Store) Discard(ctx context.Context, run string) error {
	list, err := s.c.List(ctx, s.prefix+draftsDir+run+"/")
	if err != nil {
		return err
	}
	for _, o := range list {
		if err := s.c.Delete(ctx, o.Key); err != nil {
			return err
		}
	}
	return nil
}

func (s *S3Store) Check(ctx context.Context) (StoreCheck, error) {
	files, err := s.Pull(ctx)
	if err != nil {
		return StoreCheck{}, err
	}
	out := StoreCheck{Files: len(files)}
	v, err := s.c.Versioning(ctx)
	if err != nil {
		out.Warnings = append(out.Warnings, "The bucket's versioning could not be read ("+err.Error()+"); turn it on, so every page keeps its history.")
		return out, nil
	}
	out.Versioning = v
	if v != "Enabled" {
		out.Warnings = append(out.Warnings, "Versioning is off on this bucket: a page overwritten keeps no history. Turn it on in the bucket's settings.")
	}
	return out, nil
}
