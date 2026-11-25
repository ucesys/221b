package kube

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// One crafted Secret must not stop the console (kube#1): a release record
// whose manifest holds a Secret value that is not valid UTF-8 made
// MaskNotes compile a regexp that panicked, in the Secret informer's
// transform, which kills the process. The record is read, and the notes
// are masked as any others.
func TestACraftedReleaseRecordDoesNotPanic(t *testing.T) {
	rec := `{"name":"x","info":{"status":"deployed","notes":"x"},"manifest":"kind: Secret\ndata:\n  a: /w==\n"}`
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "sh.helm.release.v1.x.v1", Namespace: "default"},
		Type:       HelmSecretType,
		Data:       map[string][]byte{"release": []byte(rec)},
	}
	out, err := SummarizeSecret(s)
	if err != nil {
		t.Fatal(err)
	}
	sum := out.(*SecretSummary)
	if sum.Release == nil || sum.Release.Name != "x" {
		t.Fatalf("the release was not read: %+v", sum.Release)
	}
	// A short value that is not text stands apart from the words around it.
	for _, c := range []struct {
		s, word string
		want    bool
	}{
		{"a \xff b", "\xff", true},
		{"a\xffb", "\xff", false},
		{"\xff", "\xff", true},
		{"x1", "x1", true},
		{"bx12", "x1", false},
		{"é x1é", "x1", false},
		{"é x1 é", "x1", true},
		{"\xfex1\xfe", "x1", true},
		{"", "x1", false},
	} {
		if got := wholeWord(c.s, c.word); got != c.want {
			t.Errorf("wholeWord(%q, %q) = %v, want %v", c.s, c.word, got, c.want)
		}
	}
}

// A record that makes the decoding panic for any other reason costs that
// record its release, never the console (kube#1).
func TestAPanicInARecordLosesOnlyItsRelease(t *testing.T) {
	old := decodeRelease
	t.Cleanup(func() { decodeRelease = old })
	decodeRelease = func([]byte) (*HelmRelease, error) { panic("a record that breaks the decoder") }
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "sh.helm.release.v1.y.v1", Namespace: "default"},
		Type:       HelmSecretType,
		Data:       map[string][]byte{"release": []byte("fake-not-a-record")},
	}
	out, err := SummarizeSecret(s)
	if err != nil {
		t.Fatal(err)
	}
	sum := out.(*SecretSummary)
	if sum.Release != nil || sum.Name != s.Name || len(sum.Keys) != 1 {
		t.Fatalf("summary %+v", sum)
	}
	if strings.Contains(sum.Keys[0].Key, "fake") {
		t.Fatal("a value reached the summary")
	}
}
