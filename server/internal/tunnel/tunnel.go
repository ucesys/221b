// Package tunnel is the framing between the console and the 221b CLI for
// a port forward (TB-19): many TCP connections over one WebSocket, each
// frame a type, a connection number and a payload.
package tunnel

import (
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
)

// Frame types.
const (
	// Hello is the CLI's first frame: its version, its host and the
	// address it listens on, as JSON.
	Hello byte = 1
	// Info is the console's answer to Hello: the session, as JSON.
	Info byte = 2
	// Open is a new connection on the CLI's port; the payload is the
	// address it came from.
	Open byte = 3
	// Data carries bytes of a connection, either way.
	Data byte = 4
	// Close ends a connection, either way; from the console the payload
	// may say why.
	Close byte = 5
	// End says the session is over; the payload says why.
	End byte = 6
)

// Frame is one message.
type Frame struct {
	Type    byte
	Conn    uint32
	Payload []byte
}

// Encode writes a frame as one WebSocket message.
func Encode(f Frame) []byte {
	b := make([]byte, 5+len(f.Payload))
	b[0] = f.Type
	binary.BigEndian.PutUint32(b[1:5], f.Conn)
	copy(b[5:], f.Payload)
	return b
}

// ErrShort is a message too short to be a frame.
var ErrShort = errors.New("tunnel: a message shorter than a frame header")

// Decode reads one WebSocket message.
func Decode(b []byte) (Frame, error) {
	if len(b) < 5 {
		return Frame{}, ErrShort
	}
	return Frame{Type: b[0], Conn: binary.BigEndian.Uint32(b[1:5]), Payload: b[5:]}, nil
}

// HelloBody is the CLI's Hello.
type HelloBody struct {
	Version string `json:"version"`
	Host    string `json:"host"`
	Listen  string `json:"listen"`
}

// InfoBody is the console's Info.
type InfoBody struct {
	Session string `json:"session"`
	Target  string `json:"target"`
	Port    int    `json:"port"`
	Ends    string `json:"ends"`
	Idle    string `json:"idle"`
}

// MaxData is the most a Data frame carries.
const MaxData = 32 << 10

// Older reports whether version a is older than b (both "major.minor.patch",
// a leading v allowed; a version that does not parse is older).
func Older(a, b string) bool {
	pa, oka := parse(a)
	pb, okb := parse(b)
	if !okb {
		return false
	}
	if !oka {
		return true
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

// DevBuild says whether a CLI was built from a checkout rather than a
// release ("dev", or git describe's "v0.7.1-12-gabc"): the minimum
// version does not apply to it.
func DevBuild(v string) bool {
	v = strings.TrimSpace(v)
	return v == "dev" || strings.Contains(v, "-g") || strings.HasSuffix(v, "-dirty")
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	v, _, _ = strings.Cut(v, "-")
	parts := strings.Split(v, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
