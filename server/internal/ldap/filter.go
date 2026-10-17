package ldap

import (
	"fmt"
	"strings"
)

// Filter strings are RFC 4515's: (&(a=b)(|(c=d)(!(e=*)))) and the
// extensible match the directory servers use for nested groups.

// Escape makes a value safe to put in a filter: the characters with a
// meaning in a filter, and NUL, become \hh (RFC 4515 section 3).
func Escape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch c {
		case '\\', '*', '(', ')', 0:
			fmt.Fprintf(&b, "\\%02x", c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Filter tags, context-specific (RFC 4511 section 4.5.1).
const (
	fAnd        = 0
	fOr         = 1
	fNot        = 2
	fEquality   = 3
	fSubstrings = 4
	fGE         = 5
	fLE         = 6
	fPresent    = 7
	fApprox     = 8
	fExtensible = 9
)

// compileFilter turns a filter string into its BER form.
func compileFilter(s string) (*element, error) {
	s = strings.TrimSpace(s)
	e, rest, err := parseFilter(s)
	if err != nil {
		return nil, fmt.Errorf("the filter %q: %w", s, err)
	}
	if strings.TrimSpace(rest) != "" {
		return nil, fmt.Errorf("the filter %q: unexpected %q after the filter", s, rest)
	}
	return e, nil
}

func parseFilter(s string) (*element, string, error) {
	if !strings.HasPrefix(s, "(") {
		return nil, "", fmt.Errorf("a filter starts with (")
	}
	s = s[1:]
	if s == "" {
		return nil, "", fmt.Errorf("a filter that ends after (")
	}
	switch s[0] {
	case '&', '|':
		id := byte(fAnd)
		if s[0] == '|' {
			id = fOr
		}
		set := &element{id: classContext | constructed | id}
		s = s[1:]
		for strings.HasPrefix(s, "(") {
			c, rest, err := parseFilter(s)
			if err != nil {
				return nil, "", err
			}
			set.children = append(set.children, c)
			s = rest
		}
		if len(set.children) == 0 {
			return nil, "", fmt.Errorf("an empty & or |")
		}
		if !strings.HasPrefix(s, ")") {
			return nil, "", fmt.Errorf("a missing )")
		}
		return set, s[1:], nil
	case '!':
		c, rest, err := parseFilter(s[1:])
		if err != nil {
			return nil, "", err
		}
		if !strings.HasPrefix(rest, ")") {
			return nil, "", fmt.Errorf("a missing ) after !")
		}
		return &element{id: classContext | constructed | fNot, children: []*element{c}}, rest[1:], nil
	}
	end := strings.IndexByte(s, ')')
	// A value may hold an escaped ) as \29, never a bare one.
	if end < 0 {
		return nil, "", fmt.Errorf("a missing )")
	}
	item, rest := s[:end], s[end+1:]
	e, err := parseItem(item)
	return e, rest, err
}

func unescape(v string) ([]byte, error) {
	var out []byte
	for i := 0; i < len(v); i++ {
		if v[i] != '\\' {
			out = append(out, v[i])
			continue
		}
		if i+3 > len(v) {
			return nil, fmt.Errorf("a bad escape in %q", v)
		}
		var b byte
		if _, err := fmt.Sscanf(v[i+1:i+3], "%02x", &b); err != nil {
			return nil, fmt.Errorf("a bad escape in %q", v)
		}
		out = append(out, b)
		i += 2
	}
	return out, nil
}

func parseItem(item string) (*element, error) {
	eq := strings.IndexByte(item, '=')
	if eq <= 0 {
		return nil, fmt.Errorf("%q is not attribute=value", item)
	}
	attr, val := item[:eq], item[eq+1:]
	op := byte(fEquality)
	switch attr[len(attr)-1] {
	case '>':
		op, attr = fGE, attr[:len(attr)-1]
	case '<':
		op, attr = fLE, attr[:len(attr)-1]
	case '~':
		op, attr = fApprox, attr[:len(attr)-1]
	case ':':
		return parseExtensible(attr[:len(attr)-1], val)
	}
	if attr == "" {
		return nil, fmt.Errorf("%q has no attribute", item)
	}
	if op == fEquality && val == "*" {
		return &element{id: classContext | fPresent, content: []byte(attr)}, nil
	}
	if op == fEquality && strings.Contains(val, "*") {
		return parseSubstrings(attr, val)
	}
	raw, err := unescape(val)
	if err != nil {
		return nil, err
	}
	return &element{id: classContext | constructed | op, children: []*element{octets(tagOctetString, attr), {id: tagOctetString, content: raw}}}, nil
}

func parseSubstrings(attr, val string) (*element, error) {
	parts := strings.Split(val, "*")
	subs := &element{id: tagSequence | constructed}
	for i, p := range parts {
		if p == "" {
			continue
		}
		raw, err := unescape(p)
		if err != nil {
			return nil, err
		}
		tag := byte(1) // initial is [0], any [1], final [2]
		switch {
		case i == 0:
			tag = 0
		case i == len(parts)-1:
			tag = 2
		default:
			tag = 1
		}
		subs.children = append(subs.children, &element{id: classContext | tag, content: raw})
	}
	if len(subs.children) == 0 {
		return nil, fmt.Errorf("a substring filter with nothing to match")
	}
	return &element{id: classContext | constructed | fSubstrings, children: []*element{octets(tagOctetString, attr), subs}}, nil
}

// parseExtensible reads (attr:dn:rule:=value), with each part optional but
// the value: the form of the in-chain match that Active Directory uses.
func parseExtensible(head, val string) (*element, error) {
	raw, err := unescape(val)
	if err != nil {
		return nil, err
	}
	var attr, rule string
	dn := false
	for i, p := range strings.Split(head, ":") {
		switch {
		case i == 0 && !strings.EqualFold(p, "dn") && !isOID(p):
			attr = p
		case strings.EqualFold(p, "dn"):
			dn = true
		case p != "":
			rule = p
		}
	}
	e := &element{id: classContext | constructed | fExtensible}
	if rule != "" {
		e.children = append(e.children, octets(classContext|1, rule))
	}
	if attr != "" {
		e.children = append(e.children, octets(classContext|2, attr))
	}
	e.children = append(e.children, &element{id: classContext | 3, content: raw})
	if dn {
		e.children = append(e.children, &element{id: classContext | 4, content: []byte{0xff}})
	}
	return e, nil
}

func isOID(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && c != '.' {
			return false
		}
	}
	return true
}
