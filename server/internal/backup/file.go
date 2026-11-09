package backup

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"time"
)

// MaxFile is the most a backup may hold, unzipped.
const MaxFile = 256 << 20

// File is a backup: its header and its parts, by name, as JSON.
type File struct {
	Format  string
	Version string
	Taken   time.Time
	By      string
	// LeftOut names what a backup never holds.
	LeftOut []string
	// Secrets says how the file's secrets are sealed (ADR-137); nil when
	// they are placeholders.
	Secrets *Sealing
	// Parts are the parts the file holds, by name.
	Parts map[string]json.RawMessage
	// order is the parts' order when written; a file read has its parts in
	// name order.
	order []string
}

type fileHeader struct {
	Format  string    `json:"format"`
	Version string    `json:"version"`
	Taken   time.Time `json:"taken"`
	By      string    `json:"by,omitempty"`
	LeftOut []string  `json:"leftOut"`
	Secrets *Sealing  `json:"secrets,omitempty"`
}

// Names are the parts the file holds, in its order.
func (f *File) Names() []string {
	names := slices.Clone(f.order)
	var rest []string
	for k := range f.Parts {
		if !slices.Contains(names, k) {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	return append(names, rest...)
}

// MarshalJSON writes the header, then each part.
func (f File) MarshalJSON() ([]byte, error) {
	h, err := encode(fileHeader{Format: f.Format, Version: f.Version, Taken: f.Taken, By: f.By, LeftOut: f.LeftOut, Secrets: f.Secrets})
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.Write(h[:len(h)-1])
	for _, name := range f.Names() {
		raw := bytes.TrimSpace(f.Parts[name])
		if len(raw) == 0 {
			continue
		}
		k, _ := encode(name)
		b.WriteByte(',')
		b.Write(k)
		b.WriteByte(':')
		b.Write(raw)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// UnmarshalJSON reads the header, and every other key as a part.
func (f *File) UnmarshalJSON(b []byte) error {
	var h fileHeader
	if err := json.Unmarshal(b, &h); err != nil {
		return err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return err
	}
	for _, k := range header {
		delete(all, k)
	}
	*f = File{Format: h.Format, Version: h.Version, Taken: h.Taken, By: h.By, LeftOut: h.LeftOut, Secrets: h.Secrets, Parts: all}
	return nil
}

// CheckFormat says whether a restore takes the file.
func (f *File) CheckFormat() error {
	if f.Format != Format && f.Format != FormatV1 {
		return fmt.Errorf("this is not a 221B backup (format %q, want %q)", f.Format, Format)
	}
	return nil
}

// Name is the file's name: 221b-backup-2026-11-05-030000.json.gz.
func (f *File) Name() string {
	return "221b-backup-" + f.Taken.UTC().Format("2006-01-02-150405") + ".json.gz"
}

// Gzip writes the file as a backup is kept: indented JSON, gzipped.
func (f *File) Gzip() ([]byte, error) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Name, zw.ModTime = f.Name()[:len(f.Name())-len(".gz")], f.Taken
	enc := json.NewEncoder(zw)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return gz.Bytes(), nil
}

// Read reads a backup as kept: sealed (then opened with passphrase, which
// is asked only then), gzipped, or plain JSON as the first format was; and
// checks its format.
func Read(b []byte, passphrase func() (string, error)) (*File, error) {
	if Sealed(b) {
		if passphrase == nil {
			return nil, errors.New("this backup is encrypted, and this console has no passphrase for it")
		}
		p, err := passphrase()
		if err != nil {
			return nil, err
		}
		if b, err = Unseal(b, p); err != nil {
			return nil, err
		}
	}
	if len(b) > 1 && b[0] == 0x1f && b[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, errors.New("this file is gzipped but cannot be read: it is damaged or cut short")
		}
		out, err := io.ReadAll(io.LimitReader(zr, MaxFile+1))
		if err != nil {
			return nil, errors.New("this file is gzipped but cannot be read: it is damaged or cut short")
		}
		if len(out) > MaxFile {
			return nil, fmt.Errorf("the backup is over %d MiB unzipped", MaxFile>>20)
		}
		b = out
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, errors.New("this is not a 221B backup: it is neither gzipped JSON nor JSON")
	}
	if err := f.CheckFormat(); err != nil {
		return nil, err
	}
	return &f, nil
}
