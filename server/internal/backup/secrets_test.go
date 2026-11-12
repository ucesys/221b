package backup

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// keyOf makes a key from 64 hexadecimal characters, for tests.
func keyOf(t *testing.T, text string) *SecretsKey {
	t.Helper()
	k, err := ParseSecretsKey([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

const (
	keyA = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	keyB = "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"
)

// A secret sealed with a key opens with it only: each value has its own
// nonce, the fingerprint names the key without giving it away, and a value
// changed, or sealed with another key, does not open.
func TestASealedSecretOpensWithItsKeyOnly(t *testing.T) {
	a, b := keyOf(t, keyA), keyOf(t, keyB)
	if a.Fingerprint() == b.Fingerprint() || len(a.Fingerprint()) != 32 || strings.Contains(keyA, a.Fingerprint()) || a.Fingerprint() != keyOf(t, keyA).Fingerprint() {
		t.Fatalf("fingerprints %q %q", a.Fingerprint(), b.Fingerprint())
	}
	one, err := a.SealValue("ghp_verysecret")
	if err != nil {
		t.Fatal(err)
	}
	two, _ := a.SealValue("ghp_verysecret")
	if one == two || strings.Contains(one, "ghp_") || !strings.HasPrefix(one, "<sealed:"+a.Fingerprint()+":") || !SealedValue(one) {
		t.Fatalf("sealed %q and %q", one, two)
	}
	if v, err := a.OpenValue(one); err != nil || v != "ghp_verysecret" {
		t.Fatalf("opened %q %v", v, err)
	}
	if _, err := b.OpenValue(one); !errors.Is(err, ErrOtherKey) {
		t.Fatalf("another key: %v", err)
	}
	// The same fingerprint written over another key's value: the
	// authentication fails.
	forged := strings.Replace(one, a.Fingerprint(), b.Fingerprint(), 1)
	if _, err := b.OpenValue(forged); !errors.Is(err, ErrOtherKey) {
		t.Fatalf("a value moved to another key: %v", err)
	}
	cut := one[:len(one)-6] + "AAAA>"
	if _, err := a.OpenValue(cut); !errors.Is(err, ErrOtherKey) {
		t.Fatalf("a changed value: %v", err)
	}
	if empty, _ := a.SealValue(""); empty != "" {
		t.Fatalf("nothing sealed is %q", empty)
	}
	if SealedValue(Placeholder) || SealedValue("ghp_x") || SealedValue("<sealed:>") {
		t.Fatal("SealedValue")
	}
	for _, bad := range []string{"", "xyz", keyA[:62], keyA + "00"} {
		if _, err := ParseSecretsKey([]byte(bad)); err == nil || strings.Contains(err.Error(), bad) && bad != "" {
			t.Fatalf("key %q: %v", bad, err)
		}
	}
	if _, err := ParseSecretsKey([]byte(" " + keyA + "\n")); err != nil {
		t.Fatalf("a key with a newline, as kubectl writes it: %v", err)
	}
}

// slowSecret is MemorySecret with every read held until both starters
// have read, so they both find no key and both try to make one.
type slowSecret struct {
	MemorySecret
	reads sync.WaitGroup
	once  sync.Map
}

func (s *slowSecret) LoadKey(ctx context.Context, key string) ([]byte, string, bool, error) {
	b, v, ok, err := s.MemorySecret.LoadKey(ctx, key)
	if _, done := s.once.LoadOrStore(ctx.Value(starter{}), true); !done {
		s.reads.Done()
		s.reads.Wait()
	}
	return b, v, ok, err
}

type starter struct{}

// Two replicas starting at once both find no key: the conditional write
// takes one, and both end with the same key. A key there is never
// replaced, not even one that cannot be read.
func TestTheKeyIsMadeOnceByTwoStarters(t *testing.T) {
	st := &slowSecret{}
	st.reads.Add(2)
	var wg sync.WaitGroup
	fps := make([]string, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k, err := EnsureSecretsKey(context.WithValue(context.Background(), starter{}, i), st)
			errs[i] = err
			if k != nil {
				fps[i] = k.Fingerprint()
			}
		}()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || fps[0] == "" || fps[0] != fps[1] {
		t.Fatalf("two starters: %v %v, %q %q", errs[0], errs[1], fps[0], fps[1])
	}
	saved, _, _, _ := st.MemorySecret.LoadKey(context.Background(), KeyData)
	if k := keyOf(t, string(saved)); k.Fingerprint() != fps[0] || st.version != 1 {
		t.Fatalf("the Secret holds %s, written %d times", k.Fingerprint(), st.version)
	}
	// Read again, the same.
	if k, err := EnsureSecretsKey(context.Background(), &st.MemorySecret); err != nil || k.Fingerprint() != fps[0] {
		t.Fatalf("again: %v", err)
	}
	bad := &MemorySecret{}
	_ = bad.SaveKeyIf(context.Background(), KeyData, []byte("not-a-key"), "")
	if _, err := EnsureSecretsKey(context.Background(), bad); err == nil || strings.Contains(err.Error(), "not-a-key") {
		t.Fatalf("a key that cannot be read: %v", err)
	}
	if b, _, _, _ := bad.LoadKey(context.Background(), KeyData); string(b) != "not-a-key" {
		t.Fatal("a key there was replaced")
	}
	if k, err := LoadSecretsKey(context.Background(), &MemorySecret{}); k != nil || err != nil {
		t.Fatalf("no key: %v %v", k, err)
	}
}

// sealedPart is a made-up package that carries its secrets sealed: the
// restore takes a secret back only where it has none.
func (t tokens) sealedPart() Part {
	return Part{
		Name: "tokens", Label: "Tokens", How: "are added or updated; a token here stays", Order: 200,
		Export: func(ctx context.Context, env *Env) (any, error) {
			var out []secretRecord
			for n, tok := range t {
				out = append(out, secretRecord{Name: n, Token: env.Seal(tok)})
			}
			return out, nil
		},
		Restore: func(ctx context.Context, raw json.RawMessage, env *Env) (int, error) {
			var list []secretRecord
			if err := json.Unmarshal(raw, &list); err != nil {
				return 0, err
			}
			n := 0
			for _, r := range list {
				if t[r.Name] != "" {
					continue
				}
				tok, err := env.Open(r.Token, "the token "+r.Name, "Settings › Tokens › "+r.Name)
				if err != nil {
					env.Note("token %s is not restored: %v", r.Name, err)
					continue
				}
				t[r.Name] = tok
				n++
			}
			return n, nil
		},
	}
}

// A backup taken with a key seals every secret and names the key by
// fingerprint; a restore with the same key brings the secrets back, with
// another key or none it brings the rest and lists each secret with where
// to set it, and a placeholder backup still restores as it did.
func TestSecretsComeBackWithTheSameKeyOnly(t *testing.T) {
	ctx := context.Background()
	here := tokens{"ci": "ghp_here", "docs": "ghp_docs"}
	r := NewRegistry()
	r.Register(here.sealedPart())
	r.SetKey("221b-backup-key", func(context.Context) (*SecretsKey, error) { return keyOf(t, keyA), nil })
	f, err := r.Export(ctx, &Env{By: "ops", Taken: time.Date(2026, 11, 9, 3, 0, 0, 0, time.UTC), Version: "0.12.0"})
	if err != nil {
		t.Fatal(err)
	}
	gz, _ := f.Gzip()
	back, err := Read(gz, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(back.Parts["tokens"])
	if strings.Contains(raw, "ghp_") || strings.Contains(raw, Placeholder) || strings.Count(raw, "<sealed:"+keyOf(t, keyA).Fingerprint()+":") != 2 {
		t.Fatalf("the part: %s", raw)
	}
	if back.Secrets == nil || back.Secrets.Key != keyOf(t, keyA).Fingerprint() || back.Secrets.Cipher != Cipher || !strings.Contains(back.LeftOut[0], "sealed with this install's backup key") {
		t.Fatalf("the header: %+v %v", back.Secrets, back.LeftOut)
	}
	if strings.Contains(string(gz), keyA) {
		t.Fatal("the key is in the backup")
	}

	// The same key, on an install without the tokens.
	same := tokens{}
	r2 := NewRegistry()
	r2.Register(same.sealedPart())
	r2.SetKey("x", func(context.Context) (*SecretsKey, error) { return keyOf(t, keyA), nil })
	if c := r2.CheckKey(ctx, back); !c.Sealed || !c.Same {
		t.Fatalf("check: %+v", c)
	}
	rep := r2.Restore(ctx, back, map[string]bool{"tokens": true}, &Env{By: "ops"})
	if rep.Restored["tokens"] != 2 || same["ci"] != "ghp_here" || same["docs"] != "ghp_docs" || len(rep.Secrets) != 0 {
		t.Fatalf("the same key: %+v %v", rep, same)
	}

	// Another key, and no key: the rest comes back, the secrets are listed.
	for _, k := range []*SecretsKey{keyOf(t, keyB), nil} {
		other := tokens{}
		r3 := NewRegistry()
		r3.Register(other.sealedPart())
		r3.SetKey("x", func(context.Context) (*SecretsKey, error) { return k, nil })
		if c := r3.CheckKey(ctx, back); !c.Sealed || c.Same {
			t.Fatalf("check: %+v", c)
		}
		rep := r3.Restore(ctx, back, map[string]bool{"tokens": true}, &Env{By: "ops"})
		if rep.Restored["tokens"] != 2 || other["ci"] != "" || len(rep.Secrets) != 2 || rep.Secrets[0].Part != "Tokens" || !strings.HasPrefix(rep.Secrets[0].Where, "Settings › Tokens › ") || !strings.Contains(strings.Join(rep.Notes, "\n"), "sealed with") {
			t.Fatalf("key %v: %+v %v", k, rep, other)
		}
	}

	// A backup taken without a key holds placeholders, and restores as
	// it always did, listing each secret.
	r4 := NewRegistry()
	r4.Register(here.sealedPart())
	f4, _ := r4.Export(ctx, &Env{By: "ops"})
	if f4.Secrets != nil || !strings.Contains(string(f4.Parts["tokens"]), Placeholder) {
		t.Fatalf("without a key: %s", f4.Parts["tokens"])
	}
	old := tokens{"ci": "ghp_kept"}
	r5 := NewRegistry()
	r5.Register(old.sealedPart())
	r5.SetKey("x", func(context.Context) (*SecretsKey, error) { return keyOf(t, keyA), nil })
	if c := r5.CheckKey(ctx, f4); c.Sealed {
		t.Fatalf("a placeholder backup is not sealed: %+v", c)
	}
	rep = r5.Restore(ctx, f4, map[string]bool{"tokens": true}, &Env{By: "ops"})
	if old["ci"] != "ghp_kept" || len(rep.Secrets) != 1 || rep.Secrets[0].Name != "the token docs" {
		t.Fatalf("a placeholder backup: %+v %v", rep, old)
	}

	// A token in clear is refused.
	f4.Parts["tokens"] = json.RawMessage(`[{"name":"new","token":"ghp_clear"}]`)
	rep = r5.Restore(ctx, f4, map[string]bool{"tokens": true}, &Env{By: "ops"})
	if old["new"] != "" || !strings.Contains(strings.Join(rep.Notes, "\n"), "token new is not restored: the file holds a secret") {
		t.Fatalf("a token in clear: %+v", rep)
	}

	// A key that cannot be read fails the backup, and opens nothing.
	r6 := NewRegistry()
	r6.Register(here.sealedPart())
	r6.SetKey("221b-backup-key", func(context.Context) (*SecretsKey, error) { return nil, errors.New("forbidden") })
	if _, err := r6.Export(ctx, &Env{}); err == nil || !strings.Contains(err.Error(), "221b-backup-key") {
		t.Fatalf("an unreadable key: %v", err)
	}
	if c := r6.CheckKey(ctx, back); c.Same || c.Here != "" {
		t.Fatalf("an unreadable key opens nothing: %+v", c)
	}
}
