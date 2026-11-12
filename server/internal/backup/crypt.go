package backup

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/scrypt"
)

// A file encrypted on the console before it leaves for S3 (ADR-82) starts
// with one line that says how, readable to whoever finds it:
//
//	221b-backup-sealed/1 aes-256-gcm scrypt N=32768 r=8 p=1 salt=<base64> nonce=<base64>
//
// then the gzipped backup sealed with AES-256-GCM, under a key derived
// from the passphrase with scrypt, the line itself authenticated with it.
// Its name ends in SealedSuffix.
const (
	sealedMagic = "221b-backup-sealed/1"
	// SealedSuffix ends the name of an encrypted backup.
	SealedSuffix = ".enc"
	// MinPassphrase is the shortest passphrase taken.
	MinPassphrase = 12
	scryptN       = 1 << 15
	scryptR       = 8
	scryptP       = 1
)

// ErrPassphrase is a sealed file the passphrase does not open: another
// passphrase, or a file changed since.
var ErrPassphrase = errors.New("the passphrase does not open this backup, or the file was changed")

// Sealed says whether b is an encrypted backup.
func Sealed(b []byte) bool { return bytes.HasPrefix(b, []byte(sealedMagic+" ")) }

func sealKey(passphrase string, salt []byte, n, r, p int) ([]byte, error) {
	return scrypt.Key([]byte(passphrase), salt, n, r, p, 32)
}

// Seal encrypts a backup with a key derived from passphrase.
func Seal(plain []byte, passphrase string) ([]byte, error) {
	if len(passphrase) < MinPassphrase {
		return nil, fmt.Errorf("the passphrase is shorter than %d characters", MinPassphrase)
	}
	salt, nonce := make([]byte, 16), make([]byte, 12)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	key, err := sealKey(passphrase, salt, scryptN, scryptR, scryptP)
	if err != nil {
		return nil, err
	}
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	line := fmt.Sprintf("%s aes-256-gcm scrypt N=%d r=%d p=%d salt=%s nonce=%s\n", sealedMagic, scryptN, scryptR, scryptP, base64.StdEncoding.EncodeToString(salt), base64.StdEncoding.EncodeToString(nonce))
	return gcm.Seal([]byte(line), nonce, plain, []byte(line)), nil
}

// Unseal opens what Seal made.
func Unseal(b []byte, passphrase string) ([]byte, error) {
	i := bytes.IndexByte(b, '\n')
	if !Sealed(b) || i < 0 || i > 512 {
		return nil, errors.New("this is not an encrypted 221B backup")
	}
	line := string(b[:i+1])
	f := strings.Fields(line)
	if len(f) != 8 || f[1] != "aes-256-gcm" || f[2] != "scrypt" {
		return nil, errors.New("the encrypted backup's first line is not one this console reads")
	}
	var n, r, p int
	var salt64, nonce64 string
	if _, err := fmt.Sscanf(strings.Join(f[3:6], " "), "N=%d r=%d p=%d", &n, &r, &p); err != nil || n < 2 || n > 1<<20 || r < 1 || r > 32 || p < 1 || p > 16 {
		return nil, errors.New("the encrypted backup's scrypt settings are not readable")
	}
	salt64, _ = strings.CutPrefix(f[6], "salt=")
	nonce64, _ = strings.CutPrefix(f[7], "nonce=")
	salt, err1 := base64.StdEncoding.DecodeString(salt64)
	nonce, err2 := base64.StdEncoding.DecodeString(nonce64)
	if err1 != nil || err2 != nil || len(nonce) != 12 || len(salt) < 8 {
		return nil, errors.New("the encrypted backup's salt or nonce is not readable")
	}
	key, err := sealKey(passphrase, salt, n, r, p)
	if err != nil {
		return nil, err
	}
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	out, err := gcm.Open(nil, nonce, b[i+1:], []byte(line))
	if err != nil {
		return nil, ErrPassphrase
	}
	return out, nil
}
