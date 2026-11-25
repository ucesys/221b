package kube

import (
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// A refusal of the settings ConfigMap reads as a sentence (kube-cut-5).
func TestAConfigMapRefusalReadsAsASentence(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "x", errors.New("no"))
	for doing, want := range map[string]string{
		"reading":   "not allowed to read the settings ConfigMap ns/x;",
		"saving to": "not allowed to save to the settings ConfigMap ns/x;",
		"deleting":  "not allowed to delete the settings ConfigMap ns/x;",
		"listing":   "not allowed to list the settings ConfigMap ns/x;",
	} {
		if got := describeConfigMapError(doing, "ns/x", forbidden).Error(); !strings.HasPrefix(got, want) {
			t.Errorf("%s: %q", doing, got)
		}
	}
}
