// Package scrub removes what looks like a credential from text: tool
// results before they leave for the Claude API, and every audit record.
package scrub

import (
	"encoding/json"
	"regexp"
	"strings"

	"sigs.k8s.io/yaml"
)

// Redacted is what a scrubbed value is replaced with.
const Redacted = "[REDACTED]"

// secretWords are what mark a name as holding a secret, in any case, for
// the patterns below and SecretName alike.
const secretWords = `password|passwd|passphrase|passcode|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credential|auth|dsn|connection[_-]?string|conn[_-]?str|cookie`

const (
	nameChars = `[A-Za-z0-9_.-]*`
	// secretKey is a name that holds a secret, in text. "pass" counts only
	// as a word of its own (db_pass, smtpPass, DB_PASS), so that passed and
	// passive do not.
	secretKey = `(?:(?i:` + nameChars + `(?:` + secretWords + `)` + nameChars + `)|` + nameChars + `(?:[Pp]ass(?:[_.A-Z-]` + nameChars + `)?|PASS(?:[_.-]` + nameChars + `)?))`
	// secretValue is a value after such a name: quoted, quoted inside a
	// JSON string, or up to the next separator.
	secretValue = `(?:\\+".*?\\+"(?:\s*[,}\]]|\s*$)|"[^"]*"|'[^']*'|[^\s,;&}\]]+)`
)

// The shapes of real credentials, whatever words stand around them. Text
// removes them with the rest; Credential looks for them alone.
var (
	// PEM and PGP private keys.
	rePEM = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----.*?(-----END [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----|$)`)
	// JSON Web Tokens, wherever they are.
	reJWT = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{6,}\.[A-Za-z0-9_-]{0,}`)
	// Tokens of well-known formats: AWS access keys, GitHub, Slack, OpenAI
	// and Anthropic API keys, Google API keys, GitLab.
	tokenShapes = []*regexp.Regexp{
		regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
		regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}\b`),
		regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{40,}\b`),
		regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}\b`),
		regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`),
		regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
		regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`),
	}
	// A bearer token written out.
	reBearer = regexp.MustCompile(`(?i)\bbearer\s+([A-Za-z0-9\-._~+/]{20,}=*)`)
	// A password in an address: scheme://user:password@host.
	reURLPassword = regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.-]*://[^\s:/@]*:([^\s"'<>@]+)@`)
	reDigit       = regexp.MustCompile(`[0-9]`)
	reLetter      = regexp.MustCompile(`[A-Za-z]`)
	// What stands in for a password: $VAR, ${VAR}, <password>, {{ .x }},
	// [REDACTED], %s, *** or xxx, or a word for what goes there.
	rePlaceholder = regexp.MustCompile(`(?i)^(?:\$.*|<.*>|\{.*\}|\[.*\]|%.*|\*+|x+|\.+|(?:your|my|the)?[_-]?(?:pass(?:word|wd)?|pwd|secret|token|changeme|example|redacted)[_-]?[a-z0-9]{0,6})$`)
)

// Credential says whether text carries a real credential's shape: a
// private key's PEM block, a JSON Web Token, a token of a well-known
// format, a bearer token written out (twenty characters or more, letters
// and digits), or a password in an address that is not a placeholder.
// Words such as password or token, and key=value pairs, are not enough:
// Text removes those from what tools return, erring towards too much,
// while Credential is for what a model wrote, such as an answer, where
// prose about a password is no leak.
func Credential(s string) bool {
	if rePEM.MatchString(s) || reJWT.MatchString(s) {
		return true
	}
	for _, re := range tokenShapes {
		if re.MatchString(s) {
			return true
		}
	}
	for _, m := range reBearer.FindAllStringSubmatch(s, -1) {
		if reDigit.MatchString(m[1]) && reLetter.MatchString(m[1]) {
			return true
		}
	}
	for _, m := range reURLPassword.FindAllStringSubmatch(s, -1) {
		if !rePlaceholder.MatchString(m[1]) {
			return true
		}
	}
	return false
}

// scrubbers find what looks like a credential in text that leaves for the
// Claude API (P5-03): log lines above all, but every tool result passes
// through them. They err towards removing too much.
var scrubbers = []struct {
	re   *regexp.Regexp
	keep int // the submatch to keep in front of the redaction, 0 for none
}{
	// PEM and PGP private keys.
	{rePEM, 0},
	// Authorization and cookie headers, and bearer tokens.
	{regexp.MustCompile(`(?i)(\b(?:authorization|proxy-authorization|cookie|set-cookie)\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\r\n]+)`), 1},
	{regexp.MustCompile(`(?i)(\b(?:bearer|basic|token)\s+)[A-Za-z0-9\-._~+/]{8,}=*`), 1},
	// JSON Web Tokens, wherever they are.
	{reJWT, 0},
	// Credentials in URLs: scheme://user:password@host. The password runs
	// to the last @ of the address, so it may hold / or @ itself.
	{regexp.MustCompile(`(\b[a-zA-Z][a-zA-Z0-9+.-]*://[^\s:/@]*:)[^\s"'<>]*(@)`), -1},
	// An environment variable's name and value, as text: JSON or YAML.
	{regexp.MustCompile(`(?m)(\\*["']?\bname\\*["']?\s*[:=]\s*\\*["']?` + secretKey + `\\*["']?\s*,?\s*(?:-\s+)?\\*["']?value\\*["']?\s*[:=]\s*)` + secretValue), 1},
	// A command-line flag followed by its value.
	{regexp.MustCompile(`(?m)((?:^|[\s"'\[,])-{1,2}` + secretKey + `["']?(?:\s*,\s*|\s+))` + secretValue), 1},
	// A MySQL client's password, joined to its -p.
	{regexp.MustCompile(`(?m)(\b(?:mysql|mysqldump|mysqladmin|mysqlsh|mariadb|mariadb-dump)\b[^\n]*?\s-p)(?:"[^"]*"|'[^']*'|[^\s"'-]\S*)`), 1},
	// A Secret's literal value, whatever its key is called.
	{regexp.MustCompile(`(--from-literal[= ]\\*["']?[^=\s"'\\]+=)` + secretValue), 1},
	// curl's user:password.
	{regexp.MustCompile(`(\bcurl\b[^\n]*?\s(?:-u\s*|--user[=\s]\s*)\\*["']?[^\s:"'\\]*:)` + secretValue), 1},
	// key=value and key: value pairs whose key names a secret, also inside
	// a JSON string.
	{regexp.MustCompile(`(?m)((?:\\*["'])?` + secretKey + `(?:\\*["'])?\s*[:=]\s*)` + secretValue), 1},
	// Well-known token shapes.
	{tokenShapes[0], 0}, {tokenShapes[1], 0}, {tokenShapes[2], 0}, {tokenShapes[3], 0},
	{tokenShapes[4], 0}, {tokenShapes[5], 0}, {tokenShapes[6], 0},
}

// blockKey is a YAML key that names a secret and whose value is the block
// on the lines below it.
var blockKey = regexp.MustCompile(`(?m)^([ \t]*(?:-[ \t]+)?["']?` + secretKey + `["']?[ \t]*:)[ \t]*[|>][-+0-9]*[ \t]*\r?$`)

// scrubBlocks removes the blocks of such keys: every line under the key
// that is indented further, and the blank ones between them.
func scrubBlocks(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range blockKey.FindAllStringSubmatchIndex(s, -1) {
		if m[0] < last {
			continue
		}
		line := s[m[0]:m[1]]
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		b.WriteString(s[last:m[0]])
		b.WriteString(s[m[2]:m[3]] + " " + Redacted)
		last = m[1]
		for last < len(s) {
			next, _, _ := strings.Cut(s[last+1:], "\n")
			trimmed := strings.TrimLeft(next, " \t")
			if strings.TrimSpace(next) != "" && len(next)-len(trimmed) <= indent {
				break
			}
			last += 1 + len(next)
		}
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// Scrub removes what looks like a credential from text.
func Text(s string) string {
	if s == "" {
		return s
	}
	s = scrubBlocks(s)
	for _, sc := range scrubbers {
		switch sc.keep {
		case 0:
			s = sc.re.ReplaceAllString(s, Redacted)
		case -1:
			s = sc.re.ReplaceAllString(s, "${1}"+Redacted+"${2}")
		default:
			s = sc.re.ReplaceAllStringFunc(s, func(m string) string {
				sub := sc.re.FindStringSubmatch(m)
				if len(sub) <= sc.keep {
					return Redacted
				}
				// A value that is itself already redacted stays as it is:
				// cut at its "]" by a value that ends there, it starts with
				// the redaction's rest, so scrubbing again changes nothing.
				if v := m[len(sub[sc.keep]):]; strings.Contains(v, Redacted) || strings.HasPrefix(v, Redacted[:len(Redacted)-1]) {
					return m
				}
				return sub[sc.keep] + Redacted
			})
		}
	}
	return s
}

// Known also removes values known to be secret, wherever they appear:
// the API key itself, for one.
func Known(s string, known []string) string {
	for _, k := range known {
		if len(k) >= 6 {
			s = strings.ReplaceAll(s, k, Redacted)
		}
	}
	return Text(s)
}

// secretName matches names that look like they hold a secret.
var secretName = regexp.MustCompile(`(?i:` + secretWords + `)|[Pp]ass(?:$|[^a-z])|PASS(?:$|[^A-Z])`)

// SecretName says whether a name looks like it holds a secret: an
// environment variable's, a field's, a Helm value's.
func SecretName(name string) bool { return secretName.MatchString(name) }

// secretFlag is a command-line argument that names a secret and leaves its
// value to the next argument.
var secretFlag = regexp.MustCompile(`^-{1,2}` + secretKey + `$`)

// joinedPassword is a short -p with its value joined to it, as the MySQL
// clients take it. Arguments may stand apart from the command they are for,
// so no client is asked for here.
var joinedPassword = regexp.MustCompile(`^-p[^-=\s]`)

// notSecretFields are fields of the console's own rows whose names look
// secret but whose values are not: counts, flags and references.
var notSecretFields = map[string]bool{"secretsRead": true, "tokens": true, "secretRefs": true, "secrets": true, "secretName": true, "secretKeyRef": true, "secret": true, "authMethod": true}

// JSON scrubs a JSON document value by value: every string is
// scrubbed as text, a string field whose name looks secret is redacted,
// and so is the value of a {"name": …, "value": …} pair (an environment
// variable) whose name does, and the argument after a flag that does. A
// string that is itself JSON, such as a last-applied annotation, is
// scrubbed the same way. Invalid JSON is scrubbed as text.
func JSON(b []byte) []byte {
	var v any
	if err := jsonUnmarshal(b, &v); err != nil {
		return []byte(Text(string(b)))
	}
	v = scrubValue(v, "")
	out, err := jsonMarshal(v)
	if err != nil {
		return []byte(Text(string(b)))
	}
	return out
}

// YAML scrubs a YAML document as JSON does a JSON one, so an
// object reads the same to the model in either form. Text that is not YAML
// is scrubbed as text.
func YAML(b []byte) []byte {
	j, err := yaml.YAMLToJSON(b)
	if err != nil {
		return []byte(Text(string(b)))
	}
	out, err := yaml.JSONToYAML(JSON(j))
	if err != nil {
		return []byte(Text(string(b)))
	}
	return out
}

func scrubValue(v any, field string) any {
	switch x := v.(type) {
	case string:
		if field != "" && SecretName(field) && !notSecretFields[field] && x != "" {
			return Redacted
		}
		return scrubString(x)
	case []any:
		scrubArgs(x)
		for i := range x {
			x[i] = scrubValue(x[i], field)
		}
		return x
	case map[string]any:
		if name, ok := x["name"].(string); ok && SecretName(name) {
			if _, ok := x["value"].(string); ok && x["value"] != "" {
				x["value"] = Redacted
			}
		}
		for k, e := range x {
			x[k] = scrubValue(e, k)
		}
		return x
	}
	return v
}

// scrubString scrubs a string, value by value when it is itself JSON.
func scrubString(s string) string {
	if t := strings.TrimSpace(s); len(t) > 1 && (t[0] == '{' || t[0] == '[') && json.Valid([]byte(t)) {
		var v any
		if jsonUnmarshal([]byte(t), &v) == nil {
			if out, err := jsonMarshal(scrubValue(v, "")); err == nil {
				return string(out)
			}
		}
	}
	return Text(s)
}

// scrubArgs redacts what a list of command-line arguments gives away.
func scrubArgs(list []any) {
	for i, e := range list {
		s, ok := e.(string)
		if !ok {
			continue
		}
		if joinedPassword.MatchString(s) {
			list[i] = Redacted
		}
		if secretFlag.MatchString(s) && i+1 < len(list) {
			if _, ok := list[i+1].(string); ok {
				list[i+1] = Redacted
			}
		}
	}
}
