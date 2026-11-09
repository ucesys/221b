package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ucesys/221b/server/internal/confluence"
)

// A backup holds the knowledge's connections (sources and where the wiki
// is kept, each credential sealed with the install's backup key, ADR-137,
// or a placeholder), the manifest of what was read, and the publisher's
// settings and pages. The wiki's pages have their own history in git or
// S3, and the index is built again, so neither is in it.

// Placeholder stands where a credential was (backup.Placeholder).
const Placeholder = "<secret: not in the backup>"

// Seal and Open are the backup's (backup.Env's): Seal makes a secret what
// a backup carries; Open takes one back from a file, "" when it cannot,
// and then lists it to set again as name, at where.
type (
	Seal func(value string) string
	Open func(inFile, name, where string) (string, error)
)

// BackupPart is the part's content.
type BackupPart struct {
	Settings Settings `json:"settings"`
	// Credentials are the credentials set, by the Secret's key: each one
	// sealed (its JSON), or the placeholder, never in clear.
	Credentials map[string]string `json:"credentials,omitempty"`
	Manifest    *Manifest         `json:"manifest,omitempty"`
	Publisher   *confluence.Saved `json:"publisher,omitempty"`
}

// Export is the backup part, nil when there is nothing to keep.
func (s *Service) Export(ctx context.Context, seal Seal) (*BackupPart, error) {
	set, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	out := &BackupPart{Settings: set, Credentials: map[string]string{}}
	keys := []string{storeKey, hookKey}
	for _, x := range set.Sources {
		keys = append(keys, sourceKey(x.ID))
	}
	for _, k := range keys {
		c, err := s.creds(ctx, k)
		if err != nil {
			return nil, err
		}
		if !c.empty() {
			b, err := json.Marshal(c)
			if err != nil {
				return nil, err
			}
			out.Credentials[k] = seal(string(b))
		}
	}
	s.mu.RLock()
	if len(s.manifest.Items) > 0 {
		out.Manifest = s.manifest.clone()
	}
	s.mu.RUnlock()
	if s.o.Publisher != nil {
		p, err := s.o.Publisher.Export(ctx, seal)
		if err != nil {
			return nil, err
		}
		out.Publisher = p
	}
	return out, nil
}

// ErrSecretInFile is a file that holds a credential in clear where a
// backup holds a sealed one or a placeholder.
var ErrSecretInFile = errors.New("the file holds a credential in clear where a backup holds a sealed one or a placeholder")

// sealedOrPlaceholder says whether a file's credential may stand there:
// nothing, the placeholder, or a sealed value (backup.SealedValue).
func sealedOrPlaceholder(v string) bool {
	return v == "" || v == Placeholder || strings.HasPrefix(v, "<sealed:") && strings.HasSuffix(v, ">")
}

// credName says which credential a Secret's key is, and where it is set.
func credName(k string, set Settings) (string, string) {
	switch {
	case k == storeKey:
		return "the wiki store's credential", "Settings › Knowledge › Wiki"
	case k == hookKey:
		return "the knowledge webhook's token", "Settings › Knowledge › Webhook"
	case strings.HasPrefix(k, "source-"):
		id := strings.TrimPrefix(k, "source-")
		name := id
		for _, x := range set.Sources {
			if x.ID == id && x.Name != "" {
				name = x.Name
			}
		}
		return "the token of the knowledge source " + name, "Settings › Knowledge › Sources › " + name
	}
	return "the knowledge credential " + k, "Settings › Knowledge"
}

// Import restores the part: the settings replace the console's, every
// credential the console has is kept, one it lacks is taken from the file
// when the backup's key opens it (open), and listed to give again when not;
// the manifest is written to the wiki's store when there is one, and the
// publisher's settings and pages are restored. It returns what is left to
// do by hand.
func (s *Service) Import(ctx context.Context, raw json.RawMessage, open Open) (int, []string, error) {
	var in BackupPart
	if err := json.Unmarshal(raw, &in); err != nil {
		return 0, nil, err
	}
	for k, v := range in.Credentials {
		if !sealedOrPlaceholder(v) {
			return 0, nil, fmt.Errorf("%s: %w", k, ErrSecretInFile)
		}
	}
	for _, x := range in.Settings.Sources {
		if err := x.Validate(); err != nil {
			return 0, nil, fmt.Errorf("source %s: %w", x.ID, err)
		}
	}
	if err := in.Settings.Store.Validate(); err != nil {
		return 0, nil, err
	}
	var notes []string
	if _, err := s.editSettings(ctx, func(v *Settings) error { *v = in.Settings.withDefaults(); return nil }); err != nil {
		return 0, nil, err
	}
	n := len(in.Settings.Sources) + 1
	keys := make([]string, 0, len(in.Credentials))
	for k := range in.Credentials {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		c, err := s.creds(ctx, k)
		if err != nil || !c.empty() {
			continue
		}
		name, where := credName(k, in.Settings)
		v, err := open(in.Credentials[k], name, where)
		if err != nil || v == "" {
			continue
		}
		var back Creds
		if json.Unmarshal([]byte(v), &back) != nil || back.empty() {
			notes = append(notes, "knowledge: "+name+" in the backup cannot be read; give it again in "+where)
			continue
		}
		if err := s.saveCreds(ctx, k, CredsChange{Token: &back.Token, AccessKeyID: &back.AccessKeyID, SecretAccessKey: &back.SecretAccessKey}); err != nil {
			notes = append(notes, "knowledge: "+name+" was not saved: "+err.Error())
		}
	}
	if in.Manifest != nil {
		if store, _, err := s.currentStore(ctx); err == nil {
			if err := store.Write(ctx, []Change{{Path: ManifestPath, Body: in.Manifest.encode()}}, "The manifest, restored from a backup"); err != nil {
				notes = append(notes, "knowledge: the manifest could not be written to the wiki's store: "+err.Error())
			} else {
				n++
				_ = s.Refresh(ctx)
			}
		} else {
			notes = append(notes, "knowledge: the manifest was not restored, since the wiki's store cannot be reached; Mycroft reads every source again at its next sync")
		}
	}
	if in.Publisher != nil && s.o.Publisher != nil {
		more, err := s.o.Publisher.Import(ctx, *in.Publisher, open)
		if err != nil {
			notes = append(notes, "knowledge: the Confluence publisher's settings were not restored: "+err.Error())
		} else {
			n++
			notes = append(notes, more...)
		}
	}
	return n, notes, nil
}
