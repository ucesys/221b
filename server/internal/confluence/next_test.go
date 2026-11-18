package confluence_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/confluence"
)

// The token goes to the site only: a listing whose next page is on
// another host, or outside the site's path, is not followed there.
func TestTheNextPageStaysOnTheSite(t *testing.T) {
	var mu sync.Mutex
	var leaked []string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		leaked = append(leaked, r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		w.Write([]byte(`{"results":[]}`))
	}))
	defer other.Close()
	next := ""
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/ctx/") {
			mu.Lock()
			leaked = append(leaked, r.URL.Path+" "+r.Header.Get("Authorization"))
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"id":"1","title":"A","version":{"number":1}}],"_links":{"next":"` + next + `"}}`))
	}))
	defer site.Close()
	c, err := confluence.New(confluence.Config{BaseURL: site.URL + "/ctx", Flavor: confluence.Cloud, Email: "ann@example.com", Token: "tok-123456", HTTP: site.Client(), Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{other.URL + "/wiki/api/v2/pages?cursor=2", "//" + strings.TrimPrefix(other.URL, "http://") + "/x", "/elsewhere/wiki/api/v2/pages", "/ctx/../elsewhere/x", "/ctx/%2e%2e/elsewhere/x", "/ctx%2F..%2Felsewhere"} {
		next = n
		if _, err := c.Pages(ctx, confluence.Space{ID: "1", Key: "K"}, time.Time{}); err == nil || !strings.Contains(err.Error(), "not asked") {
			t.Errorf("next %s: %v", n, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(leaked) > 0 {
		t.Fatalf("asked off the site: %q", leaked)
	}
}
