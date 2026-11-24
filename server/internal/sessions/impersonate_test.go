package sessions

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/textproto"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/ucesys/221b/server/internal/kube"
)

// TestImpersonatedGroupIsTrimmed settles the step finding 1 of the roles
// review could not verify: an exec upgrade asserting the group
// " system:masters" puts "system:masters" on the wire, so a padded name is
// the reserved one by the time an API server reads it. roles.Reserved
// refuses such names for that reason.
func TestImpersonatedGroupIsTrimmed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan []string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- nil
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		var raw []string
		r := bufio.NewReader(c)
		for {
			line, err := r.ReadString('\n')
			if err != nil || line == "\r\n" {
				break
			}
			if k, v, ok := strings.Cut(line, ":"); ok && textproto.CanonicalMIMEHeaderKey(k) == "Impersonate-Group" {
				// The value as written, after the one space Go puts after the colon.
				raw = append(raw, strings.TrimSuffix(strings.TrimPrefix(v, " "), "\r\n"))
			}
		}
		got <- raw
	}()
	b, err := NewKubeBackend(&rest.Config{Host: "http://" + ln.Addr().String()}, kube.Person{User: "alice", Groups: []string{" system:masters"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = b.Exec(ctx, "web", "p", "", []string{"id"}, false, nil, io.Discard, io.Discard, nil)
	groups := <-got
	if !slices.Contains(groups, "system:masters") {
		t.Fatalf("Impersonate-Group on the wire = %q; the header is no longer trimmed, update the comment on roles.Reserved", groups)
	}
}
