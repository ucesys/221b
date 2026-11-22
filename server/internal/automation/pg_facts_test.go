package automation

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// The store keeps the nodes' facts in the database as in a ConfigMap, and
// another replica's store reads the same.
func TestFactsInTheDatabase(t *testing.T) {
	d := dbtest.Open(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 18, 11, 0, 0, 0, time.UTC)
	st := NewStore(NewFactsPG(d), nil)
	list := []NodeFacts{
		{Cluster: "c1", Node: "worker-1", At: at, Facts: map[string]string{"os.name": "Ubuntu 24.04", "cpu.count": "16", "net.eth0.mtu": "9000"}},
		{Cluster: "c1", Node: "backup-1", At: at.Add(time.Minute), Error: "ssh: connection refused"},
	}
	if err := st.saveFacts(ctx, list); err != nil {
		t.Fatal(err)
	}
	list[0].Facts["cpu.count"], list[0].At = "32", at.Add(time.Hour)
	if err := st.saveFacts(ctx, list[:1]); err != nil {
		t.Fatal(err)
	}
	var got map[string]NodeFacts
	if _, err := NewStore(NewFactsPG(d), nil).load(ctx, FactsKey, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]NodeFacts{factKey("c1", "worker-1"): list[0], factKey("c1", "backup-1"): list[1]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}
