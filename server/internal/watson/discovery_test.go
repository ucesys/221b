package watson

import (
	"testing"

	"k8s.io/client-go/rest"
)

// A connection whose credential or certificates change under the same ID
// and server gets a new discovery client: the old one asked with the
// revoked token until a restart (review of November 24, 2026, finding 10).
func TestDiscoveryFollowsTheConnectionsCredential(t *testing.T) {
	var d Discovery
	t1 := &rest.Config{Host: "https://prod.example:6443", BearerToken: "token-one"}
	first, err := d.For("prod", t1)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := d.For("prod", rest.CopyConfig(t1)); again != first {
		t.Fatal("the same connection got another client")
	}
	t2 := rest.CopyConfig(t1)
	t2.BearerToken = "token-two"
	second, _ := d.For("prod", t2)
	if second == first {
		t.Fatal("a new token kept the old client")
	}
	ca := rest.CopyConfig(t2)
	ca.CAData = []byte("another CA")
	if third, _ := d.For("prod", ca); third == second {
		t.Fatal("a new CA kept the old client")
	}
	if len(d.byKey) != 1 {
		t.Fatalf("%d entries for one connection", len(d.byKey))
	}
	if connDigest(t1) == connDigest(t2) {
		t.Fatal("one digest for two tokens")
	}
}
