package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ucesys/221b/server/internal/kv"
)

func wrapf(err error, format string, a ...any) error {
	return fmt.Errorf("%w: %s", err, fmt.Sprintf(format, a...))
}

// hashOf is a short, stable hash of text: an item's content, a file's id.
func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// clip cuts text to at most n bytes, on a character boundary, marking the
// cut.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return strings.TrimRight(s[:cut], " \n") + "…"
}

// load reads one key of the ConfigMap into v, and its version; a key never
// saved leaves v as it is.
func load(ctx context.Context, b kv.Backend, key string, v any) (string, error) {
	raw, version, err := b.LoadKey(ctx, key)
	if errors.Is(err, kv.ErrNotFound) {
		return version, nil
	}
	if err != nil {
		return "", err
	}
	if len(raw) == 0 {
		return version, nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return "", fmt.Errorf("%s cannot be read: %w", key, err)
	}
	return version, nil
}

// update reads a key, changes it with fn and saves it where it was read,
// trying again when another writer came first. fit may shrink the value
// to its limit (oldest records first) and says when it cannot.
func update[T any](ctx context.Context, b kv.Backend, key string, limit int, fit func(*T) bool, fn func(*T) error) (T, error) {
	var zero T
	for try := 0; try < 10; try++ {
		var v T
		version, err := load(ctx, b, key, &v)
		if err != nil {
			return zero, err
		}
		if err := fn(&v); err != nil {
			return zero, err
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return zero, err
		}
		for limit > 0 && len(raw) > limit {
			if fit == nil || !fit(&v) {
				return zero, fmt.Errorf("%s is full (%d KiB at most): decide or remove what is no longer needed", key, limit>>10)
			}
			if raw, err = json.Marshal(v); err != nil {
				return zero, err
			}
		}
		if _, err := b.SaveKey(ctx, key, raw, version); errors.Is(err, kv.ErrConflict) {
			continue
		} else if err != nil {
			return zero, err
		}
		return v, nil
	}
	return zero, fmt.Errorf("%s: %w; try again", key, kv.ErrConflict)
}
