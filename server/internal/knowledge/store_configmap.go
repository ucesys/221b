package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/ucesys/221b/server/internal/kv"
)

// --- The ConfigMap wiki store (temporary, CF-57) ---

// WikiKey is the one key of the ConfigMap <release>-wiki: every file of
// the wiki and the staged runs, in one JSON document, since a ConfigMap's
// keys cannot hold the "/" of a path.
const WikiKey = "wiki.json"

// DefaultWikiBytes is the most the ConfigMap wiki store holds, below the
// ConfigMap's 1 MiB.
const DefaultWikiBytes = 900 << 10

// ErrStoreFull is a write the ConfigMap wiki store refused for want of
// room.
var ErrStoreFull = errors.New("the ConfigMap wiki store is full")

// ConfigMapStore keeps the wiki in one key of a ConfigMap of its own: a
// temporary store for trying Mycroft's wiki without a repository or a
// bucket, to be removed once Git and S3 are tried enough. Every replica
// reads the same ConfigMap; a write is conditional on the version read
// and tried again when another writer came first, so none is lost.
type ConfigMapStore struct {
	b     kv.Backend
	limit int
}

// cmWiki is the document the key holds.
type cmWiki struct {
	// Seq numbers the files' versions, so a file written again has a new
	// one.
	Seq   int               `json:"seq"`
	Files map[string]cmFile `json:"files"`
	// Staged are the runs' page changes waiting for review, by run and
	// path.
	Staged map[string]map[string]string `json:"staged,omitempty"`
}

type cmFile struct {
	Body    string `json:"body"`
	Version string `json:"version"`
}

// NewConfigMapStore keeps the wiki in b, in at most limit bytes (0: the
// default).
func NewConfigMapStore(b kv.Backend, limit int) *ConfigMapStore {
	if limit <= 0 {
		limit = DefaultWikiBytes
	}
	return &ConfigMapStore{b: b, limit: limit}
}

func (c *ConfigMapStore) Kind() string { return StoreConfigMap }

// Used is how many bytes the store holds now, of its limit (KN-37).
func (c *ConfigMapStore) Used(ctx context.Context) (int, error) {
	raw, _, err := c.b.LoadKey(ctx, WikiKey)
	if errors.Is(err, kv.ErrNotFound) {
		return 0, nil
	}
	return len(raw), err
}

func (w *cmWiki) version() string { w.Seq++; return "c" + strconv.Itoa(w.Seq) }

// load reads the document, its version and its size.
func (c *ConfigMapStore) load(ctx context.Context) (cmWiki, string, int, error) {
	w := cmWiki{Files: map[string]cmFile{}, Staged: map[string]map[string]string{}}
	raw, version, err := c.b.LoadKey(ctx, WikiKey)
	if errors.Is(err, kv.ErrNotFound) {
		return w, version, 0, nil
	}
	if err != nil {
		return w, "", 0, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &w); err != nil {
			return w, "", 0, fmt.Errorf("the ConfigMap wiki store cannot be read: %w", err)
		}
	}
	if w.Files == nil {
		w.Files = map[string]cmFile{}
	}
	if w.Staged == nil {
		w.Staged = map[string]map[string]string{}
	}
	return w, version, len(raw), nil
}

// encode writes the document as it is kept, the pages' "<" and ">" as
// they are, so a page takes no more room than its text.
func (w cmWiki) encode() ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(w); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// edit reads the document, changes it with fn and saves it where it was
// read, trying again when another replica came first. A change that would
// pass the store's room is refused whole: nothing is dropped. One that
// makes it smaller always goes through.
func (c *ConfigMapStore) edit(ctx context.Context, fn func(*cmWiki) error) error {
	for try := 0; try < 10; try++ {
		w, version, size, err := c.load(ctx)
		if err != nil {
			return err
		}
		if err := fn(&w); err != nil {
			return err
		}
		raw, err := w.encode()
		if err != nil {
			return err
		}
		if len(raw) > c.limit && len(raw) > size {
			return fmt.Errorf("%w (%d KiB): move the wiki to Git or S3 in Settings › Knowledge", ErrStoreFull, c.limit>>10)
		}
		if _, err := c.b.SaveKey(ctx, WikiKey, raw, version); errors.Is(err, kv.ErrConflict) {
			continue
		} else if err != nil {
			return err
		}
		return nil
	}
	return fmt.Errorf("the ConfigMap wiki store: %w; try again", kv.ErrConflict)
}

func (c *ConfigMapStore) Pull(ctx context.Context) (map[string]File, error) {
	w, _, _, err := c.load(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]File{}
	for p, f := range w.Files {
		if wanted(p) {
			out[p] = File{Path: p, Body: []byte(f.Body), Version: f.Version}
		}
	}
	return out, nil
}

func (c *ConfigMapStore) Write(ctx context.Context, files []Change, _ string) error {
	return c.edit(ctx, func(w *cmWiki) error {
		for _, f := range files {
			if f.Delete {
				delete(w.Files, f.Path)
				continue
			}
			w.Files[f.Path] = cmFile{Body: string(f.Body), Version: w.version()}
		}
		return nil
	})
}

func (c *ConfigMapStore) Stage(ctx context.Context, run string, changes []Change, _ string) error {
	return c.edit(ctx, func(w *cmWiki) error {
		st := map[string]string{}
		for _, ch := range changes {
			st[ch.Path] = string(ch.Body)
		}
		w.Staged[run] = st
		return nil
	})
}

func (c *ConfigMapStore) Publish(ctx context.Context, run string, changes []Change, _ string) ([]string, error) {
	var conflicts []string
	err := c.edit(ctx, func(w *cmWiki) error {
		conflicts = nil
		st, ok := w.Staged[run]
		if !ok {
			return fmt.Errorf("%w: nothing is staged for %s", ErrNotFound, run)
		}
		for _, ch := range changes {
			if f, ok := w.Files[ch.Path]; (ok && f.Version != ch.Base) || (!ok && ch.Base != "") {
				conflicts = append(conflicts, ch.Path)
				continue
			}
			body, ok := st[ch.Path]
			if !ok {
				return fmt.Errorf("%w: %s is not staged for run %s", ErrNotFound, ch.Path, run)
			}
			w.Files[ch.Path] = cmFile{Body: body, Version: w.version()}
			delete(st, ch.Path)
		}
		if len(st) == 0 {
			delete(w.Staged, run)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(conflicts)
	return conflicts, nil
}

func (c *ConfigMapStore) Discard(ctx context.Context, run string) error {
	w, _, _, err := c.load(ctx)
	if err != nil {
		return err
	}
	if _, ok := w.Staged[run]; !ok {
		return nil
	}
	return c.edit(ctx, func(w *cmWiki) error { delete(w.Staged, run); return nil })
}

func (c *ConfigMapStore) Check(ctx context.Context) (StoreCheck, error) {
	w, _, size, err := c.load(ctx)
	if err != nil {
		return StoreCheck{}, err
	}
	out := StoreCheck{}
	for p := range w.Files {
		if wanted(p) {
			out.Files++
		}
	}
	out.Warnings = append(out.Warnings, fmt.Sprintf("The wiki is kept in a ConfigMap, %d KiB of its %d KiB used: temporary, for testing, and it will be removed. Move the wiki to Git or S3 before relying on it.", (size+1023)>>10, c.limit>>10))
	return out, nil
}
