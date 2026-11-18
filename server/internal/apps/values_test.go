package apps

import (
	"reflect"
	"testing"
)

func TestValueChangesListsRemovedAndChangedKeysOnly(t *testing.T) {
	now := map[string]any{
		"replicas":     float64(3),
		"service":      map[string]any{"type": "LoadBalancer", "port": float64(80)},
		"image":        map[string]any{"tag": "v1"},
		"allowedHosts": []any{"a", "b"},
		"nodeSelector": map[string]any{"pool": "infra"},
	}
	next := map[string]any{
		"replicas":     int64(3),
		"service":      map[string]any{"port": 8080},
		"image":        map[string]any{"tag": "v1", "pullPolicy": "Always"},
		"allowedHosts": []any{"a"},
		"ldap":         map[string]any{"enabled": true},
	}
	removed, changed := ValueChanges(now, next)
	if !reflect.DeepEqual(removed, []string{"nodeSelector", "service.type"}) || !reflect.DeepEqual(changed, []string{"allowedHosts", "service.port"}) {
		t.Fatalf("removed %v changed %v", removed, changed)
	}
	if r, c := ValueChanges(now, now); len(r)+len(c) != 0 {
		t.Fatalf("%v %v", r, c)
	}
	if r, _ := ValueChanges(now, nil); len(r) != 5 {
		t.Fatalf("%v", r)
	}
}

func TestMergeValuesLaysTheirsOverTheRelease(t *testing.T) {
	base := map[string]any{"service": map[string]any{"type": "LoadBalancer"}, "ldap": map[string]any{"url": "ldap://old"}}
	over := map[string]any{"ldap": map[string]any{"url": "ldaps://new", "enabled": true}}
	got := MergeValues(base, over)
	want := map[string]any{"service": map[string]any{"type": "LoadBalancer"}, "ldap": map[string]any{"url": "ldaps://new", "enabled": true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	if base["ldap"].(map[string]any)["url"] != "ldap://old" {
		t.Fatal("the base changed")
	}
}

func TestSelfRelease(t *testing.T) {
	s := ParseSelfRelease("local", "221b/k8s-console")
	if !s.Is("local", "221b", "k8s-console") || s.Is("other", "221b", "k8s-console") || s.Is("local", "default", "k8s-console") {
		t.Fatalf("%+v", s)
	}
	for _, bad := range []string{"", "k8s-console", "/x", "x/", "a/b/c"} {
		if ParseSelfRelease("local", bad).Is("local", "", "") || ParseSelfRelease("local", bad).Name != "" {
			t.Errorf("%q parsed", bad)
		}
	}
}
