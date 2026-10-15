package sherlock

import (
	"regexp"
	"strings"
)

// Scrubbed is what a scrubbed value is replaced with.
const Scrubbed = "[REDACTED]"

// scrubbers find what looks like a credential in text that leaves for the
// Claude API (P5-03): log lines above all, but every tool result passes
// through them. They err towards removing too much.
var scrubbers = []struct {
	re   *regexp.Regexp
	keep int // the submatch to keep in front of the redaction, 0 for none
}{
	// PEM private keys and certificates' private parts.
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?(-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`), 0},
	// Authorization headers and bearer tokens.
	{regexp.MustCompile(`(?i)(\b(?:authorization|proxy-authorization)\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\r\n]+)`), 1},
	{regexp.MustCompile(`(?i)(\b(?:bearer|basic|token)\s+)[A-Za-z0-9\-._~+/]{8,}=*`), 1},
	// JSON Web Tokens, wherever they are.
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{0,}`), 0},
	// Credentials in URLs: scheme://user:password@host.
	{regexp.MustCompile(`(\b[a-zA-Z][a-zA-Z0-9+.-]*://[^\s:/@]+:)[^\s@/]+(@)`), -1},
	// key=value and key: value pairs whose key names a secret.
	{regexp.MustCompile(`(?i)((?:["']?)[A-Za-z0-9_.-]*(?:password|passwd|pwd|secret|token|api[_-]?key|apikey|access[_-]?key|private[_-]?key|client[_-]?secret|credentials?|auth)[A-Za-z0-9_.-]*["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;&}\]]+)`), 1},
	// Well-known token shapes.
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`), 0},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}\b`), 0},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{40,}\b`), 0},
	{regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}\b`), 0},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`), 0},
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`), 0},
	{regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`), 0},
}

// Scrub removes what looks like a credential from text.
func Scrub(s string) string {
	if s == "" {
		return s
	}
	for _, sc := range scrubbers {
		switch sc.keep {
		case 0:
			s = sc.re.ReplaceAllString(s, Scrubbed)
		case -1:
			s = sc.re.ReplaceAllString(s, "${1}"+Scrubbed+"${2}")
		default:
			s = sc.re.ReplaceAllStringFunc(s, func(m string) string {
				sub := sc.re.FindStringSubmatch(m)
				if len(sub) <= sc.keep {
					return Scrubbed
				}
				// A value that is itself already redacted stays as it is.
				if strings.Contains(m[len(sub[sc.keep]):], Scrubbed) {
					return m
				}
				return sub[sc.keep] + Scrubbed
			})
		}
	}
	return s
}

// ScrubKnown also removes values known to be secret, wherever they appear:
// the API key itself, for one.
func ScrubKnown(s string, known []string) string {
	for _, k := range known {
		if len(k) >= 6 {
			s = strings.ReplaceAll(s, k, Scrubbed)
		}
	}
	return Scrub(s)
}

// secretName says whether a name looks like it holds a secret.
var secretName = regexp.MustCompile(`(?i)(password|passwd|pwd|secret|token|api[_-]?key|apikey|access[_-]?key|private[_-]?key|credential|auth)`)

// notSecretFields are fields of the console's own rows whose names look
// secret but whose values are not: counts, flags and references.
var notSecretFields = map[string]bool{"secretsRead": true, "tokens": true, "secretRefs": true, "secrets": true, "secretName": true, "secretKeyRef": true, "secret": true, "authMethod": true}

// ScrubJSON scrubs a JSON document value by value: every string is
// scrubbed as text, a string field whose name looks secret is redacted,
// and so is the value of a {"name": …, "value": …} pair (an environment
// variable) whose name does. Invalid JSON is scrubbed as text.
func ScrubJSON(b []byte) []byte {
	var v any
	if err := jsonUnmarshal(b, &v); err != nil {
		return []byte(Scrub(string(b)))
	}
	v = scrubValue(v, "")
	out, err := jsonMarshal(v)
	if err != nil {
		return []byte(Scrub(string(b)))
	}
	return out
}

func scrubValue(v any, field string) any {
	switch x := v.(type) {
	case string:
		if field != "" && secretName.MatchString(field) && !notSecretFields[field] && x != "" {
			return Scrubbed
		}
		return Scrub(x)
	case []any:
		for i := range x {
			x[i] = scrubValue(x[i], "")
		}
		return x
	case map[string]any:
		if name, ok := x["name"].(string); ok && secretName.MatchString(name) {
			if _, ok := x["value"].(string); ok && x["value"] != "" {
				x["value"] = Scrubbed
			}
		}
		for k, e := range x {
			x[k] = scrubValue(e, k)
		}
		return x
	}
	return v
}
