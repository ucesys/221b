package credentials

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// A backup holds the credentials (CR-13) as the consoles' other secrets
// are held (ADR-137): each one's secret sealed with the install's backup
// key, or a placeholder, never in clear. A restore adds the credentials
// missing, by id, and keeps the ones the console has; one whose secret
// the backup's key does not open comes back without it, listed to be
// given again in Settings › Credentials.

// BackupEntry is one credential in a backup.
type BackupEntry struct {
	Credential
	// Secret is its secret part's JSON, sealed, or the placeholder.
	Secret string `json:"secret,omitempty"`
}

// Export is the credentials for a backup, each secret sealed by seal.
func (s *Service) Export(ctx context.Context, seal func(string) string) ([]BackupEntry, error) {
	list, err := s.List(ctx)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	out := make([]BackupEntry, 0, len(list))
	for _, c := range list {
		sec, err := s.secret(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		e := BackupEntry{Credential: c}
		if !sec.empty() {
			b, _ := json.Marshal(sec)
			e.Secret = seal(string(b))
		}
		out = append(out, e)
	}
	return out, nil
}

// Import restores a backup's credentials: check refuses a secret in
// clear, open takes a sealed one back ("" when it cannot, listing it to
// give again at where). It returns how many it added.
func (s *Service) Import(ctx context.Context, list []BackupEntry, check func(string) error, open func(inFile, name, where string) (string, error), note func(string, ...any)) (int, error) {
	for _, e := range list {
		if err := check(e.Secret); err != nil {
			return 0, fmt.Errorf("the credential %s: %w", e.Name, err)
		}
	}
	have, err := s.List(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range list {
		if slices.ContainsFunc(have, func(c Credential) bool { return c.ID == e.ID }) {
			continue
		}
		if slices.ContainsFunc(have, func(c Credential) bool { return strings.EqualFold(c.Name, e.Name) }) {
			note("credential %s: one of that name is here already; the backup's is not restored", e.Name)
			continue
		}
		var sec Secret
		if len(e.Set) > 0 || e.Secret != "" {
			v, err := open(e.Secret, "the secret of the credential "+e.Name, "Settings › Credentials › "+e.Name)
			if err == nil && v != "" {
				if json.Unmarshal([]byte(v), &sec) != nil {
					note("credential %s: its secret in the backup cannot be read; give it again in Settings › Credentials", e.Name)
					sec = Secret{}
				}
			}
		}
		c := e.Credential
		sec = keep(c.Kind, sec)
		c.Set, c.LastTest = sec.parts(), nil
		if err := s.add(ctx, c, sec); err != nil {
			note("credential %s: %v", e.Name, err)
			continue
		}
		have = append(have, c)
		n++
	}
	return n, nil
}
