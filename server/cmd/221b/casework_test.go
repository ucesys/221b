package main

import (
	"testing"

	"github.com/ucesys/221b/server/internal/casework"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/lessons"
)

// The cases' settings (Phase 12, wave 6) are bounded in the casework
// ConfigMap beside the lessons' exceptions, all within the object's
// budget; the cases themselves are ConfigMaps of their own, and an RCA is
// its case's (CF-44).
func TestTheCasesHaveRoomInTheirConfigMap(t *testing.T) {
	l := limits(objCasework)
	for _, k := range []string{casework.SettingsKey, casework.ChecksKey, casework.CleanKey, lessons.ExceptionsKey} {
		if l[k].Bytes == 0 {
			t.Errorf("%s is not bounded in the casework ConfigMap", k)
		}
	}
	total := 0
	for _, k := range l {
		total += k.Bytes
	}
	if total > objectBudget {
		t.Errorf("the casework ConfigMap's keys take %d KiB, more than its %d KiB", total>>10, objectBudget>>10)
	}
}

// A case's object holds the issue service's record, the work on the case
// and, since wave 6f, its Sherlock conversation (CF-47): within what one
// ConfigMap has room for.
func TestACaseObjectHasRoomForItsConversationCF47(t *testing.T) {
	if total := issues.MaxCaseBytes + casework.DefaultWorkBytes + casework.DefaultChatBytes; total > objectBudget {
		t.Errorf("a case's keys take %d KiB, more than the %d KiB of a ConfigMap", total>>10, objectBudget>>10)
	}
}
