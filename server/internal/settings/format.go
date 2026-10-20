package settings

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// The stored format of settings.json (P7-03). The document carries
// "version"; one without it was saved before formats were numbered, and is
// version 0.
//
// To change the format: add a step to migrations that turns a document of
// the last version into the next, working on the raw JSON object so it can
// rename or reshape fields the Settings type no longer has, and add a test
// that a document of the old version loads. FormatVersion follows the
// number of steps. A console upgraded from either of the two previous
// releases must load what they saved (CONTRIBUTING.md).
//
// A document of a newer format than this console knows (after a rollback)
// is read for the fields this one knows, and never saved over: the newer
// fields would be lost. Fields this console does not know in a document it
// may save are written back as they were, so going back and forth between
// releases loses nothing.

// migrations[i] turns a document of version i into version i+1.
var migrations = []func(doc map[string]json.RawMessage) error{
	// 0 → 1: only the version field, which encode writes.
	func(map[string]json.RawMessage) error { return nil },
}

// FormatVersion is the format this console writes.
var FormatVersion = len(migrations)

// versionField is the key of the format version in the document.
const versionField = "version"

// document is a stored settings.json as read.
type document struct {
	settings Settings
	// extra are top-level fields this console does not know, kept to be
	// written back.
	extra   map[string]json.RawMessage
	version int
	newer   bool
}

// decode reads a stored document, migrating an older format.
func decode(b []byte) (document, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return document{}, err
	}
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}
	d := document{}
	if v, ok := raw[versionField]; ok {
		if err := json.Unmarshal(v, &d.version); err != nil || d.version < 0 {
			return document{}, fmt.Errorf("the settings' format version %s is not a number", v)
		}
	}
	if d.version > FormatVersion {
		d.newer = true
	}
	for v := d.version; v < FormatVersion; v++ {
		if err := migrations[v](raw); err != nil {
			return document{}, fmt.Errorf("migrating the settings from format %d: %w", v, err)
		}
	}
	delete(raw, versionField)
	known, _ := json.Marshal(raw)
	if err := json.Unmarshal(known, &d.settings); err != nil {
		return document{}, err
	}
	for k, v := range raw {
		if !knownFields[k] {
			if d.extra == nil {
				d.extra = map[string]json.RawMessage{}
			}
			d.extra[k] = v
		}
	}
	return d, nil
}

// encode writes settings in the current format, with the fields this
// console does not know as they were read.
func encode(s Settings, extra map[string]json.RawMessage) []byte {
	b, _ := json.Marshal(s)
	if len(extra) == 0 && len(b) > 2 {
		// The version first, for whoever reads the ConfigMap by hand.
		return append([]byte(fmt.Sprintf(`{"%s":%d,`, versionField, FormatVersion)), b[1:]...)
	}
	var doc map[string]json.RawMessage
	_ = json.Unmarshal(b, &doc)
	for k, v := range extra {
		if _, ok := doc[k]; !ok && !knownFields[k] {
			doc[k] = v
		}
	}
	doc[versionField] = json.RawMessage(fmt.Sprint(FormatVersion))
	out, _ := json.Marshal(doc)
	return out
}

// knownFields are the JSON names of the Settings fields.
var knownFields = func() map[string]bool {
	out := map[string]bool{versionField: true}
	t := reflect.TypeOf(Settings{})
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
}()
