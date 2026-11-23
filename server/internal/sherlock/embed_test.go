package sherlock

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An OpenAI-compatible provider's embeddings, in the order of the texts,
// with its credential (KN-55).
func TestEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer k-1" || in.Model != "qwen3-embedding" || len(in.Input) != 2 {
			http.Error(w, `{"error":{"message":"bad request"}}`, http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"index":1,"embedding":[0,1]},{"index":0,"embedding":[1,0]}]}`))
	}))
	defer srv.Close()
	c := &Client{Key: func() string { return "k-1" }, Provider: func() Provider {
		return Provider{Kind: KindOpenAI, Endpoint: srv.URL + "/v1", Auth: AuthHeader, AuthHeaderName: "Authorization", AuthPrefix: "Bearer "}
	}}
	vecs, err := c.Embed(context.Background(), "qwen3-embedding", []string{"a", "b"})
	if err != nil || len(vecs) != 2 || vecs[0][0] != 1 || vecs[1][1] != 1 {
		t.Fatalf("embeddings: %v, %v", vecs, err)
	}
	if _, err := c.Embed(context.Background(), "other", []string{"a", "b"}); err == nil {
		t.Fatal("an error answered is an error")
	}
	claude := &Client{Provider: func() Provider { return Provider{Kind: KindAnthropic} }}
	if _, err := claude.Embed(context.Background(), "m", []string{"a"}); err == nil {
		t.Fatal("embeddings of the Claude API")
	}
}
