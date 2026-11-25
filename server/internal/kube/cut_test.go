package kube

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
)

// Long texts are cut at a character, never inside one (kube-cut-4): an
// event's message, and a client error shown as a warning.
func TestLongTextsAreCutAtACharacter(t *testing.T) {
	long := strings.Repeat("é", maxEventMessage) // two bytes each
	out, _ := stripEvent(&corev1.Event{Message: "x" + long})
	msg := out.(*corev1.Event).Message
	if !utf8.ValidString(msg) || !strings.HasSuffix(msg, "…") || len(msg) > maxEventMessage+len("…") {
		t.Errorf("event message cut to %d bytes, valid %v", len(msg), utf8.ValidString(msg))
	}
	if s := shortError(errors.New("x" + strings.Repeat("ü", 200))); !utf8.ValidString(s) || !strings.HasSuffix(s, "…") {
		t.Errorf("error cut to %q", s)
	}
}
