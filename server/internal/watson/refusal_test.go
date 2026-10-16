package watson

import (
	"bytes"
	"sync/atomic"
	"testing"
)

// kubectl cordon prints "error: unable to cordon node" and returns success
// when the API server refuses; the sniffer is what turns that into a failure.
func TestRefusalSnifferNotesAUnableLine(t *testing.T) {
	var hit atomic.Bool
	var buf bytes.Buffer
	w := &refusalSniffer{w: &buf, hit: &hit}
	w.Write([]byte("node/a cordoned\n"))
	if hit.Load() {
		t.Fatal("a success line was taken for a refusal")
	}
	line := "error: unable to cordon node \"a\": nodes \"a\" is forbidden\n"
	w.Write([]byte(line))
	if !hit.Load() {
		t.Fatal("the refusal was not noticed")
	}
	if !bytes.Contains(buf.Bytes(), []byte(line)) {
		t.Fatalf("the line was not passed through: %q", buf.String())
	}
}
