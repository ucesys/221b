package backup

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A file sealed with a passphrase opens with it, and with no other; its
// first line says how it was sealed, and changing that line or a byte of
// the rest is found.
func TestASealedBackupRoundTrip(t *testing.T) {
	r := NewRegistry()
	r.Register(Part{Name: "notes", Label: "Notes", Export: func(context.Context, *Env) (any, error) { return []string{"hello"}, nil }})
	f, _ := r.Export(context.Background(), &Env{Taken: time.Now()})
	gz, _ := f.Gzip()
	pass := "correct horse battery staple"
	sealed, err := Seal(gz, pass)
	if err != nil {
		t.Fatal(err)
	}
	if !Sealed(sealed) || !strings.HasPrefix(string(sealed), "221b-backup-sealed/1 aes-256-gcm scrypt N=32768 r=8 p=1 salt=") || bytes.Contains(sealed, []byte("hello")) {
		t.Fatalf("the sealed file: %q", sealed[:80])
	}
	back, err := Read(sealed, func() (string, error) { return pass, nil })
	if err != nil || compact(back.Parts["notes"]) != `["hello"]` {
		t.Fatalf("%v %v", back, err)
	}
	if _, err := Read(sealed, func() (string, error) { return "another passphrase", nil }); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("another passphrase: %v", err)
	}
	if _, err := Read(sealed, nil); err == nil || !strings.Contains(err.Error(), "encrypted") {
		t.Fatalf("no passphrase: %v", err)
	}
	tampered := bytes.Replace(sealed, []byte("p=1"), []byte("p=2"), 1)
	if _, err := Unseal(tampered, pass); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("a changed first line: %v", err)
	}
	tampered = append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 1
	if _, err := Unseal(tampered, pass); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("a changed byte: %v", err)
	}
	if _, err := Seal(gz, "short"); err == nil {
		t.Fatal("a short passphrase was taken")
	}
}
