package ldap

import (
	"bytes"
	"errors"
	"fmt"
)

// A small BER encoder and decoder: only what LDAP's messages use.

// Tag classes and the tags of the universal types used.
const (
	classUniversal   = 0x00
	classApplication = 0x40
	classContext     = 0x80
	constructed      = 0x20

	tagBoolean     = 0x01
	tagInteger     = 0x02
	tagOctetString = 0x04
	tagEnumerated  = 0x0a
	tagSequence    = 0x10
	tagSet         = 0x11
)

// element is a BER value: an identifier octet, and its content.
type element struct {
	id       byte
	content  []byte
	children []*element
}

func (e *element) constructed() bool { return e.id&constructed != 0 }

// tag is the identifier without the constructed bit.
func (e *element) tag() byte { return e.id &^ constructed }

func encodeLength(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	var b []byte
	for v := n; v > 0; v >>= 8 {
		b = append([]byte{byte(v)}, b...)
	}
	return append([]byte{0x80 | byte(len(b))}, b...)
}

// bytes encodes the element.
func (e *element) bytes() []byte {
	content := e.content
	if e.constructed() {
		var b bytes.Buffer
		for _, c := range e.children {
			b.Write(c.bytes())
		}
		content = b.Bytes()
	}
	out := []byte{e.id}
	out = append(out, encodeLength(len(content))...)
	return append(out, content...)
}

func seq(id byte, children ...*element) *element {
	return &element{id: id | constructed, children: children}
}

func octets(id byte, s string) *element { return &element{id: id, content: []byte(s)} }

func integer(id byte, v int) *element {
	var b []byte
	switch {
	case v == 0:
		b = []byte{0}
	case v > 0:
		for x := v; x > 0; x >>= 8 {
			b = append([]byte{byte(x)}, b...)
		}
		if b[0]&0x80 != 0 {
			b = append([]byte{0}, b...)
		}
	default:
		// Negative values are not used by LDAP's requests.
		b = []byte{0}
	}
	return &element{id: id, content: b}
}

func boolean(v bool) *element {
	b := byte(0)
	if v {
		b = 0xff
	}
	return &element{id: tagBoolean, content: []byte{b}}
}

func (e *element) int() (int, error) {
	if len(e.content) == 0 || len(e.content) > 4 {
		return 0, errors.New("ldap: an integer of an unexpected size")
	}
	v := 0
	if e.content[0]&0x80 != 0 {
		v = -1
	}
	for _, c := range e.content {
		v = v<<8 | int(c)
	}
	return v, nil
}

// maxMessage bounds what the client will read from one element.
const maxMessage = 16 << 20

// decode reads one element from the front of b and returns the rest.
func decode(b []byte, depth int) (*element, []byte, error) {
	if depth > 32 {
		return nil, nil, errors.New("ldap: a message nested too deep")
	}
	if len(b) < 2 {
		return nil, nil, errors.New("ldap: a truncated message")
	}
	id := b[0]
	if id&0x1f == 0x1f {
		return nil, nil, errors.New("ldap: a multi-byte tag, which LDAP does not use")
	}
	l, n := int(b[1]), 2
	if l&0x80 != 0 {
		k := l & 0x7f
		if k == 0 || k > 4 || len(b) < 2+k {
			return nil, nil, errors.New("ldap: a bad length")
		}
		l = 0
		for _, c := range b[2 : 2+k] {
			l = l<<8 | int(c)
		}
		n = 2 + k
	}
	if l < 0 || l > maxMessage || len(b) < n+l {
		return nil, nil, fmt.Errorf("ldap: a message of %d bytes is cut short or too large", l)
	}
	e := &element{id: id}
	body := b[n : n+l]
	if id&constructed != 0 {
		for len(body) > 0 {
			c, rest, err := decode(body, depth+1)
			if err != nil {
				return nil, nil, err
			}
			e.children = append(e.children, c)
			body = rest
		}
	} else {
		e.content = body
	}
	return e, b[n+l:], nil
}
