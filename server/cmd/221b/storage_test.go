package main

import (
	"testing"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/sessions"
)

// No ConfigMap the console keeps records in can pass Kubernetes' 1 MiB:
// the limits of each one's keys add up to less (#46).
func TestEveryConfigMapsKeysFitItsBudget(t *testing.T) {
	for obj, keys := range layout {
		total := 0
		for key, l := range limits(obj) {
			if l.Bytes <= 0 {
				t.Errorf("%s: %s has no limit", obj, key)
			}
			total += l.Bytes
		}
		if total > objectBudget {
			t.Errorf("%s: its keys may hold %d KiB, over the %d KiB a ConfigMap has room for", obj, total>>10, objectBudget>>10)
		}
		if len(keys) == 0 {
			t.Errorf("%s holds nothing", obj)
		}
	}
}

// What moves to a ConfigMap of its own is bounded there, and every key a
// store writes is in the layout.
func TestWhatMovesIsBoundedWhereItGoes(t *testing.T) {
	for key, obj := range map[string]string{audit.StoreKey: objAudit, sessions.HistoryKey: objSessions, automation.RunsKey: objAutomationRuns, automation.FactsKey: objAutomationRuns} {
		if limits(obj)[key].Bytes == 0 {
			t.Errorf("%s has no limit in %s", key, obj)
		}
	}
	if audit.DefaultMaxBytes > limits(objAudit)[audit.StoreKey].Bytes {
		t.Error("the audit log trims itself above its ConfigMap's limit")
	}
	if historyMaxBytes > limits(objSessions)[sessions.HistoryKey].Bytes {
		t.Error("the session records trim themselves above their ConfigMap's limit")
	}
}
