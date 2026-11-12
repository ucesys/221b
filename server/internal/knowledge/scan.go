package knowledge

import (
	"regexp"
	"strings"
)

// Sources are data, never instructions. A source that tries to direct the
// model reading it is reported to people, with the words that did, and
// read as data all the same: the prompt says so, and the model's output
// is only ever a draft a person approves.

var instructs = regexp.MustCompile(`(?i)\b(?:ignore|disregard|forget|override)\s+(?:all\s+|any\s+|the\s+)*(?:previous|prior|above|earlier|your|these|those|system)\s+(?:instructions|rules|prompts?|directions|guidelines)\b` +
	`|\byou are now\b|\bsystem prompt\b|\bnew instructions\s*:|\b(?:as an ai|as a language model)\b` +
	`|\bdo not (?:tell|inform|show) (?:the )?(?:user|operator|person|administrator)s?\b` +
	`|\b(?:mycroft|sherlock|assistant|agent)s?,?\s+(?:you must|must now|please now|should now)\b` +
	`|\b(?:run|execute)\s+(?:this|the following)\s+(?:command|commands|kubectl|script)\s+(?:now|immediately|without)\b`)

// Instructs returns what in a text tries to give instructions, or "".
func Instructs(text string) string {
	m := instructs.FindStringIndex(text)
	if m == nil {
		return ""
	}
	start, end := max(m[0]-40, 0), min(m[1]+40, len(text))
	for start > 0 && (text[start]&0xC0) == 0x80 {
		start--
	}
	for end < len(text) && (text[end]&0xC0) == 0x80 {
		end++
	}
	return strings.Join(strings.Fields(text[start:end]), " ")
}
