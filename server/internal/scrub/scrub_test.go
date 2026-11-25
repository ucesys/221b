package scrub

import (
	"strings"
	"testing"
)

// Text scrubbed again stays as it is: a case's shared draft is scrubbed
// on every save (CF-59).
func TestScrubTwiceChangesNothing(t *testing.T) {
	for _, in := range []string{"password=hunter2 same as C-4", "token: abc123def, next", `{"api_key":"hunter2"}`, "--db-password hunter2secret --port 80"} {
		once := Text(in)
		if twice := Text(once); twice != once {
			t.Errorf("%q: %q, then %q", in, once, twice)
		}
	}
}

// Each credential form leaves the text, and what stands beside it stays.
func TestScrubRemovesEveryCredentialForm(t *testing.T) {
	for name, c := range map[string]struct{ in, gone, kept string }{
		"a name ending in pass":          {"MYSQL_ROOT_PASS=hunter2secret next=1", "hunter2secret", "next=1"},
		"a short name":                   {"DB_PASS: hunter2secret", "hunter2secret", "DB_PASS"},
		"a camel-case name":              {"smtpPass=hunter2secret", "hunter2secret", "smtpPass"},
		"a passphrase":                   {"passphrase: hunter2secret", "hunter2secret", "passphrase"},
		"a data source name":             {"DSN=user:hunter2secret ok", "hunter2secret", "ok"},
		"a connection string":            {"connectionString: hunter2secret", "hunter2secret", "connectionString"},
		"a pair in JSON text":            {`{"name":"PASS","value":"hunter2secret"}`, "hunter2secret", `"name":"PASS"`},
		"a pair in YAML text":            {"- name: DB_PASS\n  value: hunter2secret\n- name: MODE\n  value: prod\n", "hunter2secret", "value: prod"},
		"a flag with its value apart":    {"run --db-password hunter2secret --port 80", "hunter2secret", "--port 80"},
		"a flag in JSON text":            {`["--db-password","hunter2secret"]`, "hunter2secret", "--db-password"},
		"a JSON string in a JSON log":    {`{"msg":"{\"password\":\"hunter2secret\"}","level":"info"}`, "hunter2secret", `"level":"info"`},
		"a quote inside a JSON string":   {`{"msg":"{\"password\":\"hun\\\"ter2secret\"}"}`, "ter2secret", "password"},
		"a MySQL client's joined value":  {"mysql -h db -uroot -phunter2secret shop", "hunter2secret", "-uroot"},
		"a cookie header":                {"Cookie: session=hunter2secret; theme=dark\nnext line", "hunter2secret", "next line"},
		"a set-cookie header":            {"Set-Cookie: sid=hunter2secret; Path=/\nnext line", "hunter2secret", "next line"},
		"a PGP private key":              {"-----BEGIN PGP PRIVATE KEY BLOCK-----\nhunter2secret\n-----END PGP PRIVATE KEY BLOCK-----\nafter", "hunter2secret", "after"},
		"a YAML block":                   {"password: |\n  hunter2secret\n  line two\nnext: 1\n", "hunter2secret", "next: 1"},
		"a folded YAML block in a list":  {"users:\n- password: >-\n    hunter2secret\n- name: bob\n", "hunter2secret", "name: bob"},
		"a URL password with a slash":    {"https://user:pa/hunter2secret@host/x", "hunter2secret", "@host/x"},
		"a URL password with an at sign": {"postgres://app:p@hunter2secret@db/x", "hunter2secret", "@db/x"},
		"a URL with no user name":        {"redis://:hunter2secret@redis:6379/0", "hunter2secret", "@redis:6379"},
	} {
		t.Run(name, func(t *testing.T) {
			out := Text(c.in)
			if strings.Contains(out, c.gone) {
				t.Errorf("%q kept %q: %q", c.in, c.gone, out)
			}
			if !strings.Contains(out, c.kept) {
				t.Errorf("%q lost %q: %q", c.in, c.kept, out)
			}
		})
	}
}

// Ordinary text stays as it was: words that only contain "pass", a
// MySQL client's port, and addresses with no credentials.
func TestScrubLeavesOrdinaryTextAlone(t *testing.T) {
	for _, s := range []string{
		"tests passed=5 passive: true",
		"mysql -h db -P3306 shop",
		"GET https://example.com/a?b=c 200",
	} {
		if out := Text(s); out != s {
			t.Errorf("%q became %q", s, out)
		}
	}
}

// SecretName is the one test of a name, for the scrubbers and Helm values.
func TestSecretNameTakesPassOnlyAsAWord(t *testing.T) {
	for name, want := range map[string]bool{
		"pass": true, "PASS": true, "DB_PASS": true, "smtp.pass": true, "smtpPass": true, "db_pass_file": true,
		"pwd": true, "passwd": true, "password": true, "passphrase": true, "dsn": true, "connectionString": true, "connection_string": true,
		"passed": false, "passive": false, "PASSED": false, "name": false, "value": false, "url": false, "mode": false,
	} {
		if SecretName(name) != want {
			t.Errorf("SecretName(%q) = %v", name, !want)
		}
	}
}

// A document's values are judged by their names wherever they stand.
func TestScrubJSONJudgesValuesByTheirNames(t *testing.T) {
	out := string(JSON([]byte(`{
		"env":[{"name":"PASS","value":"hunter2one"},{"name":"MODE","value":"prod"}],
		"args":["--db-password","hunter2two","--port","80"],
		"command":["mysql","-uroot","-phunter2three"],
		"passwords":["hunter2four"],
		"metadata":{"annotations":{"kubectl.kubernetes.io/last-applied-configuration":"{\"env\":[{\"name\":\"DB_PASSWORD\",\"value\":\"hunter2five\"}]}\n"}}
	}`)))
	if strings.Contains(out, "hunter2") {
		t.Fatalf("a value stayed: %s", out)
	}
	for _, keep := range []string{`"value":"prod"`, `"--port"`, `"80"`, `"-uroot"`} {
		if !strings.Contains(out, keep) {
			t.Errorf("lost %s: %s", keep, out)
		}
	}
}

// An object as YAML goes through the same path as the same object as JSON.
func TestScrubYAMLTreatsAnObjectAsScrubJSONDoes(t *testing.T) {
	in := `apiVersion: apps/v1
kind: Deployment
metadata:
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: |
      {"spec":{"env":[{"name":"DB_PASSWORD","value":"hunter2one"}]}}
  name: web
spec:
  replicas: 2
  template:
    spec:
      containers:
      - args:
        - --db-password
        - hunter2two
        env:
        - name: DB_PASSWORD
          value: hunter2three
        - name: MODE
          value: prod
        - name: API
          valueFrom:
            secretKeyRef:
              key: password
              name: creds
`
	out := string(YAML([]byte(in)))
	if strings.Contains(out, "hunter2") {
		t.Fatalf("a value stayed:\n%s", out)
	}
	for _, keep := range []string{"name: web", "replicas: 2", "value: prod", "name: DB_PASSWORD", "key: password", "name: creds"} {
		if !strings.Contains(out, keep) {
			t.Errorf("lost %q:\n%s", keep, out)
		}
	}
	if got := string(YAML([]byte("{ not yaml: [\npassword: hunter2four"))); strings.Contains(got, "hunter2") {
		t.Errorf("text that is not YAML kept a value: %s", got)
	}
}

// Credential finds a credential's shape and nothing else: prose about a
// password, a placeholder or a key=value pair is not one (AI-49).
func TestCredentialIsTheShapeOfOne(t *testing.T) {
	for _, in := range []string{
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.sig",
		"AKIAIOSFODNN7EXAMPLE", "ghp_abcdefghijklmnopqrstuvwxyz0123456789", "glpat-abcdefghij0123456789",
		"sk-ant-api03-abcdefghijklmnop0123", "xoxb-1234567890-abcdef",
		"Authorization: Bearer 9f8e7d6c5b4a39281706f5e4d3c2b1a0",
		"https://ci:hunter2x@registry.example.com/v2/",
	} {
		if !Credential(in) {
			t.Errorf("not found: %q", in)
		}
	}
	for _, in := range []string{
		"**About the password:** it isn't in the script.", "this service account's token controls the whole cluster",
		"password=$REGISTRY_PASSWORD", "token: abc123def", "--password-stdin", "Bearer authentication",
		"Authorization: Bearer $TOKEN", "postgres://app:${DB_PASSWORD}@db/app", "postgres://app:<password>@db/app",
		"https://user:password@host/", "mysql://root:***@db", "the secret key: never written", "",
	} {
		if Credential(in) {
			t.Errorf("found in %q", in)
		}
	}
}

// Credential's rules each catch their credential and leave what only
// looks like one: the findings of the code review of November 25, 2026,
// by ID. Every secret here is a fake.
func TestCredentialFindsTheReviewedForms(t *testing.T) {
	for name, c := range map[string]struct {
		in   string
		want bool
	}{
		// scrub#1: an HTTP Basic header carries user:password in base64.
		"#1 a basic header":              {`curl -H "Authorization: Basic Y2k6UGxhbnRlZFBhc3MtNzdReg==" https://registry.example.com/v2/`, true},
		"#1 a short basic value":         {"basic Y2k6cHcxMjM0NQ==", true},
		"#1 basic authentication, prose": {"Basic authentication sends the user and password in a header.", false},
		"#1 basic of a placeholder":      {"Authorization: Basic dXNlcjokUEFTUw==", false},
		"#1 basic of the word password":  {"Authorization: Basic dXNlcjpwYXNzd29yZA==", false},
		// scrub#2: a real password that starts like a placeholder word, or
		// with a percent-encoded character.
		"#2 Passw0rd":                 {"Its address is postgres://app:Passw0rd@db:5432/app.", true},
		"#2 Secret123":                {"mysql://root:Secret123@db/shop", true},
		"#2 password2024":             {"postgres://app:password2024@db/app", true},
		"#2 changeme1":                {"postgres://app:changeme1@db/app", true},
		"#2 a percent-encoded start":  {"postgres://app:%23Planted-77Qz@db/app", true},
		"#2 a dollar without a name":  {"postgres://app:$%21x9@db/app", true},
		"#2 a format verb":            {"postgres://app:%s@db/app", false},
		"#2 a Python format":          {"postgres://app:%(password)s@db/app", false},
		"#2 a Windows variable":       {"postgres://app:%DB_PASS%@db/app", false},
		"#2 a shell variable":         {"postgres://app:$PGPASSWORD@db/app", false},
		"#2 a variable with default":  {"postgres://app:${PGPASSWORD:-none}@db/app", false},
		"#2 a placeholder word":       {"postgres://app:your_password@db/app", false},
		"#2 a placeholder word, here": {"postgres://app:password_here@db/app", false},
		"#2 changeme":                 {"postgres://app:changeme@db/app", false},
		// scrub#3: a URL password holding @ is judged whole.
		"#3 an at sign after a word": {"postgres://app:Password@123@db:5432/app", true},
		"#3 two at signs":            {"mysql://root:Pass@w0rd!@db/x", true},
		"#3 no user name":            {"redis://:Secret@2024@redis:6379/0", true},
		"#3 a variable, then an at":  {"redis://:$REDIS_PASSWORD@redis:6379/0 mail ops@example.com", false},
		// scrub#5: a host's port, then an @ further on, is no password.
		"#5 an image digest":        {"helm pull oci://registry.example.com:5000/charts/app@sha256:4f9a2c1e7b3d", false},
		"#5 PromQL's at":            {"http://prometheus:9090/api/v1/query?query=up@1700000000", false},
		"#5 a Go module path":       {"curl http://athens:3000/github.com/gin-gonic/gin/@v/list", false},
		"#5 an e-mail in the query": {"http://keycloak:8080/admin/realms/master/users?email=alice@example.com", false},
		"#5 a port in a query":      {"http://keycloak:8080?email=alice@example.com", false},
		"#5 a password with a hash": {"postgres://app:Pa#ss-41x@db/app", true},
		"#5 a password with slash":  {"https://ci:pa/Fake9secret@registry.example.com/v2/", true},
		"#5 digits, then a path":    {"https://ci:Fake9/secret@registry.example.com/v2/", true},
		"#5 an address in the path": {"http://gw:8080/login?next=https://ci:Fake9secret@registry.example.com/v2/", true},
		// scrub#6: what people write for a password that is not there.
		"#6 a variable's name":      {"Set DATABASE_URL=postgres://app:YOUR_DB_PASSWORD@db:5432/app", false},
		"#6 a token's name":         {"https://x-access-token:GITHUB_TOKEN@github.com/org/repo.git", false},
		"#6 PAT":                    {"https://ci:PAT@dev.azure.com/org/_git/repo", false},
		"#6 API_KEY":                {"https://api:API_KEY@api.example.com/", false},
		"#6 CHANGE_ME":              {"postgres://app:CHANGE_ME@db/app", false},
		"#6 replace-me":             {"postgres://app:replace-me@db/app", false},
		"#6 an ellipsis":            {"postgres://app:…@db/app", false},
		"#6 an escaped variable":    {`DATABASE_URL=postgres://app:\${DB_PASSWORD}@db/app`, false},
		"#6 escaped stars":          {`postgres://app:\*\*\*@db/app`, false},
		"#6 a name with a number":   {"postgres://app:ADMIN_PASS_2024@db/app", true},
		"#6 capitals, run together": {"postgres://app:SUPERPASS99@db/app", true},
		"#6 an escaped real one":    {`postgres://app:\Fake9pass@db/app`, true},
		"#6 changeme with more":     {"postgres://app:changeme-41x@db/app", true},
	} {
		if got := Credential(c.in); got != c.want {
			t.Errorf("%s: Credential(%q) = %v", name, c.in, got)
		}
	}
}
