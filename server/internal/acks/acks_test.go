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

// A job acknowledges an alert it handled (CF-79), never over a person's
// acknowledgement of the alert as it is, and within Max.
func TestSetByJob(t *testing.T) {
	at := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	job := Ack{Key: "pod:web/b", Fingerprint: "failed|CrashLoopBackOff", By: "Alert diagnosis", At: at, Run: "run-7", Case: "C-3"}
	list, ok := SetByJob(nil, job)
	if !ok || len(list) != 1 || !list[0].ByJob() || list[0].Case != "C-3" {
		t.Fatalf("set %+v %v", list, ok)
	}
	if err := Validate(list); err != nil {
		t.Fatal(err)
	}
	// Again with a new run: the job's own is replaced.
	again := job
	again.Run = "run-9"
	if list, ok = SetByJob(list, again); !ok || len(list) != 1 || list[0].Run != "run-9" {
		t.Fatalf("again %+v %v", list, ok)
	}
	// A person's acknowledgement of the alert as it is stands.
	person := []Ack{{Key: "pod:web/b", Fingerprint: "failed|CrashLoopBackOff", By: "ann", At: at}}
	if got, ok := SetByJob(person, job); ok || got[0].By != "ann" || got[0].ByJob() {
		t.Fatalf("over a person's %+v %v", got, ok)
	}
	// A person's of the alert as it was before has lapsed: the job's goes.
	person[0].Fingerprint = "pending|ContainerCreating"
	if got, ok := SetByJob(person, job); !ok || got[0].Run != "run-7" {
		t.Fatalf("over a lapsed one %+v %v", got, ok)
	}
	full := make([]Ack, Max)
	for i := range full {
		full[i] = Ack{Key: "k" + time.Duration(i).String(), Fingerprint: "x"}
	}
	if got, ok := SetByJob(full, job); ok || len(got) != Max {
		t.Fatalf("past Max: %d %v", len(got), ok)
	}
	if bad := Validate([]Ack{{Key: "a", Fingerprint: "x", Run: strings.Repeat("r", 65)}}); bad == nil {
		t.Fatal("a run too long was accepted")
	}
}

// A job acknowledges an alert its run concluded on without a case (CF-82):
// with the exception that covers what it found, or as expected with the
// agent's reason; kept and checked like the others.
func TestSetByJobWhatItConcludedCF82(t *testing.T) {
	at := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	expected := Ack{Key: "pod:kube-system/canal", Fingerprint: "failed|Unknown", By: "Alert diagnosis", At: at, Run: "run-17", Expected: true, Why: "r620-worker is annotated nodepower/status: asleep."}
	excepted := Ack{Key: "pod:web/c", Fingerprint: "failed|Error", By: "Alert diagnosis", At: at, Run: "run-18", Exception: "EX-3"}
	list, ok := SetByJob(nil, expected)
	if list, ok = SetByJob(list, excepted); !ok || len(list) != 2 || !list[0].ByJob() || !list[0].Expected || list[0].Why == "" || list[1].Exception != "EX-3" {
		t.Fatalf("set %+v %v", list, ok)
	}
	if err := Validate(list); err != nil {
		t.Fatal(err)
	}
	if bad := Validate([]Ack{{Key: "a", Fingerprint: "x", Run: "run-1", Exception: strings.Repeat("E", 33)}}); bad == nil {
		t.Error("an exception too long was accepted")
	}
	if bad := Validate([]Ack{{Key: "a", Fingerprint: "x", Run: "run-1", Expected: true, Why: strings.Repeat("w", 2001)}}); bad == nil {
		t.Error("a reason too long was accepted")
	}
}
