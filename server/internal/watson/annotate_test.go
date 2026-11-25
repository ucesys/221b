package watson

import (
	"testing"
)

// kubectl annotate never shows a Secret's annotations, which can hold its
// data: --list is refused on Secrets, and setting one needs --overwrite,
// without which kubectl quotes the value it has (review of November 24,
// 2026, finding 5).
func TestAnnotateNeverListsOrQuotesASecretsAnnotations(t *testing.T) {
	for _, line := range []string{
		"kubectl annotate secret db -n web --list",
		"kubectl annotate secrets -n web -l app=db --list note=x",
		"kubectl annotate secret db -n web kubectl.kubernetes.io/last-applied-configuration=x",
		"kubectl annotate secret,configmap -l app=db -n web note=x",
	} {
		_, err := Parse(line)
		r, ok := err.(*Refusal)
		if !ok || r.Rule != "secret values" {
			t.Errorf("%s: %v", line, err)
		}
	}
	for _, line := range []string{
		"kubectl annotate secret db -n web note=x --overwrite",
		"kubectl annotate secret db -n web kubectl.kubernetes.io/last-applied-configuration-",
		"kubectl annotate configmap web -n web --list",
		"kubectl annotate deploy api -n web note=x",
		"kubectl label secret db -n web --list",
	} {
		if _, err := Parse(line); err != nil {
			t.Errorf("%s: %v", line, err)
		}
	}
}
