package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/ucesys/221b/server/internal/apps"
	"github.com/ucesys/221b/server/internal/backup"
	"github.com/ucesys/221b/server/internal/kube"
)

// ADR-137: the backup key's Secret is made once, by whichever replica
// gets there first, however many start at once; every one of them ends
// with that key, and the Secret is kept when the release is uninstalled.
func TestTheBackupKeySecretIsMadeOnce(t *testing.T) {
	ctx := context.Background()
	for round := range 20 {
		cs := fake.NewClientset()
		var wg sync.WaitGroup
		fps := make([]string, 4)
		for i := range fps {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ks := kube.NewSecretKeyFor(cs, "221b", "x"+backup.KeySecretSuffix, map[string]string{"app.kubernetes.io/component": "backup-key"}).WithAnnotations(map[string]string{"helm.sh/resource-policy": "keep"})
				k, err := backup.EnsureSecretsKey(ctx, ks)
				if err != nil {
					t.Errorf("round %d, replica %d: %v", round, i, err)
					return
				}
				fps[i] = k.Fingerprint()
			}()
		}
		wg.Wait()
		for _, fp := range fps[1:] {
			if fp != fps[0] || fp == "" {
				t.Fatalf("round %d: the replicas hold different keys: %v", round, fps)
			}
		}
		list, _ := cs.CoreV1().Secrets("221b").List(ctx, metav1.ListOptions{})
		if len(list.Items) != 1 {
			t.Fatalf("round %d: %d Secrets", round, len(list.Items))
		}
		sec := list.Items[0]
		if sec.Name != "x-backup-key" || sec.Annotations["helm.sh/resource-policy"] != "keep" || len(sec.Data[backup.KeyData]) != 64 {
			t.Fatalf("round %d: the Secret %s %v, key of %d bytes", round, sec.Name, sec.Annotations, len(sec.Data[backup.KeyData]))
		}
		if k, _ := backup.ParseSecretsKey(sec.Data[backup.KeyData]); k.Fingerprint() != fps[0] {
			t.Fatalf("round %d: the Secret holds another key", round)
		}
	}
}

// HR-10 with ADR-137: with the same key a restored repository gets its
// password back; the backup holds it sealed, never in clear.
func TestHelmRepositoriesBackupWithTheKey(t *testing.T) {
	ctx := context.Background()
	key, _ := backup.NewKeyText()
	src := func(context.Context) (*backup.SecretsKey, error) { return backup.ParseSecretsKey(key) }
	repos := apps.NewRepos(apps.RepoOptions{})
	pw := "s3cret-value"
	if _, _, err := repos.Save(ctx, "private", apps.RepositoryInput{Kind: apps.KindHelm, URL: "https://charts.example.com/private", Auth: apps.AuthBasic, Username: "reader", Secret: &pw}, "alice"); err != nil {
		t.Fatal(err)
	}
	reg := backup.NewRegistry()
	reg.SetKey("x-backup-key", src)
	registerHelmReposBackup(reg, repos)
	f, err := reg.Export(ctx, &backup.Env{By: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(f)
	if strings.Contains(string(b), pw) || strings.Contains(string(b), string(key)) || !strings.Contains(string(b), `sealed:`+mustKey(t, key).Fingerprint()+`:`) {
		t.Fatalf("the backup: %s", b)
	}
	into := apps.NewRepos(apps.RepoOptions{})
	reg2 := backup.NewRegistry()
	reg2.SetKey("x-backup-key", src)
	registerHelmReposBackup(reg2, into)
	rep := reg2.Restore(ctx, f, map[string]bool{"helmRepositories": true}, &backup.Env{By: "bob"})
	if rep.Restored["helmRepositories"] != 1 || len(rep.Secrets) != 0 {
		t.Fatalf("restored: %+v", rep)
	}
	got, secret, err := into.Get(ctx, "private")
	if err != nil || secret != pw || got.Auth != apps.AuthBasic || got.Username != "reader" {
		t.Fatalf("private: %+v %v", got, err)
	}
}

func mustKey(t *testing.T, text []byte) *backup.SecretsKey {
	t.Helper()
	k, err := backup.ParseSecretsKey(text)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
