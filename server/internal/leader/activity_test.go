package leader

import (
	"reflect"
	"testing"
)

func TestActivityTellsItsReadersInOrderAndLateOnesAtOnce(t *testing.T) {
	a := NewActivity(true)
	if a.Active() || a.Standby() || !a.Warm() {
		t.Fatalf("a warm standby starts inactive and not cold: active %v standby %v", a.Active(), a.Standby())
	}
	var seen []string
	a.OnChange(func(active bool) { seen = append(seen, "first:"+boolStr(active)) })
	a.OnChange(func(active bool) { seen = append(seen, "second:"+boolStr(active)) })
	if len(seen) != 0 {
		t.Fatalf("hooks were called before any change: %v", seen)
	}
	a.Set(true)
	a.Set(true) // the same again says nothing
	// A reader registered after the election was won hears about it now.
	a.OnChange(func(active bool) { seen = append(seen, "late:"+boolStr(active)) })
	a.Set(false)
	want := []string{"first:true", "second:true", "late:true", "first:false", "second:false", "late:false"}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("hooks saw %v, want %v", seen, want)
	}
}

func TestAColdStandbyIsOneUntilItLeads(t *testing.T) {
	a := NewActivity(false)
	if !a.Standby() {
		t.Fatal("inactive and not warm is a cold standby")
	}
	a.Set(true)
	if a.Standby() || !a.Active() {
		t.Fatal("the leader is no standby")
	}
	a.Set(false)
	if !a.Standby() {
		t.Fatal("back to a cold standby")
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
