package sherlock

import (
	"strings"
	"testing"
	"time"
)

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
			out := Scrub(c.in)
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
		if out := Scrub(s); out != s {
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
	out := string(ScrubJSON([]byte(`{
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
	out := string(ScrubYAML([]byte(in)))
	if strings.Contains(out, "hunter2") {
		t.Fatalf("a value stayed:\n%s", out)
	}
	for _, keep := range []string{"name: web", "replicas: 2", "value: prod", "name: DB_PASSWORD", "key: password", "name: creds"} {
		if !strings.Contains(out, keep) {
			t.Errorf("lost %q:\n%s", keep, out)
		}
	}
	if got := string(ScrubYAML([]byte("{ not yaml: [\npassword: hunter2four"))); strings.Contains(got, "hunter2") {
		t.Errorf("text that is not YAML kept a value: %s", got)
	}
}

// The rule that text from the cluster is data holds with view changes on or
// off, and with the administrators' own instructions.
func TestClusterTextIsDataInEveryPrompt(t *testing.T) {
	for name, q := range map[string]Question{
		"view changes on":           {Cluster: "lab", ViewChanges: true},
		"view changes off":          {Cluster: "lab"},
		"their own instructions":    {Cluster: "lab", Instructions: "Answer in French."},
		"instructions that have it": {Cluster: "lab", Instructions: "Answer in French.\n" + clusterTextRule},
	} {
		core, _ := PromptText(q, time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC))
		if n := strings.Count(core, clusterTextRule); n != 1 {
			t.Errorf("%s: the rule is there %d times:\n%s", name, n, core)
		}
	}
}
