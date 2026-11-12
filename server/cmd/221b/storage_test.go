package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/ai"
	"github.com/ucesys/221b/server/internal/api"
	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/automation"
	"github.com/ucesys/221b/server/internal/issues"
	"github.com/ucesys/221b/server/internal/knowledge"
	"github.com/ucesys/221b/server/internal/nodessh"
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

// The hosts outside Kubernetes (Phase 12) are bounded beside the pairings,
// and as many typical hosts as a console may keep fit: an ed25519 key
// pinned, another offered, three labels.
func TestTheHostsHaveRoomInTheSettings(t *testing.T) {
	l := limits(objSettings)[nodessh.HostsKey]
	if l.Bytes == 0 {
		t.Fatal("the hosts have no limit in the settings ConfigMap")
	}
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEx1aAoMmc6V6I0/3qbG4sYjDBDMpkK1mwEu+Ztu5tUe"
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	m := map[string]nodessh.Host{}
	for i := range nodessh.MaxHosts {
		name := fmt.Sprintf("backup-server-%03d.example.internal", i)
		m["k8s-production-eu/"+name] = nodessh.Host{Cluster: "k8s-production-eu", Name: name, Address: "10.120.14." + strconv.Itoa(i), Port: 22, LoginUser: "root",
			Labels: map[string]string{"site": "frankfurt-2", "role": "backup", "team": "storage-ops"}, State: nodessh.HostPaired, AddedBy: "Pat Example", AddedAt: at,
			HostKey: key, Fingerprint: "SHA256:7mZ9dQ1yW3Qh3dS2x6Lr2gF0aH0dJk3Xv8tC5nB1pQo", TrustedBy: "Pat Example", TrustedAt: &at, Offered: key, PairedBy: "Pat Example", PairedAt: &at}
	}
	b, _ := json.Marshal(m)
	if len(b) > l.Bytes {
		t.Errorf("%d typical hosts take %d KiB, over their %d KiB", nodessh.MaxHosts, len(b)>>10, l.Bytes>>10)
	}
}

// The AI agents' keys (Phase 12) are each bounded in their own ConfigMap,
// and the instructions trim themselves below their limit.
func TestTheAIKeysHaveRoom(t *testing.T) {
	for _, key := range ai.BackupKeys {
		if limits(objAI)[key].Bytes == 0 {
			t.Errorf("%s has no limit in %s", key, objAI)
		}
	}
	if limits(objAI)[ai.PromptsKey].Bytes < 440<<10 {
		t.Error("the instructions trim themselves above their limit")
	}
}

// The issue service (Phase 12) has a ConfigMap of its own, and fits its
// records to the same limits the ConfigMap's keys have, so it refuses a
// save with its own words before the store would.
func TestTheIssueServiceIsBoundedInItsOwnConfigMap(t *testing.T) {
	l := limits(objIssues)
	for key, want := range map[string]int{issues.IndexKey: issues.DefaultIndexBytes, issues.KnownKey: issues.DefaultKnownBytes, issues.ConfigKey: issues.DefaultConfigBytes} {
		if l[key].Bytes == 0 || l[key].Bytes < want {
			t.Errorf("%s: the ConfigMap keeps %d bytes, the service fits to %d", key, l[key].Bytes, want)
		}
	}
	if lim := issueLimits(); lim.IndexBytes != l[issues.IndexKey].Bytes || lim.KnownBytes != l[issues.KnownKey].Bytes || lim.ConfigBytes != l[issues.ConfigKey].Bytes {
		t.Errorf("the service is not given the layout's limits: %+v", lim)
	}
}

// Getting started's record (Phase 12) is bounded beside the settings, and
// a record with every step dismissed and the first backup fits.
func TestGettingStartedHasRoomInTheSettings(t *testing.T) {
	l := limits(objSettings)[api.SetupKey]
	if l.Bytes == 0 {
		t.Fatal("Getting started has no limit in the settings ConfigMap")
	}
	at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	rec := map[string]any{"firstBackup": map[string]any{"by": strings.Repeat("x", 254), "at": at}}
	dismissed := map[string]any{}
	for _, id := range []string{"signin", "clusters", "provider", "agents", "knowledge", "jobs", "fleets", "hosts", "backup"} {
		dismissed[id] = map[string]any{"by": strings.Repeat("x", 254), "at": at}
	}
	rec["dismissed"] = dismissed
	if b, _ := json.Marshal(rec); len(b) > l.Bytes {
		t.Errorf("a full record takes %d bytes, over its %d", len(b), l.Bytes)
	}
}

// The temporary wiki store (CF-57) has a ConfigMap of its own, bounded to
// what the store refuses past, so its own words come first.
func TestTheWikiStoreIsBoundedInItsOwnConfigMap(t *testing.T) {
	l := limits(objWiki)
	if len(l) != 1 || l[knowledge.WikiKey].Bytes != knowledge.DefaultWikiBytes || knowledge.DefaultWikiBytes != 900<<10 {
		t.Fatalf("the wiki's ConfigMap: %+v", l)
	}
}
