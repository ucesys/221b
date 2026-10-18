package api

import "testing"

// A name that is not one path segment is refused before it reaches the API
// server, where "../secrets/x" would read a Secret as another kind (X-01).
func TestYAMLRefusesPathNames(t *testing.T) {
	srv, _, src := setup(t, true)
	src.Put(samplePod("shop", "web", "n1"))
	for _, path := range []string{
		"/api/v1/yaml/configmaps/shop/..%2Fsecrets%2Fcreds",
		"/api/v1/yaml/pods/..%2Fkube-system/web",
	} {
		if r := get(t, srv.URL+path); r.StatusCode != 400 {
			t.Errorf("%s: %d, want 400", path, r.StatusCode)
		}
	}
}
