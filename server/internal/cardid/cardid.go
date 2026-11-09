// Package cardid names the proposed changes, the Watson cards (CF-56):
// PC-<n>, as the other records are C-<n> (cases), KI-<n>, EX-<n> and
// F-<n>. Until wave 6 a card was c<n>, too easily read as the case C-<n>;
// those ids keep working: Canon reads either form, and every package that
// keeps a card's id reads it through Canon, so a card stored as c16 shows
// and is found as PC-16.
package cardid

import (
	"regexp"
	"strconv"
	"strings"
)

// Prefix starts every card's id.
const Prefix = "PC-"

// pattern is a card's id in any form: PC-16, pc16, and the c16 of before.
// C-16, with the hyphen, is a case, never a card.
var pattern = regexp.MustCompile(`^(?i:pc-?|c)([1-9][0-9]{0,8})$`)

// Of is the id of the card numbered n.
func Of(n int) string { return Prefix + strconv.Itoa(n) }

// Canon is a card's id written the one way: "c16", "C16", "pc-16",
// "PC16" and "PC-16" are all "PC-16". Anything else comes back as it was,
// trimmed.
func Canon(id string) string {
	id = strings.TrimSpace(id)
	if m := pattern.FindStringSubmatch(id); m != nil {
		return Prefix + m[1]
	}
	return id
}

// Is says whether s names a card, in either form.
func Is(s string) bool { return pattern.MatchString(strings.TrimSpace(s)) }

// Same says whether two ids name the same card.
func Same(a, b string) bool { return Canon(a) == Canon(b) }
