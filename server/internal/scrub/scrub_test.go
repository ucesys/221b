package scrub

import (
	"strings"
	"testing"
)

// Text scrubbed again stays as it is: a case's shared draft is scrubbed
// on every save (CF-59).
func TestScrubTwiceChangesNothing(t *testing.T) {
	for _, in := range []string{"password=hunter2 same as C-4", "token: abc123def, next", `{"api_key":"hunter2"}`, "--db-password hunter2secret --port 80",
		"auth:\n  rootPassword: F4keRootPw\n", `{"auth": {"password": "hunter2secret"}}`, `{"passwords": ["hunter2secret"]}`, "password:\n  hunter2secret\n"} {
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
		// scrub#10: a pair under a secret-named parent key.
		"#10 a child of a secret-named key": {"auth:\n  rootPassword: F4keRootPw\n  database: app\n", "F4keRootPw", "database: app"},
		"#10 an object under such a key":    {`{"auth": {"password": "hunter2secret"}, "db": "app"}`, "hunter2secret", `"db": "app"`},
		"#10 an object in a JSON string":    {`{"msg":"{\"auth\":{\"password\":\"hunter2secret\"}}","level":"info"}`, "hunter2secret", `"level":"info"`},
		"#10 a list under such a key":       {`{"passwords": ["hunter2secret", "hunter3"], "db": "app"}`, "hunter2secret", `"db": "app"`},
		"#10 a value on the next line":      {"password:\n  hunter2secret\nnext: 1\n", "hunter2secret", "next: 1"},
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
		"auth:\n  enabled: true\n  database: app\n", // scrub#10
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
		// scrub#7: a private key's header alone, or around a placeholder, is
		// no key; a header with key material after it is.
		"#7 headers named":           {"The tls.key must begin with `-----BEGIN PRIVATE KEY-----`, but yours begins with `-----BEGIN CERTIFICATE-----`.", false},
		"#7 a template":              {"tls.key: |\n  -----BEGIN PRIVATE KEY-----\n  <your key here>\n  -----END PRIVATE KEY-----", false},
		"#7 an elided key":           {"-----BEGIN PRIVATE KEY-----\n...\n-----END PRIVATE KEY-----", false},
		"#7 a header in prose":       {"-----BEGIN PRIVATE KEY----- followed by the key", false},
		"#7 an indented key":         {"tls.key: |\n  -----BEGIN PRIVATE KEY-----\n  MIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n", true},
		"#7 a key in a JSON string":  {`{"key":"-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n-----END PRIVATE KEY-----\n"}`, true},
		"#7 a PGP key with headers":  {"-----BEGIN PGP PRIVATE KEY BLOCK-----\nVersion: GnuPG v2\n\nlQOYBFakeKeyMaterial0\n-----END PGP PRIVATE KEY BLOCK-----", true},
		"#7 an encrypted RSA key":    {"-----BEGIN RSA PRIVATE KEY-----\nProc-Type: 4,ENCRYPTED\nDEK-Info: AES-128-CBC,0123ABCD\n\nMIIEowFakeKeyMaterial\n-----END RSA PRIVATE KEY-----", true},
		"#7 a key on one line":       {"-----BEGIN PRIVATE KEY----- MIIEvQIBADANBgkqhkiG9w0BAQEFAASC -----END PRIVATE KEY-----", true},
		"#7 a word on the next line": {"-----BEGIN PRIVATE KEY-----\nREDACTED\n-----END PRIVATE KEY-----", false},
		// scrub#8: a scheme and its token stand on one line.
		"#8 bearer at a line's end": {"kubelet | Bearer\nkube-state-metrics-6f8b7c9d4-x2v7q | none", false},
		"#8 basic at a line's end":  {"proxy | Basic\nY2k6cHcxMjM0NQ== | none", false},
		"#8 bearer and a tab":       {"Authorization: Bearer\t9f8e7d6c5b4a39281706f5e4d3c2b1a0", true},
		// scrub#9: a token cut short, or a placeholder where one goes.
		"#9 an elided JWT":           {`curl -k -H "Authorization: Bearer eyJhbGciOiJSUzI1NiIsImtpZCI6..." https://kubernetes.default.svc/api`, false},
		"#9 an elided JWT, one char": {"Bearer eyJhbGciOiJSUzI1NiIs…", false},
		"#9 a whole JWT":             {"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.c2lnbmF0dXJl", true},
		"#9 sk- placeholder":         {"OPENAI_API_KEY=sk-your-api-key-goes-here", false},
		"#9 xoxb- placeholder":       {"SLACK_BOT_TOKEN=xoxb-your-bot-token-here", false},
		"#9 ghp_ and X's":            {"GITHUB_TOKEN=ghp_XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX", false},
		"#9 AKIA and X's":            {"AWS_ACCESS_KEY_ID=AKIAXXXXXXXXXXXXXXXX", false},
		"#9 bearer placeholder":      {"Authorization: Bearer YOUR_ACCESS_TOKEN_V2", false},
		"#9 bearer of X's":           {"Authorization: Bearer xxxxxxxxxxxxxxxxxxxxxxxx", false},
		"#9 sk- with a real body":    {"OPENAI_API_KEY=sk-proj-abcdefghijklmnopqrstuvwx0123", true},
		"#9 your inside a token":     {"glpat-yourabcdefghij0123456789", true},
		"#9 a bearer token":          {"Authorization: Bearer 9f8e7d6c5b4a39281706f5e4d3c2b1a0", true},
		// scrub#4: a literal secret in a script or manifest, by its key,
		// flag or header, when it is no placeholder, has ten characters or
		// more and holds a letter and a digit.
		"#4 a flag and its value":        {"docker login -u ci --password 'Fake2Planted!' registry.example.com", true},
		"#4 a flag with =":               {"run --db-password=Fake2Planted! now", true},
		"#4 docker login -p":             {"docker login -u ci -p Fake2Planted! registry.example.com", true},
		"#4 from-literal":                {"kubectl create secret generic db --from-literal=password=F4ke-Pa55word", true},
		"#4 from-literal, any key":       {"kubectl create secret generic db --from-literal=db=F4ke-Pa55word", true},
		"#4 base64 under data":           {"data:\n  password: RmFrZTJQbGFudGVkIQ==", true},
		"#4 a key in JSON":               {`{"client_secret": "f4ke0123456789abcdef"}`, true},
		"#4 a key in a JSON string":      {`{"msg":"{\"api_key\":\"F4keApiKey0123\"}"}`, true},
		"#4 an environment variable":     {"export DB_PASSWORD=F4ke-Pa55word", true},
		"#4 a camel-case key":            {"smtpPass: F4ke-Pa55word", true},
		"#4 an env pair in YAML":         {"- name: DB_PASSWORD\n  value: F4ke-Pa55word", true},
		"#4 an env pair in JSON":         {`{"name":"API_TOKEN","value":"F4keToken0123"}`, true},
		"#4 mysql -p":                    {"mysql -h db -uroot -pF4ke-Pa55word shop", true},
		"#4 curl -u":                     {"curl -u admin:F4ke-Pa55word https://hooks.example.com/", true},
		"#4 a cookie":                    {"Cookie: theme=dark; sid=F4keSession0123", true},
		"#4 an authorization header":     {"Authorization: F4keRawToken0123456", true},
		"#4 a token scheme":              {`-H "Authorization: Token f4ke0123456789abcdef"`, true},
		"#4 a variable after a key":      {`password="$REGISTRY_PASSWORD"`, false},
		"#4 a short value":               {"token: abc123def", false},
		"#4 password-stdin":              {"docker login --username ci --password-stdin registry2.example.com", false},
		"#4 a password file":             {"--password-file /run/secrets/db2", false},
		"#4 a word as the value":         {"password: required-for-login", false},
		"#4 prose after the key":         {"**About the password:** it isn't in the script.", false},
		"#4 a template":                  {`password: "{{ .Values.db.password }}"`, false},
		"#4 a secret's name":             {"secretName: tls-secret-2024", false},
		"#4 an existing secret":          {"existingSecret: db-secret-2024", false},
		"#4 a secret reference":          {"- name: DB_PASSWORD\n  valueFrom:\n    secretKeyRef:\n      name: db-creds-v2\n      key: password", false},
		"#4 bypass":                      {"bypass: route-2024-west", false},
		"#4 a uid and gid":               {"docker run -u 1000:1000 image", false},
		"#4 a cookie of a variable":      {"Cookie: sid=$SESSION_ID", false},
		"#4 a header of a variable":      {`Authorization: "$API_TOKEN"`, false},
		"#4 the key on the line before":  {"password:\n  rotated2024-11 last week", false},
		"#4 from-literal of a variable":  {`--from-literal=password="$DB_PASSWORD"`, false},
		"#4 a key whose value is a path": {"tokenPath: /var/run/secrets/token2", false},
		"#4 code as the value":           {`{"auth": "token", "token": sys.argv[1]}`, false},
		"#4 a call as the value":         {"password = os.getenv('DB_PASSWORD2')", false},
		"#4 a dotted real one":           {"password = S3cr3t.Pa55w0rd", true},
		"#4 a quoted one with brackets":  {`password: "Pa[ss]w0rd-x"`, true},
	} {
		if got := Credential(c.in); got != c.want {
			t.Errorf("%s: Credential(%q) = %v", name, c.in, got)
		}
	}
}

// A list of arguments gives a password away after -u, --user and
// --from-literal too, apart from the flag or joined to it (scrub#11).
func TestScrubJSONTakesAPasswordAfterItsFlag(t *testing.T) {
	for name, c := range map[string]struct{ in, gone, kept string }{
		"-u apart":                  {`{"args":["-u","admin:F4keSecret","https://hooks.example.com/backup"]}`, "F4keSecret", `"admin:[REDACTED]","https://hooks.example.com/backup"`},
		"curl --user apart":         {`{"command":["curl","--user","admin:F4keSecret","https://x"]}`, "F4keSecret", `"admin:[REDACTED]"`},
		"--user joined":             {`{"args":["--user=admin:F4keSecret"]}`, "F4keSecret", `"--user=admin:[REDACTED]"`},
		"-u joined":                 {`{"args":["-uadmin:F4keSecret"]}`, "F4keSecret", `"-uadmin:[REDACTED]"`},
		"--proxy-user apart":        {`{"args":["--proxy-user","admin:F4keSecret"]}`, "F4keSecret", `"admin:[REDACTED]"`},
		"--from-literal apart":      {`{"args":["--from-literal","db=F4keSecret"]}`, "F4keSecret", `"db=[REDACTED]"`},
		"--from-literal joined":     {`{"args":["--from-literal=db=F4keSecret"]}`, "F4keSecret", `--from-literal=db=`},
		"-u with no password":       {`{"args":["-u","1000","-uroot"]}`, "[REDACTED]", `"-u","1000","-uroot"`},
		"--from-literal, no value":  {`{"args":["--from-literal","x"]}`, "[REDACTED]", `"x"`},
		"a user name and a uid:gid": {`{"args":["--user","1000"]}`, "[REDACTED]", `"--user","1000"`},
	} {
		if out := string(JSON([]byte(c.in))); strings.Contains(out, c.gone) || !strings.Contains(out, c.kept) {
			t.Errorf("%s: %s became %s", name, c.in, out)
		}
	}
	in := "spec:\n  containers:\n  - command: [\"curl\", \"-u\", \"admin:F4keSecret\", \"https://hooks.example.com/backup\"]\n"
	if out := string(YAML([]byte(in))); strings.Contains(out, "F4keSecret") || !strings.Contains(out, "admin:") {
		t.Errorf("YAML: %s", out)
	}
}
