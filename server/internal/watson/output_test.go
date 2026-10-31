package watson

import (
	"errors"
	"strings"
	"testing"
)

// kubectl reads the file a -o format names in the server's own process,
// where the console's token is, so neither read that takes -o accepts one.
func TestOutputFormatsNeverReadAFile(t *testing.T) {
	for _, verb := range []string{"kubectl events -A", "kubectl get pods -A"} {
		for _, format := range []string{"go-template-file=/var/run/secrets/kubernetes.io/serviceaccount/token", "jsonpath-file=/proc/self/environ", "templatefile=/etc/hostname", "template=x", "go-template=x"} {
			line := verb + " -o " + format
			_, err := Parse(line)
			var r *Refusal
			if !errors.As(err, &r) || !strings.Contains(r.Message, "Watson prints") {
				t.Errorf("%s: %v", line, err)
			}
		}
	}
	if _, err := Parse("kubectl events -n web -o json"); err != nil {
		t.Errorf("-o json: %v", err)
	}
}
