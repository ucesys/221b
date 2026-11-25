package watson

import (
	"slices"
	"strconv"
	"strings"
)

// Hidden is what stands for a parameter's value where a command is
// recorded (DB-74).
const Hidden = "***"

// The flags whose value is a parameter, key=value: helm's --set and its
// kinds, and kubectl's --from-literal. Of each the key stays and the value
// is hidden. --set-literal and --from-literal take one key=value each,
// whatever the value holds; the others take a list, a=b,c=d.
var paramFlags = map[string]bool{"--set": false, "--set-string": false, "--set-json": false, "--set-literal": true, "--from-literal": true}

// The flags whose whole value is a secret.
var secretFlags = map[string]bool{"--password": true, "--token": true, "--kube-token": true}

// MaskLine is a command as it is recorded, in the audit log and the
// console's log (DB-74): the value of every parameter hidden, its name
// kept, so the record still says which command ran with which parameters.
// A parameter is what helm's --set, --set-string, --set-json and
// --set-literal and kubectl's --from-literal take, key=value, written
// apart from the flag or after an =; a list (a=b,c=d) hides each value.
// --set-file, -f and --values name a file, whose path stays; the release,
// the chart and the other words are not parameters, and stay. The value
// of --password, --token and --kube-token is hidden whole.
// A line with nothing to hide is returned as it is; one that cannot be
// read as words (a quote not closed) is cut before its first such flag.
func MaskLine(line string) string {
	// A comment is not part of the command, and can follow a flag whose
	// value it swallowed (--token #value): its text is hidden.
	if bare := stripComment(line); bare != line {
		if strings.TrimSpace(bare) == "" {
			return "# " + Hidden
		}
		return strings.TrimRight(MaskLine(bare), " ") + " # " + Hidden
	}
	words, err := split(line)
	if err != nil {
		return cutAtParams(line)
	}
	changed := false
	plain := make([]bool, len(words))
	// kubectl patch's patch can hold anything it sets, a Secret's data
	// included: hidden, unless it is the claim size Watson patches.
	patchLine := words[0] == "kubectl" && slices.Contains(words, "patch")
	for i := 0; i < len(words); i++ {
		w := words[i]
		name, value, joined := strings.Cut(w, "=")
		literal, param := paramFlags[name]
		secret := secretFlags[name]
		if patchLine && (name == "--patch" || name == "-p") {
			v := value
			if !joined && i+1 < len(words) {
				v = words[i+1]
			}
			if _, err := ClaimSize(v); err != nil && v != Hidden {
				secret = true
			}
		}
		if !param && !secret {
			continue
		}
		if !joined {
			if i+1 >= len(words) {
				continue
			}
			i++
			value = words[i]
		}
		masked := Hidden
		if param {
			masked = maskParams(value, literal)
		}
		if masked == value {
			continue
		}
		changed = true
		if joined {
			// The flag stays as typed, the value in quotes after it.
			words[i] = name + "=" + quoteHidden(masked)
			plain[i] = true
		} else {
			words[i] = quoteHidden(masked)
			plain[i] = true
		}
	}
	if !changed {
		return line
	}
	for i, w := range words {
		if !plain[i] {
			words[i] = shellQuote(w)
		}
	}
	return strings.Join(words, " ")
}

// quoteHidden writes a value with its parameters hidden in single quotes,
// as the API writes a saved command's parameters.
func quoteHidden(v string) string {
	if strings.Contains(v, "'") {
		return shellQuote(v)
	}
	return "'" + v + "'"
}

// MaskDetail is an audit event's detail as Watson writes it,
// command="<line>" and what follows, with the line's parameters hidden
// (MaskLine): what a start does to the events recorded before DB-74. A
// detail of another shape is returned as it is.
func MaskDetail(detail string) string {
	rest, ok := strings.CutPrefix(detail, "command=")
	if !ok {
		return detail
	}
	q, err := strconv.QuotedPrefix(rest)
	if err != nil {
		return detail
	}
	line, err := strconv.Unquote(q)
	if err != nil {
		return detail
	}
	masked := MaskLine(line)
	if masked == line {
		return detail
	}
	return "command=" + strconv.Quote(masked) + rest[len(q):]
}

// maskParams hides the values of key=value parameters: one when literal,
// else a list split at the commas helm splits at (not inside {…} or [...],
// nor after a backslash).
func maskParams(v string, literal bool) string {
	if literal {
		return maskOne(v)
	}
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(v); i++ {
		switch v[i] {
		case '\\':
			i++
		case '{', '[':
			depth++
		case '}', ']':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, v[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, v[start:])
	for i, p := range parts {
		parts[i] = maskOne(p)
	}
	return strings.Join(parts, ",")
}

// maskOne hides the value of one key=value; a key alone stays.
func maskOne(p string) string {
	for i := 0; i < len(p); i++ {
		if p[i] == '\\' {
			i++
			continue
		}
		if p[i] == '=' {
			if i+1 == len(p) {
				return p
			}
			return p[:i+1] + Hidden
		}
	}
	return p
}

// cutAtParams is a line that cannot be read as words, cut before the first
// flag whose value would be hidden.
func cutAtParams(line string) string {
	cut := -1
	for f := range paramFlags {
		if i := strings.Index(line, f); i >= 0 && (cut < 0 || i < cut) {
			cut = i
		}
	}
	for f := range secretFlags {
		if i := strings.Index(line, f); i >= 0 && (cut < 0 || i < cut) {
			cut = i
		}
	}
	if cut < 0 {
		return line
	}
	return strings.TrimRight(line[:cut], " ") + " " + Hidden
}

// shellQuote writes a word so that split reads it back: as it is when it
// holds nothing split acts on, else in single quotes, or double quotes
// when it holds a single quote.
func shellQuote(w string) string {
	safe := w != ""
	for i := 0; i < len(w) && safe; i++ {
		c := w[i]
		safe = c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-_./:=@,+%*", c) >= 0
	}
	switch {
	case safe:
		return w
	case !strings.Contains(w, "'"):
		return "'" + w + "'"
	default:
		return `"` + strings.ReplaceAll(strings.ReplaceAll(w, `\`, `\\`), `"`, `\"`) + `"`
	}
}

// MaskedLike are the LIKE patterns of an audit event's detail that may
// hold a value MaskDetail hides.
var MaskedLike = []string{"%--set%", "%--from-literal%", "%--password%", "%--token%", "%--kube-token%"}

// MaskedImportID is the name the hiding of the parameters in the audit
// events recorded before DB-74 is recorded under, once done.
const MaskedImportID = "audit/watson-parameters"
