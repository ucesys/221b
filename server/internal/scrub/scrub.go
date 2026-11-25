// Package scrub removes what looks like a credential from text: tool
// results before they leave for the Claude API, and every audit record.
package scrub

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

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
	// pairValue is secretValue for a key=value pair: a list of plain
	// values whole, and not an object or a list of them ({" or [{), nor a
	// bare value that ends in : (a child key).
	pairValue = `(?:\\+".*?\\+"(?:\s*[,}\]]|\s*$)|"[^"]*"|'[^']*'|\[[^\[\]{}\n]*\]|[{\[][^"'{\[\s\\,;&}\]][^\s,;&}\]]*|[^\s,;&}\]{\[](?:[^\s,;&}\]]*[^\s,;&}\]:])?)`
)

// The shapes of real credentials, whatever words stand around them. Text
// removes them with the rest; Credential looks for them alone.
var (
	// PEM and PGP private keys.
	rePEM = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----.*?(-----END [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----|$)`)
	// A private key's PEM block with key material after its header, for
	// Credential: on the next line, after armour headers (PGP's Version:,
	// an encrypted key's Proc-Type:), also in a JSON string's \n, or on the
	// same line. rePEM takes a header alone, as Text should.
	rePrivateKey = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----(?:[ \t]*(?:\r?\n|\\(?:r\\)?n)(?:[ \t]*[A-Za-z][A-Za-z-]*:[^\r\n\\]*(?:\r?\n|\\(?:r\\)?n))*(?:\s|\\(?:r\\)?n)*([A-Za-z0-9+/]{6,})|[ \t]+([A-Za-z0-9+/]{20,}))`)
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
	// A bearer token written out, on the scheme's line.
	reBearer = regexp.MustCompile(`(?i)\bbearer[ \t]+([A-Za-z0-9\-._~+/]{20,}=*)`)
	// An HTTP Basic value: user:password in base64.
	reBasic = regexp.MustCompile(`(?i)\bbasic[ \t]+([A-Za-z0-9+/]{4,}={0,2})`)
	// A password in an address: scheme://user:password@host. The password
	// runs to the last @, as in Text, so it may hold @ itself.
	reURLPassword = regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.-]*://[^\s:/@]*:([^\s"'<>]+)@`)
	// What such a match takes for a password when the address has no user
	// but a port: the port, then its path or query up to an @ further on
	// (an image digest, PromQL's @, an e-mail address in the query).
	rePortPath = regexp.MustCompile(`^[0-9]+[/?#]`)
	// A token shape's fixed start, before the part that is the secret.
	reTokenPrefix = regexp.MustCompile(`^(?:AKIA|ASIA|gh[pousr]_|github_pat_|xox[abposr]-|sk-|AIza|glpat-)`)
	// "your" as a word of a token's body: sk-your-api-key-goes-here.
	reYourWord = regexp.MustCompile(`(?i)(?:^|[-_])your(?:[-_]|$)`)
	reDigit    = regexp.MustCompile(`[0-9]`)
	reLetter   = regexp.MustCompile(`[A-Za-z]`)
	// What stands in for a password: $VAR, ${VAR}, $(command), <password>,
	// {{ .x }}, [REDACTED], a format verb (%s, %(password)s) or %VAR%, ***,
	// xxx or …, a variable's name in capitals (YOUR_DB_PASSWORD,
	// GITHUB_TOKEN, PAT), or a word for what goes there (password,
	// your_password, password_here, changeme, REPLACE_ME). A word with more
	// after it (Passw0rd, password2024, ADMIN_PASS_2024) is a password, and
	// so is a value that only starts with $ or % (a percent-encoded
	// character). placeholder removes backslashes first.
	rePlaceholder = regexp.MustCompile(`(?i)^(?:\$\{[^}]*\}|\$\(.*|\$[A-Za-z_][A-Za-z0-9_]*|<.*>|\{.*\}|\[.*\]|%(?:\[[0-9]+\])?[-+# 0]*[0-9]*(?:\.[0-9]+)?[a-z]|%\([A-Za-z_][A-Za-z0-9_]*\)[a-z]|%[A-Za-z_][A-Za-z0-9_]*%|\*+|x+|\.+|…+|` +
		`(?-i:(?:[A-Z][A-Z0-9]*_)*(?:PASSWORD|PASSWD|PASS|PWD|TOKEN|SECRET|KEY|PAT)(?:_[A-Z][A-Z0-9]*)*)|` +
		`(?:your|my|the)?[_-]?(?:pass(?:word|wd)?|pwd|secret|token|change[_-]?me|replace[_-]?me|example|redacted)(?:[_-]?here)?)$`)
)

// Credential says whether text carries a real credential's shape: a
// private key's PEM block with key material in it, a JSON Web Token, a
// token of a well-known format (not one of X's, nor one written for
// "your" key), a bearer token written out (twenty characters or more,
// letters and digits; not a JWT's header cut short, nor a placeholder),
// an HTTP Basic value whose user:password has a password that is not a
// placeholder, a password in an address that is not one, or a literal
// value after a key, flag or header that names a secret (DB_PASSWORD=,
// --password, --from-literal, mysql -p, curl -u, Cookie:, Authorization:)
// when it is no placeholder, has ten characters or more and holds a
// letter and a digit or a symbol. Words such as password or token, and a
// key=value pair whose value is a word, are not enough: Text removes those
// from what tools return, erring towards too much, while Credential is
// for what a model wrote, such as an answer, where prose about a password
// is no leak.
func Credential(s string) bool {
	for _, m := range rePrivateKey.FindAllStringSubmatch(s, -1) {
		if keyMaterial(m[1] + m[2]) {
			return true
		}
	}
	if reJWT.MatchString(s) {
		return true
	}
	for _, re := range tokenShapes {
		for _, m := range re.FindAllString(s, -1) {
			if !elided(reTokenPrefix.ReplaceAllString(m, "")) {
				return true
			}
		}
	}
	for _, m := range reBearer.FindAllStringSubmatch(s, -1) {
		// A whole JWT is reJWT's to judge; its header alone, cut short,
		// is what goes where one does (the console's own token field
		// shows eyJhbGciOiJSUzI1NiIs…).
		if strings.HasPrefix(m[1], "eyJ") || elided(m[1]) || placeholder(m[1]) {
			continue
		}
		if reDigit.MatchString(m[1]) && reLetter.MatchString(m[1]) {
			return true
		}
	}
	for _, m := range reBasic.FindAllStringSubmatch(s, -1) {
		if basicPassword(m[1]) {
			return true
		}
	}
	if urlPassword(s) {
		return true
	}
	for _, r := range valueRules {
		// Each search goes on from where the value started, not from the
		// match's end: a key's value may be the start of the next pair
		// ({"msg":"{\"api_key\":…), and matches would not overlap.
		for pos := 0; pos < len(s); {
			m := r.re.FindStringSubmatchIndex(s[pos:])
			if m == nil {
				break
			}
			name := ""
			if r.name > 0 {
				name = s[pos+m[2*r.name] : pos+m[2*r.name+1]]
			}
			if (r.name == 0 || secretKeyName(name)) && realValue(s[pos+m[2*r.value]:pos+m[2*r.value+1]]) {
				return true
			}
			pos += max(m[2*r.value], 1)
		}
	}
	for _, m := range reAuthorization.FindAllStringSubmatch(s, -1) {
		// Bearer and Basic are judged above.
		if scheme := strings.ToLower(m[1]); scheme != "bearer" && scheme != "basic" && realValue(m[2]) {
			return true
		}
	}
	for _, m := range reCookie.FindAllStringSubmatch(s, -1) {
		for _, c := range strings.Split(m[1], ";") {
			if _, v, ok := strings.Cut(c, "="); ok && realValue(strings.TrimSpace(v)) {
				return true
			}
		}
	}
	return false
}

// credValue is a value as Credential reads it: quoted, quoted inside a
// JSON string, or up to the next separator, on the line it starts on.
const credValue = `(\\*"[^"\n]*?\\*"|'[^'\n]*'|[^\s,;&}\]"'\x60]+)`

// valueRules find a value by what stands before it: a key, a flag, a
// command's option. name is the submatch that must be a secret's name
// (secretKeyName), 0 for none; value is the value's.
var valueRules = []struct {
	re          *regexp.Regexp
	name, value int
}{
	// key=value and key: value, also quoted and inside a JSON string.
	{regexp.MustCompile(`(?:^|[^A-Za-z0-9_.-])\\*["']?([A-Za-z0-9_.-]+)\\*["']?[ \t]*[:=][ \t]*` + credValue), 1, 2},
	// A flag and its value apart.
	{regexp.MustCompile(`(?:^|[\s"'\[,(])(-{1,2}[A-Za-z0-9][A-Za-z0-9_.-]*)[ \t]+` + credValue), 1, 2},
	// An environment variable's name and value: YAML on two lines, or JSON.
	{regexp.MustCompile(`\\*["']?\bname\\*["']?[ \t]*:[ \t]*\\*["']?([A-Za-z0-9_.-]+)\\*["']?[ \t]*(?:,|\r?\n)[ \t]*(?:-[ \t]+)?\\*["']?value\\*["']?[ \t]*:[ \t]*` + credValue), 1, 2},
	// A registry login's -p.
	{regexp.MustCompile(`\b(?:docker|podman|nerdctl|buildah|skopeo|oras|helm)\b[^\n]*?\slogin\b[^\n]*?\s-p[ \t]+` + credValue), 0, 1},
	// A MySQL client's password, joined to its -p.
	{regexp.MustCompile(`\b(?:mysql|mysqldump|mysqladmin|mysqlsh|mariadb|mariadb-dump)\b[^\n]*?\s-p([^\s"'-]\S*)`), 0, 1},
	// A Secret's literal value, whatever its key is called.
	{regexp.MustCompile(`--from-literal[= ]\\*["']?[^=\s"'\\]+=` + credValue), 0, 1},
	// user:password after -u or --user (curl, wget, httpie's -a).
	{regexp.MustCompile(`(?:^|[\s"'])(?:-u|-a|--user|--proxy-user|--auth)[ \t=]*\\*["']?[^\s:"'\\]*:` + credValue), 0, 1},
}

var (
	// An unquoted value that is code: sys.argv[1], os.getenv(…).
	reCode = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*[\[(]`)
	// An Authorization header: its scheme, if any, and its value.
	reAuthorization = regexp.MustCompile(`(?i)\b(?:proxy-)?authorization\\*["']?[ \t]*[:=][ \t]*\\*["']?(?:(bearer|basic|token|apikey|api-key|key|digest|negotiate)[ \t]+)?([^\s"'\\,]+)`)
	// A Cookie or Set-Cookie header's pairs.
	reCookie = regexp.MustCompile(`(?i)\b(?:set-)?cookie\\*["']?[ \t]*:[ \t]*([^\r\n"]+)`)
	// The names of the words a secret's name ends in; pass and pwd also
	// need to stand as a word of their own (bypass and compass do not).
	reSecretEnd = regexp.MustCompile(`(?i)(?:password|passwd|passphrase|passcode|secret|token|api[_.-]?key|access[_.-]?key|secret[_.-]?key|private[_.-]?key|credential|(?:^|[_.-])(?:pass|pwd)|[a-z0-9](?-i:Pass|Pwd)|(?-i:^[A-Z0-9_.-]*(?:PASS|PWD)))$`)
)

// secretKeyName says whether the name of a key or a flag ends in a word
// that names a secret (db_password, client_secret, --token, smtpPass),
// where one that ends otherwise (secretName, tokenPath, --password-stdin)
// names something else, and an existing secret's name is a reference.
func secretKeyName(name string) bool {
	name = strings.TrimLeft(name, "-")
	return reSecretEnd.MatchString(name) && !strings.HasPrefix(strings.ToLower(name), "existing")
}

// realValue says whether a value Credential found by its key is a secret:
// not a placeholder nor code, ten characters or more, with a letter and a digit or
// a symbol such as ! or base64's = (token: abc123def and password:
// required-for-login are not).
func realValue(v string) bool {
	v = strings.ReplaceAll(v, `\`, "")
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		v = v[1 : len(v)-1]
	} else if reCode.MatchString(v) {
		return false
	}
	v = strings.TrimRight(v, ".,;:)")
	return utf8.RuneCountInString(v) >= 10 && reLetter.MatchString(v) && (reDigit.MatchString(v) || strings.ContainsAny(v, "!#%^&*+=?~")) && !placeholder(v)
}

// urlPassword says whether text holds an address with a password that is
// not a placeholder. A port and its path are no password, but an address
// inside that path (a redirect's target) may hold one.
func urlPassword(s string) bool {
	for _, m := range reURLPassword.FindAllStringSubmatch(s, -1) {
		if rePortPath.MatchString(m[1]) {
			if urlPassword(m[1] + "@") {
				return true
			}
			continue
		}
		if !placeholder(m[1]) {
			return true
		}
	}
	return false
}

// elided says whether a token's body stands in for one: all X's, or
// written for a person to fill in ("your" as a word of it).
func elided(body string) bool {
	return strings.Trim(body, "xX") == "" || reYourWord.MatchString(body)
}

// keyMaterial says whether what follows a private key's header is base64
// rather than a word: it holds a digit, + or /, or a capital letter after
// a small one's start (MIIEow), where "followed", "Followed" and
// "REDACTED" do not.
func keyMaterial(m string) bool {
	if strings.ContainsAny(m, "0123456789+/") {
		return true
	}
	return strings.ContainsFunc(m[1:], unicode.IsUpper) && strings.ContainsFunc(m, unicode.IsLower)
}

// placeholder says whether a value stands in for a password, escaped or
// not: a heredoc's \${DB_PASSWORD} and Markdown's \*\*\* are placeholders.
func placeholder(v string) bool {
	return rePlaceholder.MatchString(strings.ReplaceAll(v, `\`, ""))
}

// basicPassword says whether an HTTP Basic value decodes to user:password
// with a password that is not a placeholder.
func basicPassword(v string) bool {
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		if b, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(v, "=")); err != nil {
			return false
		}
	}
	if !utf8.Valid(b) || strings.ContainsFunc(string(b), func(r rune) bool { return !unicode.IsPrint(r) }) {
		return false
	}
	_, pw, ok := strings.Cut(string(b), ":")
	return ok && pw != "" && !placeholder(pw)
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
	// a JSON string. The value is on the key's line, and is no object and
	// no key of its own: under a secret-named parent (auth: in a chart's
	// values, {"auth": {"password": …}}) the child pairs are matched on
	// their own. A list of plain values is taken whole.
	{regexp.MustCompile(`(?m)((?:\\*["'])?` + secretKey + `(?:\\*["'])?[ \t]*[:=][ \t]*)` + pairValue), 1},
	// A YAML plain value on the line below its key.
	{regexp.MustCompile(`(?m)((?:\\*["'])?` + secretKey + `(?:\\*["'])?[ \t]*:[ \t]*\r?\n[ \t]+)(?:"[^"\n]*"|'[^'\n]*'|[^\s:#,;&}\]{\["'][^\s:,;&}\]]*)[ \t]*\r?$`), 1},
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

// userFlag is a flag whose next argument is user:password (curl, wget),
// and joinedUser one with user:password joined to it.
var (
	userFlag   = regexp.MustCompile(`^(?:-u|--user|--proxy-user|-U)$`)
	joinedUser = regexp.MustCompile(`^((?:-u|--user=|--proxy-user=|-U)[^:]*:).+`)
)

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

// scrubArgs redacts what a list of command-line arguments gives away: the
// argument after a flag that names a secret, a joined -p, the password of
// user:password after -u or --user, and the value of --from-literal's
// key=value, apart from the flag or joined to it.
func scrubArgs(list []any) {
	for i, e := range list {
		s, ok := e.(string)
		if !ok {
			continue
		}
		if joinedPassword.MatchString(s) {
			list[i] = Redacted
		}
		if m := joinedUser.FindStringSubmatch(s); m != nil {
			list[i] = m[1] + Redacted
		}
		next, ok := "", i+1 < len(list)
		if ok {
			next, ok = list[i+1].(string)
		}
		if !ok {
			continue
		}
		switch {
		case secretFlag.MatchString(s):
			list[i+1] = Redacted
		case userFlag.MatchString(s):
			if u, _, ok := strings.Cut(next, ":"); ok {
				list[i+1] = u + ":" + Redacted
			}
		case s == "--from-literal":
			if k, _, ok := strings.Cut(next, "="); ok {
				list[i+1] = k + "=" + Redacted
			}
		}
	}
}
