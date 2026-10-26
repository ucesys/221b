package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ucesys/221b/server/internal/tunnel"
)

func TestTunnelURL(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"https://console.example", "prod"}:     "wss://console.example/api/v1/clusters/prod/sessions/s1/tunnel",
		{"https://console.example/", ""}:        "wss://console.example/api/v1/sessions/s1/tunnel",
		{"http://localhost:8080/221b", "local"}: "ws://localhost:8080/221b/api/v1/sessions/s1/tunnel",
	} {
		got, err := tunnelURL(options{console: strings.TrimRight(in[0], "/"), cluster: in[1], session: "s1"})
		if err != nil || got != want {
			t.Errorf("%v: %s %v, want %s", in, got, err, want)
		}
	}
	if _, err := tunnelURL(options{console: "console.example", session: "s1"}); err == nil {
		t.Error("a URL without a scheme passed")
	}
}

func TestParse(t *testing.T) {
	o, err := parse([]string{"--cluster", "prod", "https://c.example", "--session", "s1", "--token", "tk", "--local", "15432"})
	if err != nil || o.console != "https://c.example" || o.cluster != "prod" || o.local != "15432" {
		t.Fatalf("%+v %v", o, err)
	}
	if _, err := parse([]string{"https://c.example", "--token", "tk"}); err == nil {
		t.Fatal("no session passed")
	}
	// The token need not be on the command line: it is asked for (TB-20).
	if o, err := parse([]string{"https://c.example", "--session", "s1"}); err != nil || o.token != "" {
		t.Fatalf("%+v %v", o, err)
	}
}

// Without --token or the environment, the token comes from the terminal,
// hidden, or from a pipe: either way it stays out of the shell's history.
func TestAskTokenReadsAPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		io.WriteString(w, "  tk-from-a-pipe \n")
		w.Close()
	}()
	var prompt strings.Builder
	got, err := askToken(r, &prompt)
	if err != nil || got != "tk-from-a-pipe" {
		t.Fatalf("%q %v", got, err)
	}
	if prompt.Len() != 0 {
		t.Fatalf("a pipe was prompted: %q", prompt.String())
	}
}

// A console end that answers each connection with its data upper-cased.
func TestForwardCarriesConnections(t *testing.T) {
	ended := make(chan struct{})
	var gotHello tunnel.HelloBody
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tk" || r.URL.Path != "/api/v1/clusters/prod/sessions/s1/tunnel" {
			http.Error(w, `{"error":"no"}`, http.StatusUnauthorized)
			return
		}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_, b, _ := c.ReadMessage()
		f, _ := tunnel.Decode(b)
		_ = json.Unmarshal(f.Payload, &gotHello)
		info, _ := json.Marshal(tunnel.InfoBody{Session: "s1", Target: "web/cache-0:6379", Port: 6379, Ends: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), Idle: "10m0s"})
		c.WriteMessage(websocket.BinaryMessage, tunnel.Encode(tunnel.Frame{Type: tunnel.Info, Payload: info}))
		for {
			_, b, err := c.ReadMessage()
			if err != nil {
				return
			}
			f, _ := tunnel.Decode(b)
			switch f.Type {
			case tunnel.Data:
				c.WriteMessage(websocket.BinaryMessage, tunnel.Encode(tunnel.Frame{Type: tunnel.Data, Conn: f.Conn, Payload: []byte(strings.ToUpper(string(f.Payload)))}))
			case tunnel.Close:
				c.WriteMessage(websocket.BinaryMessage, tunnel.Encode(tunnel.Frame{Type: tunnel.End, Payload: []byte("Ended by the owner.")}))
				<-ended
				return
			}
		}
	}))
	defer srv.Close()

	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	errc := make(chan error, 1)
	go func() {
		errc <- forward([]string{srv.URL, "--cluster", "prod", "--session", "s1", "--token", "tk", "--local", strconv.Itoa(port)})
	}()
	var conn net.Conn
	var err error
	for i := 0; i < 100; i++ {
		if conn, err = net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	conn.Write([]byte("ping"))
	buf := make([]byte, 4)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "PING" {
		t.Fatalf("%q %v", buf, err)
	}
	conn.Close()
	close(ended)
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the CLI did not stop when the session ended")
	}
	if gotHello.Version == "" || !strings.HasSuffix(gotHello.Listen, ":"+strconv.Itoa(port)) {
		t.Fatalf("hello %+v", gotHello)
	}
}

func TestForwardSaysWhyTheConsoleRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"There is no such session."}`))
	}))
	defer srv.Close()
	err := forward([]string{srv.URL, "--session", "gone", "--token", "tk"})
	if err == nil || !strings.Contains(err.Error(), "There is no such session.") {
		t.Fatalf("%v", err)
	}
}
