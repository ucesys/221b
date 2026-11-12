// Package cardid names the proposed changes, the Watson cards (CF-56):
// PC-<n>, as the other records are C-<n> (cases), KI-<n>, EX-<n> and
// F-<n>. Until wave 6 a card was c<n>, too easily read as the case C-<n>;
// those ids keep working: Canon reads either form, and every package that
// keeps a card's id reads it through Canon, so a card stored as c16 shows
// and is found as PC-16.
package cardid

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
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

// --- What a proposed change is about (phase 12 review, W-10, CF-101) ---

// KeyPrefix starts the dedup key of a proposed change an agent made for
// cases and known issues. Keys of before named the job or the case and
// the change's title, which the model writes: a reworded title filed the
// same change again.
const KeyPrefix = "fix:"

// Key is the dedup key of a proposed change: its cluster, the cases and
// known issues it resolves, sorted, and a hash of its steps in their
// order (Step). A newer proposal with the same key replaces the older, so
// the same commands for the same cases wait once, whatever their title
// and whichever job proposed them (ADR-148).
func Key(cluster string, resolves, steps []string) string {
	refs := make([]string, 0, len(resolves))
	for _, r := range resolves {
		if r = strings.ToUpper(strings.TrimSpace(r)); r != "" && !slices.Contains(refs, r) {
			refs = append(refs, r)
		}
	}
	slices.Sort(refs)
	sum := sha256.Sum256([]byte(strings.Join(steps, "\n")))
	return KeyPrefix + strings.TrimSpace(cluster) + ":" + strings.Join(refs, ",") + ":" + hex.EncodeToString(sum[:8])
}

// Step is one action of a proposed change as Key reads it: its kind and
// its command with the spaces between words made one; an action without a
// command by its arguments.
func Step(kind string, args map[string]any) string {
	if cmd, ok := args["command"].(string); ok {
		return kind + " " + strings.Join(strings.Fields(cmd), " ")
	}
	b, _ := json.Marshal(args)
	return kind + " " + string(b)
}
