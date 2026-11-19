package cards

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/db/dbtest"
	"github.com/ucesys/221b/server/internal/kv"
)

func day(h int) time.Time { return time.Date(2026, 11, 18, h, 0, 0, 0, time.UTC) }

// save writes a document through a backend and reads it back.
func roundTrip[T any](t *testing.T, b kv.Backend, key string, doc T, version string) (T, string) {
	t.Helper()
	ctx := context.Background()
	raw, _ := json.Marshal(doc)
	if _, err := b.SaveKey(ctx, key, raw, version); err != nil {
		t.Fatalf("saving: %v", err)
	}
	got, next, err := b.LoadKey(ctx, key)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	var out T
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatal(err)
	}
	return out, next
}

// Every field of a standing approval is a column and comes back; the
// list keeps its order; a save over a stale version is refused; a removed
// one is gone.
func TestStandingApprovalsInTheDatabase(t *testing.T) {
	b := NewStandingPG(dbtest.Open(t))
	ctx := context.Background()
	if _, v, err := b.LoadKey(ctx, StandingKey); !errors.Is(err, kv.ErrNotFound) || v != "0" {
		t.Fatalf("nothing kept: %q %v", v, err)
	}
	full := Standing{ID: "s1a2b3c4", Name: "Restart crashed pods", On: true, Agent: "troubleshooter", Clusters: []string{"c1", "c2"}, Actions: []string{"kubectl.rollout-restart"},
		Namespaces: []string{"shop"}, Condition: "risk == 'low'", PerHour: 6, By: Person{Key: "local|ann", Name: "Ann"}, RunAs: RunAs{User: "221b:agents", Groups: []string{"ops"}},
		Created: day(9), Updated: day(10), Applied: []time.Time{day(11), day(12)}}
	bare := Standing{ID: "s9f8e7d6", Name: "Scale down at night", Clusters: []string{"c1"}, Actions: []string{"kubectl.scale"}, PerHour: 1, By: Person{Key: "221b", Name: "221B", Kind: "system"},
		Created: day(8), Updated: day(8)}
	doc := standingRecord{Standing: []Standing{full, bare}}
	got, v1 := roundTrip(t, b, StandingKey, doc, "0")
	if !reflect.DeepEqual(got, doc) {
		t.Fatalf("got  %+v\nwant %+v", got, doc)
	}
	// Reordered, one changed.
	bare.On, bare.Applied = true, []time.Time{day(13)}
	doc = standingRecord{Standing: []Standing{bare, full}}
	got, v2 := roundTrip(t, b, StandingKey, doc, v1)
	if !reflect.DeepEqual(got, doc) {
		t.Fatalf("after reordering: %+v", got)
	}
	raw, _ := json.Marshal(standingRecord{})
	if _, err := b.SaveKey(ctx, StandingKey, raw, v1); !errors.Is(err, kv.ErrConflict) {
		t.Fatalf("a save over a stale version: %v", err)
	}
	got, _ = roundTrip(t, b, StandingKey, standingRecord{Standing: []Standing{full}}, v2)
	if len(got.Standing) != 1 || got.Standing[0].ID != full.ID {
		t.Fatalf("after removing one: %+v", got)
	}
}

func TestNotificationChannelsInTheDatabase(t *testing.T) {
	b := NewNotifyPG(dbtest.Open(t))
	doc := NotifySettings{Link: "https://221b.corp.example", Channels: []Channel{{ID: "n0a1b2c3", Kind: "slack", Name: "Ops", On: true}, {ID: "n4d5e6f7", Kind: "webhook", Name: "Pager"}}}
	got, v := roundTrip(t, b, NotifyKey, doc, "")
	if !reflect.DeepEqual(got, doc) {
		t.Fatalf("got  %+v\nwant %+v", got, doc)
	}
	doc.Link, doc.Channels = "", doc.Channels[1:]
	if got, _ = roundTrip(t, b, NotifyKey, doc, v); !reflect.DeepEqual(got, doc) {
		t.Fatalf("after a change: %+v", got)
	}
	// What the ConfigMap held is copied once.
	cm := &kv.Memory{}
	raw, _ := json.Marshal(NotifySettings{Channels: []Channel{{ID: "n1", Kind: "teams", Name: "Old"}}})
	_, _ = cm.SaveKey(context.Background(), NotifyKey, raw, "")
	other := NewNotifyPG(dbtest.Open(t))
	if copied, err := other.Import(context.Background(), cm, "test"); err != nil || !copied {
		t.Fatalf("copy: %v %v", copied, err)
	}
	if copied, err := other.Import(context.Background(), cm, "test"); err != nil || copied {
		t.Fatalf("second copy: %v %v", copied, err)
	}
}
