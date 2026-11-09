package jobs

import "sync/atomic"

// --- Validation rules (Phase 12, build 14) ---
//
// People read a check id as a validation rule; the screens show what the
// rule means beside its id, from the validation rules as they stand
// (Mycroft › Validation rules), or the built-in catalogues until the
// console has them.

// meaningOf is the editable list's meaning, once the console gives it
// (SetRuleMeaning); nil reads the built-in catalogues.
var meaningOf atomic.Pointer[func(string) string]

// SetRuleMeaning makes RuleMeaning read the validation rules as they stand
// (Mycroft › Validation rules: built-in, edited and added, CF-71).
func SetRuleMeaning(f func(id string) string) {
	if f != nil {
		meaningOf.Store(&f)
	}
}

// RuleMeaning is what a validation rule (a check id) means: "a container
// runs as root"; "" for an id no rule holds, an other.<slug> or a title.
// It reads the validation rules as they stand once the console set them,
// the built-in catalogues before.
func RuleMeaning(id string) string {
	if f := meaningOf.Load(); f != nil {
		return (*f)(id)
	}
	for _, list := range [][]Check{AuditChecks, AlertChecks, DigestChecks} {
		for _, c := range list {
			if c.ID == id {
				return c.Says
			}
		}
	}
	return ""
}
