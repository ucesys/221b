package sherlock

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/fakeclaude"
)

// A conversation part way through: a question, a tool call and its result.
func midConversation() Request {
	return Request{
		Model:     "some-model",
		MaxTokens: 500,
		System:    []Block{{Type: "text", Text: "You are Sherlock."}},
		Tools:     []ToolDef{{Name: "get_pod", Description: "One pod.", InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`)}},
		Messages: []Message{
			{Role: "user", Content: []Block{{Type: "text", Text: "Why is pod web/a failing?"}}},
			{Role: "assistant", Content: []Block{{Type: "thinking", Signature: "sig"}, {Type: "text", Text: "Reading it."}, {Type: "tool_use", ID: "call_1", Name: "get_pod", Input: json.RawMessage(`{"name":"a"}`)}}},
			{Role: "user", Content: []Block{{Type: "tool_result", ToolUseID: "call_1", Content: `{"status":"CrashLoopBackOff"}`}}},
		},
	}
}

func TestOpenAICompatible(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Azure's way: the key in api-key, the version in the query, a
		// gateway header beside them.
		if r.URL.Path != "/openai/deployments/d1/chat/completions" || r.URL.Query().Get("api-version") != "2024-10-21" || r.Header.Get("api-key") != "azure-key" || r.Header.Get("X-Team") != "data" {
			http.Error(w, `{"error":{"type":"invalid_request_error","message":"bad request `+r.URL.String()+`"}}`, 400)
			return
		}
		got, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range []string{
			`{"choices":[{"index":0,"delta":{"role":"assistant","content":"The pod "}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"crashes."}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_2","type":"function","function":{"name":"pod_logs","arguments":""}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"name\":"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a\"}"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":120,"completion_tokens":30,"prompt_tokens_details":{"cached_tokens":20}}}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c := &Client{Key: func() string { return "azure-key" }, Provider: func() Provider {
		return Provider{Kind: KindOpenAI, Endpoint: srv.URL + "/openai/deployments/d1", AuthHeaderName: "api-key", Query: "api-version=2024-10-21", Headers: map[string]string{"X-Team": "data"}}
	}}
	var tools []string
	resp, err := c.Stream(context.Background(), midConversation(), func(d Delta) {
		if d.Tool != "" {
			tools = append(tools, d.Tool)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "tool_use" || len(resp.Content) != 2 || resp.Content[0].Text != "The pod crashes." || resp.Content[1].Name != "pod_logs" || string(resp.Content[1].Input) != `{"name":"a"}` || resp.Content[1].ID != "call_2" {
		t.Fatalf("response %+v", resp)
	}
	if len(tools) != 1 || resp.Usage.InputTokens != 100 || resp.Usage.CacheReadInputTokens != 20 || resp.Usage.OutputTokens != 30 {
		t.Fatalf("tools %v usage %+v", tools, resp.Usage)
	}
	// The conversation as Chat Completions has it.
	b := got
	for _, want := range []string{`"role":"system","content":"You are Sherlock."`, `"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_pod","arguments":"{\"name\":\"a\"}"}}]`, `"role":"tool","content":"{\"status\":\"CrashLoopBackOff\"}","tool_call_id":"call_1"`, `"parameters":{"type":"object"`, `"max_tokens":500`, `"stream":true`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the request lacks %s:\n%s", want, b)
		}
	}
	if strings.Contains(string(b), "thinking") || strings.Contains(string(b), "sig") {
		t.Errorf("thinking blocks went to another provider: %s", b)
	}
}

// event encodes one message of AWS's event stream.
func event(headers map[string]string, payload string) []byte {
	var h bytes.Buffer
	for k, v := range headers {
		h.WriteByte(byte(len(k)))
		h.WriteString(k)
		h.WriteByte(7)
		_ = binary.Write(&h, binary.BigEndian, uint16(len(v)))
		h.WriteString(v)
	}
	total := 12 + h.Len() + len(payload) + 4
	var m bytes.Buffer
	_ = binary.Write(&m, binary.BigEndian, uint32(total))
	_ = binary.Write(&m, binary.BigEndian, uint32(h.Len()))
	_ = binary.Write(&m, binary.BigEndian, crc32.ChecksumIEEE(m.Bytes()))
	m.Write(h.Bytes())
	m.WriteString(payload)
	_ = binary.Write(&m, binary.BigEndian, crc32.ChecksumIEEE(m.Bytes()))
	return m.Bytes()
}

func TestBedrockConverse(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	var got []byte
	var auth, token string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, token = r.Header.Get("Authorization"), r.Header.Get("X-Amz-Security-Token")
		if r.URL.EscapedPath() != "/model/us.anthropic.claude-sonnet-5-5-v1:0/converse-stream" {
			http.Error(w, `{"message":"no route `+r.URL.EscapedPath()+`"}`, 404)
			return
		}
		got, _ = io.ReadAll(r.Body)
		ev := func(typ, payload string) []byte {
			return event(map[string]string{":event-type": typ, ":content-type": "application/json", ":message-type": "event"}, payload)
		}
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		for _, e := range [][]byte{
			ev("messageStart", `{"role":"assistant"}`),
			ev("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"Looking"}}`),
			ev("contentBlockStop", `{"contentBlockIndex":0}`),
			ev("contentBlockStart", `{"contentBlockIndex":1,"start":{"toolUse":{"toolUseId":"tu1","name":"pod_logs"}}}`),
			ev("contentBlockDelta", `{"contentBlockIndex":1,"delta":{"toolUse":{"input":"{\"name\":\"a\"}"}}}`),
			ev("contentBlockStop", `{"contentBlockIndex":1}`),
			ev("messageStop", `{"stopReason":"tool_use"}`),
			ev("metadata", `{"usage":{"inputTokens":90,"outputTokens":12}}`),
		} {
			w.Write(e)
		}
	}))
	defer srv.Close()
	c := &Client{
		Key:      func() string { return `{"accessKeyId":"AKIDEXAMPLE","secretAccessKey":"secret","sessionToken":"tok"}` },
		Provider: func() Provider { return Provider{Kind: KindBedrock, Region: "us-east-1", Endpoint: srv.URL} },
		Now:      func() time.Time { return now },
	}
	req := midConversation()
	req.Model = "us.anthropic.claude-sonnet-5-5-v1:0"
	resp, err := c.Stream(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261016/us-east-1/bedrock/aws4_request, SignedHeaders=content-type;host;x-amz-date;x-amz-security-token, Signature=") || token != "tok" {
		t.Fatalf("signature %q token %q", auth, token)
	}
	if resp.StopReason != "tool_use" || len(resp.Content) != 2 || resp.Content[0].Text != "Looking" || resp.Content[1].ID != "tu1" || string(resp.Content[1].Input) != `{"name":"a"}` || resp.Usage.InputTokens != 90 {
		t.Fatalf("response %+v", resp)
	}
	b := got
	for _, want := range []string{`"system":[{"text":"You are Sherlock."}]`, `"toolUse":{"toolUseId":"call_1","name":"get_pod","input":{"name":"a"}}`, `"toolResult":{"toolUseId":"call_1","content":[{"text":"{\"status\":\"CrashLoopBackOff\"}"}],"status":"success"}`, `"toolSpec":{"description":"One pod.","inputSchema":{"json":{"type":"object"`, `"maxTokens":500`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the request lacks %s:\n%s", want, b)
		}
	}
}

// The example of AWS's documentation of Signature Version 4: IAM's
// ListUsers, signed on 30 August 2015.
func TestSignV4Example(t *testing.T) {
	r, _ := http.NewRequest(http.MethodGet, "https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08", nil)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	signV4(r, nil, AWSCredentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}, "us-east-1", "iam", time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC))
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/iam/aws4_request, SignedHeaders=content-type;host;x-amz-date, Signature=5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"
	if got := r.Header.Get("Authorization"); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestBedrockWithTheRolesWebIdentity(t *testing.T) {
	stsCalls := 0
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stsCalls++
		_ = r.ParseForm()
		if r.Form.Get("Action") != "AssumeRoleWithWebIdentity" || r.Form.Get("WebIdentityToken") != "projected-jwt" || r.Form.Get("RoleArn") != "arn:aws:iam::123:role/sherlock" {
			http.Error(w, "bad", 400)
			return
		}
		fmt.Fprintf(w, `<AssumeRoleWithWebIdentityResponse><AssumeRoleWithWebIdentityResult><Credentials><AccessKeyId>ASIAROLE</AccessKeyId><SecretAccessKey>s</SecretAccessKey><SessionToken>st</SessionToken><Expiration>%s</Expiration></Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	}))
	defer sts.Close()
	env := map[string]string{"AWS_ROLE_ARN": "arn:aws:iam::123:role/sherlock", "AWS_WEB_IDENTITY_TOKEN_FILE": "/var/run/token"}
	c := &Client{Ambient: &Ambient{Getenv: func(k string) string { return env[k] }, ReadFile: func(string) ([]byte, error) { return []byte("projected-jwt\n"), nil }, STSURL: sts.URL}}
	for i := 0; i < 2; i++ {
		k, err := c.awsCredentials(context.Background(), "", "eu-west-1")
		if err != nil || k.AccessKeyID != "ASIAROLE" || k.SessionToken != "st" {
			t.Fatalf("credentials %+v %v", k, err)
		}
	}
	if stsCalls != 1 {
		t.Fatalf("STS was asked %d times; the credentials are kept until they expire", stsCalls)
	}
	none := &Client{Ambient: &Ambient{Getenv: func(string) string { return "" }}}
	if _, err := none.awsCredentials(context.Background(), "", ""); err == nil || !strings.Contains(err.Error(), "IRSA") {
		t.Fatalf("no identity: %v", err)
	}
}

func TestVertexWithAServiceAccount(t *testing.T) {
	pk, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(pk)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		parts := strings.Split(r.Form.Get("assertion"), ".")
		if len(parts) != 3 {
			http.Error(w, `{"error_description":"no jwt"}`, 400)
			return
		}
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
		if rsa.VerifyPKCS1v15(&pk.PublicKey, crypto.SHA256, sum[:], sig) != nil || !strings.Contains(string(claims), `"iss":"sherlock@proj.iam.gserviceaccount.com"`) || !strings.Contains(string(claims), "cloud-platform") {
			http.Error(w, `{"error_description":"bad jwt"}`, 401)
			return
		}
		w.Write([]byte(`{"access_token":"ya29.token","expires_in":3600}`))
	}))
	defer tokens.Close()
	key, _ := json.Marshal(map[string]string{"type": "service_account", "client_email": "sherlock@proj.iam.gserviceaccount.com", "private_key": pemKey, "token_uri": tokens.URL})
	var paths []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ya29.token" {
			http.Error(w, `{"error":{"status":"UNAUTHENTICATED","message":"no token"}}`, 401)
			return
		}
		paths = append(paths, r.URL.Path)
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Gemini here.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		for _, e := range []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`,
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Claude here."}}`,
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}`,
			`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
		} {
			fmt.Fprintf(w, "%s\n\n", e)
		}
	}))
	defer srv.Close()
	c := &Client{Key: func() string { return string(key) }, Provider: func() Provider {
		return Provider{Kind: KindVertex, Endpoint: srv.URL, Region: "us-east5", Project: "proj"}
	}}
	q := Request{Model: "claude-sonnet-5-5", MaxTokens: 100, Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "Hi"}}}}}
	resp, err := c.Stream(context.Background(), q, nil)
	if err != nil || resp.Content[0].Text != "Claude here." {
		t.Fatalf("claude: %+v %v", resp, err)
	}
	q.Model = "google/gemini-2.5-pro"
	resp, err = c.Stream(context.Background(), q, nil)
	if err != nil || resp.Content[0].Text != "Gemini here." {
		t.Fatalf("gemini: %+v %v", resp, err)
	}
	if paths[0] != "/v1/projects/proj/locations/us-east5/publishers/anthropic/models/claude-sonnet-5-5:streamRawPredict" || paths[1] != "/v1/projects/proj/locations/us-east5/endpoints/openapi/chat/completions" {
		t.Fatalf("paths %v", paths)
	}
	if bodies[0]["anthropic_version"] != "vertex-2023-10-16" || bodies[0]["model"] != nil || bodies[1]["model"] != "google/gemini-2.5-pro" {
		t.Fatalf("bodies %v", bodies)
	}
	if _, _, err := ParseGoogleKey(`{"type":"authorized_user"}`); err == nil {
		t.Fatal("a user's credentials passed for a service account key")
	}
}

func TestGatewayWithAPrivateCA(t *testing.T) {
	fakeAnthropic := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/llm/anthropic/v1/messages" || r.Header.Get("Authorization") != "Bearer gw-token" {
			http.Error(w, `{"error":{"type":"authentication_error","message":"who are you"}}`, 401)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	})
	srv := httptest.NewTLSServer(fakeAnthropic)
	defer srv.Close()
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	p := Provider{Kind: KindAnthropic, Endpoint: srv.URL + "/llm/anthropic", AuthHeaderName: "Authorization", AuthPrefix: "Bearer "}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	req := Request{Model: "claude-opus-5-5", MaxTokens: 10, Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}}}}
	// Without the CA the certificate is not trusted.
	c := &Client{Key: func() string { return "gw-token" }, Provider: func() Provider { return p }}
	if _, err := c.Stream(context.Background(), req, nil); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("an untrusted certificate was accepted: %v", err)
	}
	p.CACert = ca
	resp, err := c.Stream(context.Background(), req, nil)
	if err != nil || resp.Content[0].Text != "ok" {
		t.Fatalf("%+v %v", resp, err)
	}
	// A bad answer names the gateway, not the Claude API.
	c.Key = func() string { return "wrong" }
	_, err = c.Stream(context.Background(), req, nil)
	if err == nil || !strings.Contains(err.Error(), "the model's API answered 401") {
		t.Fatalf("error: %v", err)
	}
}

func TestProviderValidation(t *testing.T) {
	for _, bad := range []Provider{
		{Kind: "watsonx"},
		{Kind: KindOpenAI, Endpoint: "ftp://x"},
		{Kind: KindOpenAI, Endpoint: "https://user:pw@gw.example"},
		{Kind: KindOpenAI, Endpoint: "https://gw.example/v1?key=x"},
		{Kind: KindBedrock},
		{Kind: KindVertex, Region: "us-east5"},
		{Kind: KindOpenAI, Auth: AuthAWS},
		{Kind: KindOpenAI, Headers: map[string]string{"X-Bad\nName": "v"}},
		{Kind: KindOpenAI, Headers: map[string]string{"X-Ok": "v\r\nInjected: 1"}},
		{Kind: KindOpenAI, Headers: map[string]string{"Host": "evil"}},
		{Kind: KindOpenAI, CACert: "not pem"},
	} {
		if bad.Validate() == nil {
			t.Errorf("%+v passed", bad)
		}
	}
	for _, good := range []Provider{
		{},
		{Kind: KindOpenAI, Endpoint: "http://ollama.ai.svc:11434/v1", Auth: AuthNone},
		{Kind: KindBedrock, Region: "eu-central-1"},
		{Kind: KindBedrock, Endpoint: "https://ai-gw.corp.example/bedrock", Auth: AuthHeader, AuthHeaderName: "X-Gateway-Key"},
		{Kind: KindVertex, Region: "europe-west1", Project: "my-proj"},
	} {
		if err := good.Validate(); err != nil {
			t.Errorf("%+v: %v", good, err)
		}
	}
	if (Provider{Kind: KindBedrock}).NeedsKey() || !(Provider{}).NeedsKey() || (Provider{Kind: KindOpenAI, Auth: AuthNone}).NeedsKey() {
		t.Error("NeedsKey")
	}
	if u, _ := url.Parse((Provider{Kind: KindBedrock, Region: "eu-west-1"}).Normalized("").Endpoint); u.Host != "bedrock-runtime.eu-west-1.amazonaws.com" {
		t.Errorf("bedrock endpoint %s", u)
	}
}

// The fake API's Chat Completions endpoint runs the same scripts, so a
// console set to an OpenAI-compatible provider answers through it.
func TestFakeChatCompletions(t *testing.T) {
	fake := fakeclaude.New()
	fake.Delay = 0
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := &Client{Key: func() string { return "sk-openai-test" }, Provider: func() Provider { return Provider{Kind: KindOpenAI, Endpoint: srv.URL + "/v1"} }}
	req := Request{Model: "gpt-x", MaxTokens: 100, Messages: []Message{{Role: "user", Content: []Block{{Type: "text", Text: "Tell me about pod web/api-0"}}}}}
	resp, err := c.Stream(context.Background(), req, nil)
	if err != nil || resp.StopReason != "tool_use" || resp.Content[len(resp.Content)-1].Name != "get_pod" {
		t.Fatalf("%+v %v", resp, err)
	}
	call := resp.Content[len(resp.Content)-1]
	req.Messages = append(req.Messages, Message{Role: "assistant", Content: resp.Content}, Message{Role: "user", Content: []Block{{Type: "tool_result", ToolUseID: call.ID, Content: `{"status":"Running","node":"w1"}`}}})
	resp, err = c.Stream(context.Background(), req, nil)
	if err != nil || resp.StopReason != "end_turn" || !strings.Contains(resp.Content[0].Text, "is Running") {
		t.Fatalf("%+v %v", resp, err)
	}
}
