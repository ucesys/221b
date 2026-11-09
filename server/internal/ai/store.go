package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/ucesys/221b/server/internal/kv"
)

// Each key is read and written as a whole, over the version read, as the
// console's other records are (ADR-61): a writer that meets another
// replica's save reads again and applies its change to that.

// tries bounds the attempts of a change that keeps meeting other saves.
const tries = 8

// load reads a key into v; a key never saved leaves v as it is.
func load(ctx context.Context, b kv.Backend, key string, v any) (string, error) {
	raw, version, err := b.LoadKey(ctx, key)
	if errors.Is(err, kv.ErrNotFound) {
		return version, nil
	}
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		// Read as empty, the next save would wipe it.
		return "", fmt.Errorf("%s is not readable: %w", key, err)
	}
	return version, nil
}

// change applies edit to what a key holds now and saves it over that
// version, again on a conflict.
func change[T any](ctx context.Context, b kv.Backend, key string, edit func(*T) error) (T, error) {
	var err error
	for try := 0; try < tries; try++ {
		if try > 0 {
			select {
			case <-time.After(rand.N(min(40*time.Millisecond<<try, time.Second))):
			case <-ctx.Done():
				var zero T
				return zero, ctx.Err()
			}
		}
		var v T
		var version string
		if version, err = load(ctx, b, key, &v); err != nil {
			return v, err
		}
		if err = edit(&v); err != nil {
			return v, err
		}
		raw, _ := json.Marshal(v)
		if _, err = b.SaveKey(ctx, key, raw, version); errors.Is(err, kv.ErrConflict) {
			continue
		}
		return v, err
	}
	var zero T
	return zero, err
}
