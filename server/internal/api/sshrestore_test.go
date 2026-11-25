package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/ucesys/221b/server/internal/kube"
	"github.com/ucesys/221b/server/internal/kv"
	"github.com/ucesys/221b/server/internal/nodessh"
)

// A restore of SSH pairings keeps the pairings here, and a host's pairing
// comes back only for the host as it is here (finding 14 of the review of
// November 24, 2026: a backup's pairing re-pinned a host's older key and
// changed its port and login user, with no trust event).
func TestARestoreOfPairingsKeepsThoseHereAndTheHostsAsTheyAre(t *testing.T) {
	ctx := context.Background()
	e := hostsSetup(t, "ops@corp.example")
	svc := e.spy.Service
	if code, body := e.do(t, "POST", "/api/v1/hosts", `{"name":"db1","address":"10.0.0.21"}`); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("adding db1: %d %s", code, body)
	}
	here, old := hostKeyLine(t), hostKeyLine(t)
	if _, err := svc.RestoreHost(ctx, nodessh.Host{Cluster: "local", Name: "db2", Address: "10.0.0.22", Port: 22, LoginUser: "root", State: nodessh.HostPaired, HostKey: here}); err != nil {
		t.Fatal(err)
	}
	_, body := e.do(t, "GET", "/api/v1/admin/backup", "")
	var f backupFile
	if err := json.Unmarshal([]byte(unzip(t, body)), &f); err != nil {
		t.Fatal(err)
	}
	pairing := func(name, key string, port int, login string) string {
		return `"local/` + name + `":{"cluster":"local","node":"` + name + `","loginUser":"` + login + `","port":` + strconv.Itoa(port) + `,"hostKeys":["` + key + `"],"caFingerprint":"SHA256:ca","principal":"221b-superadmins-x"}`
	}
	restore := func(pairings ...string) (restoreReport, string) {
		t.Helper()
		f.SSHPairings = json.RawMessage("{" + strings.Join(pairings, ",") + "}")
		in, _ := json.Marshal(restoreInput{Backup: f, Parts: map[string]bool{"sshPairings": true}})
		code, out := e.do(t, "POST", "/api/v1/admin/restore", string(in))
		var rep restoreReport
		if err := json.Unmarshal([]byte(out), &rep); err != nil || code != http.StatusOK {
			t.Fatalf("the restore: %d %s", code, out)
		}
		return rep, strings.Join(rep.Notes, "\n")
	}
	// The node's comes back; the host with another key, port and login,
	// and the host never trusted, keep theirs.
	rep, notes := restore(pairing("n1", old, 22, "root"), pairing("db2", old, 2222, "ops"), pairing("db1", old, 22, "root"))
	if rep.Restored["sshPairings"] != 1 || !strings.Contains(notes, "host local/db2 is not restored") || !strings.Contains(notes, "host local/db1 is not restored") {
		t.Fatalf("restored %+v, notes:\n%s", rep.Restored, notes)
	}
	hosts, _ := svc.Hosts(ctx, "local")
	for _, h := range hosts {
		if h.Name == "db2" && (h.HostKey != here || h.Port != 22 || h.LoginUser != "root") {
			t.Fatalf("db2 after the restore: %+v", h)
		}
	}
	// A node paired here keeps its pairing; a host's that matches it here
	// comes back.
	rep, notes = restore(pairing("n1", here, 22, "core"), pairing("db2", here, 22, "root"))
	if rep.Restored["sshPairings"] != 1 || !strings.Contains(notes, "local/n1 is not restored: it is paired here") {
		t.Fatalf("restored %+v, notes:\n%s", rep.Restored, notes)
	}
	list, _ := svc.List(ctx, "local")
	got := map[string]nodessh.Pairing{}
	for _, p := range list {
		got[p.Node] = p
	}
	if got["n1"].LoginUser != "root" || got["n1"].HostKeys[0] != old || got["db2"].HostKeys[0] != here {
		t.Fatalf("the pairings: %+v", got)
	}
}

// A backup whose read of the SSH authority's key fails fails, saying so,
// rather than leave the key out as if there were none (finding 11 of the
// review of November 24, 2026: the nightly backup was written and recorded
// as OK without it, and a restore from it could make no pairing work).
func TestABackupFailsWhenTheAuthoritysKeyCannotBeRead(t *testing.T) {
	cs := k8sfake.NewClientset()
	cs.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("the API server did not answer")
	})
	svc := nodessh.New(nodessh.Options{CA: nodessh.NewCA(kube.NewSecretKeyFor(cs, "221b", "x-ssh-ca", nil)), Pairings: nodessh.NewPairings(&kv.Memory{}), Logger: slog.New(slog.DiscardHandler)})
	srv, c, _, _, _, _ := multiSetupWith(t, "ops@corp.example", nil, func(o *Options) { o.SSH = svc })
	resp, body := send(t, c, http.MethodGet, srv.URL+"/api/v1/admin/backup", "")
	if resp.StatusCode == http.StatusOK || !strings.Contains(body, "SSH authority") {
		t.Fatalf("the backup: %s %.200s", resp.Status, body)
	}
}
