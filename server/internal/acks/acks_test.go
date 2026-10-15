package acks

import (
	"strings"
	"testing"
	"time"
)

func TestSetRemovePrune(t *testing.T) {
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	list := Set(nil, Ack{Key: "pod:web/b", Fingerprint: "failed|CrashLoopBackOff", By: "a", At: at})
	list = Set(list, Ack{Key: "node:w1", Fingerprint: "warning|Ready", By: "a", At: at})
	list = Set(list, Ack{Key: "pod:web/b", Fingerprint: "failed|CrashLoopBackOff", By: "b", At: at})
	if len(list) != 2 || list[0].Key != "node:w1" || list[1].By != "b" {
		t.Fatalf("set %+v", list)
	}
	if err := Validate(list); err != nil {
		t.Fatal(err)
	}
	now := map[string]string{"pod:web/b": "failed|ImagePullBackOff", "node:w1": "warning|Ready"}
	pruned := Prune(list, func(k string) (string, bool) { fp, ok := now[k]; return fp, ok })
	if len(pruned) != 1 || pruned[0].Key != "node:w1" {
		t.Fatalf("prune %+v", pruned)
	}
	if got := Remove(pruned, "node:w1"); len(got) != 0 {
		t.Fatalf("remove %+v", got)
	}
	if err := Validate([]Ack{{Key: "a", Fingerprint: "x"}, {Key: "a", Fingerprint: "x"}}); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("duplicate: %v", err)
	}
	many := make([]Ack, Max+1)
	for i := range many {
		many[i] = Ack{Key: string(rune('a'+i%26)) + time.Duration(i).String(), Fingerprint: "x"}
	}
	if Validate(many) == nil {
		t.Fatal("too many acknowledgements were accepted")
	}
}
