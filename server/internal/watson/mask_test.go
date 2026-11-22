package watson

import (
	"strings"
	"testing"
)

// DB-74: a command is recorded with the values of its parameters hidden
// and their names kept; what is not a parameter stays as it was typed.
func TestALineIsRecordedWithItsParametersHidden(t *testing.T) {
	for _, c := range []struct{ line, want string }{
		// A saved command's run, as the API writes it.
		{"helm upgrade shop --git https://github.com/example/shop --path chart --ref main -n web --app --set 'db.password=s3cret' --set 'replicas=5'",
			"helm upgrade shop --git https://github.com/example/shop --path chart --ref main -n web --app --set 'db.password=***' --set 'replicas=***'"},
		// A list, an escaped comma, braces and an empty value.
		{`helm upgrade shop -n web --app --set 'a=1,b.c=x\,y,list={p,q},empty='`, `helm upgrade shop -n web --app --set 'a=***,b.c=***,list=***,empty='`},
		// After an =, and the other kinds of --set.
		{"helm upgrade shop -n web --set=token=abc --set-string pin=0042 --set-json 'cfg={\"k\":\"v\",\"n\":1}' --set-literal 'pw=a,b=c'",
			"helm upgrade shop -n web --set='token=***' --set-string 'pin=***' --set-json 'cfg=***' --set-literal 'pw=***'"},
		// A file's path is not a value: it stays, and so do the release
		// and the chart.
		{"helm upgrade shop repo/chart -n web --set-file ca=/tmp/ca.pem -f values-prod.yaml --values more.yaml",
			"helm upgrade shop repo/chart -n web --set-file ca=/tmp/ca.pem -f values-prod.yaml --values more.yaml"},
		// A secret flag's value whole, and kubectl's literals.
		{"kubectl create secret generic db --from-literal=password=hunter2 --from-literal user=ops", "kubectl create secret generic db --from-literal='password=***' --from-literal 'user=***'"},
		{"helm list --password hunter2 --kube-token=abc", "helm list --password '***' --kube-token='***'"},
		// Nothing to hide: the line as it was typed.
		{"kubectl get pods -n web", "kubectl get pods -n web"},
		{`helm upgrade shop -n web --app   --set   "x=1"`, `helm upgrade shop -n web --app --set 'x=***'`},
		// A flag with no value after it.
		{"helm upgrade shop -n web --set", "helm upgrade shop -n web --set"},
		// A line that cannot be read as words: cut before the parameters.
		{"helm upgrade shop -n web --set 'pw=s3cret", "helm upgrade shop -n web ***"},
	} {
		got := MaskLine(c.line)
		if got != c.want {
			t.Errorf("MaskLine(%q)\n got  %q\n want %q", c.line, got, c.want)
		}
		for _, secret := range []string{"s3cret", "hunter2", "0042", "abc"} {
			if strings.Contains(got, secret) {
				t.Errorf("MaskLine(%q) keeps %q", c.line, secret)
			}
		}
		// Hiding again changes nothing.
		if again := MaskLine(got); again != got {
			t.Errorf("MaskLine twice: %q, then %q", got, again)
		}
	}
	if got := MaskDetail(`command="helm upgrade shop -n web --app --set 'pw=s3cret'" kind=change from="Helm1"`); got != `command="helm upgrade shop -n web --app --set 'pw=***'" kind=change from="Helm1"` {
		t.Fatalf("MaskDetail: %s", got)
	}
	for _, d := range []string{`kind=change`, `command="kubectl get pods" kind=read`, `command="unclosed`} {
		if got := MaskDetail(d); got != d {
			t.Fatalf("MaskDetail(%q) = %q", d, got)
		}
	}
}
