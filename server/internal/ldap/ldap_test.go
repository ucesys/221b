package ldap

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ucesys/221b/server/internal/auth"
)

func hx(s string) []byte {
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		panic(err)
	}
	return b
}

// The encoding against bytes worked out by hand from RFC 4511 and X.690,
// not produced by this package's own decoder.
func TestEncodingMatchesTheRFC(t *testing.T) {
	// An anonymous simple bind, version 3, message 1.
	bind := seq(tagSequence, integer(tagInteger, 1), &element{id: classApplication | constructed | 0, children: []*element{integer(tagInteger, 3), octets(tagOctetString, ""), octets(classContext, "")}})
	if got, want := bind.bytes(), hx("30 0c 02 01 01 60 07 02 01 03 04 00 80 00"); !bytes.Equal(got, want) {
		t.Errorf("bind: % x, want % x", got, want)
	}
	// (cn=foo): context tag 3 holding two octet strings.
	f, err := compileFilter("(cn=foo)")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := f.bytes(), hx("a3 09 04 02 63 6e 04 03 66 6f 6f"); !bytes.Equal(got, want) {
		t.Errorf("(cn=foo): % x, want % x", got, want)
	}
	// (objectClass=*) is a primitive context tag 7 with the attribute.
	f, _ = compileFilter("(objectClass=*)")
	if got, want := f.bytes(), hx("87 0b 6f 62 6a 65 63 74 43 6c 61 73 73"); !bytes.Equal(got, want) {
		t.Errorf("present: % x, want % x", got, want)
	}
	// (&(a=b)(!(c=d))).
	f, _ = compileFilter("(&(a=b)(!(c=d)))")
	if got, want := f.bytes(), hx("a0 12 a3 06 04 01 61 04 01 62 a2 08 a3 06 04 01 63 04 01 64"); !bytes.Equal(got, want) {
		t.Errorf("and/not: % x, want % x", got, want)
	}
	// (cn=a*b*c): initial [0], any [1], final [2] in a SEQUENCE.
	f, _ = compileFilter("(cn=a*b*c)")
	if got, want := f.bytes(), hx("a4 0f 04 02 63 6e 30 09 80 01 61 81 01 62 82 01 63"); !bytes.Equal(got, want) {
		t.Errorf("substrings: % x, want % x", got, want)
	}
	// A length over 127 uses the long form.
	long := octets(tagOctetString, strings.Repeat("x", 300))
	if got := long.bytes(); !bytes.HasPrefix(got, hx("04 82 01 2c")) {
		t.Errorf("long form: % x", got[:6])
	}
	// Integers are minimal and positive.
	if got := integer(tagInteger, 128).bytes(); !bytes.Equal(got, hx("02 02 00 80")) {
		t.Errorf("128: % x", got)
	}
	if got := integer(tagInteger, 300).bytes(); !bytes.Equal(got, hx("02 02 01 2c")) {
		t.Errorf("300: % x", got)
	}
}

func TestFilterEscapesAndRefusesBadFilters(t *testing.T) {
	if got := Escape(`a*(b)\c` + "\x00"); got != `a\2a\28b\29\5cc\00` {
		t.Errorf("Escape = %q", got)
	}
	f, err := compileFilter(`(uid=` + Escape("a)(uid=*") + `)`)
	if err != nil {
		t.Fatal(err)
	}
	if f.id != classContext|constructed|fEquality || string(f.children[1].content) != "a)(uid=*" {
		t.Errorf("an escaped value came back as %q: it can alter the filter", f.children[1].content)
	}
	for _, bad := range []string{"", "uid=x", "(uid=x", "(&)", "(uid)", "(=x)", "(uid=x)(cn=y)", "(!(uid=x)"} {
		if _, err := compileFilter(bad); err == nil {
			t.Errorf("%q compiled", bad)
		}
	}
	// The in-chain match of Active Directory.
	f, err = compileFilter("(member:1.2.840.113556.1.4.1941:=cn=x)")
	if err != nil {
		t.Fatal(err)
	}
	if f.tag() != classContext|fExtensible || string(f.children[0].content) != "1.2.840.113556.1.4.1941" || string(f.children[1].content) != "member" {
		t.Errorf("extensible: %+v", f)
	}
}

func TestDecodeRefusesMalformedInput(t *testing.T) {
	for _, b := range [][]byte{nil, {0x30}, hx("30 05 02 01"), hx("30 84 ff ff ff ff"), hx("1f 01 00"), hx("30 80")} {
		if _, _, err := decode(b, 0); err == nil {
			t.Errorf("% x decoded", b)
		}
	}
	deep := bytes.Repeat(hx("30 80"), 0)
	_ = deep
	nested := []byte{}
	for i := 0; i < 40; i++ {
		nested = append([]byte{0x30, byte(len(nested))}, nested...)
	}
	if _, _, err := decode(nested, 0); err == nil {
		t.Error("a message nested 40 deep decoded")
	}
}

// ---- a directory server for the test ----

type entry struct {
	dn    string
	attrs map[string][]string
}

type server struct {
	t       *testing.T
	ln      net.Listener
	mu      sync.Mutex
	entries []entry
	pw      map[string]string // DN -> password
	binds   []string
	filters []string
	pages   int
	pageMax int
}

func startServer(t *testing.T) *server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &server{t: t, ln: ln, pw: map[string]string{}, pageMax: 2}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *server) opts() Options {
	return Options{
		Config: Config{URL: "ldap://" + s.ln.Addr().String(), StartTLS: true, Timeout: 5 * time.Second},
		BindDN: "cn=search,dc=corp", BindPassword: "search-pw",
		UserBase: "ou=people,dc=corp", GroupBase: "ou=groups,dc=corp", Nested: true, PageSize: 2,
	}
}

func matches(e entry, f *element) bool {
	get := func(a string) []string { return e.attrs[strings.ToLower(a)] }
	switch f.tag() {
	case classContext | fAnd:
		for _, c := range f.children {
			if !matches(e, c) {
				return false
			}
		}
		return true
	case classContext | fOr:
		for _, c := range f.children {
			if matches(e, c) {
				return true
			}
		}
		return false
	case classContext | fNot:
		return !matches(e, f.children[0])
	case classContext | fEquality:
		for _, v := range get(string(f.children[0].content)) {
			if strings.EqualFold(v, string(f.children[1].content)) {
				return true
			}
		}
		return false
	case classContext | fPresent:
		return len(get(string(f.content))) > 0
	}
	return false
}

func (s *server) serve(c net.Conn) {
	defer c.Close()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	bound := ""
	for {
		n, err := c.Read(tmp)
		if err != nil {
			return
		}
		buf = append(buf, tmp[:n]...)
		for {
			msg, rest, err := decode(buf, 0)
			if err != nil {
				break
			}
			buf = rest
			id, _ := msg.children[0].int()
			op := msg.children[1]
			reply := func(op *element, controls ...*element) {
				m := seq(tagSequence, integer(tagInteger, id), op)
				if len(controls) > 0 {
					m.children = append(m.children, &element{id: classContext | constructed, children: controls})
				}
				_, _ = c.Write(m.bytes())
			}
			done := func(tag byte, code int, text string) *element {
				return &element{id: classApplication | constructed | tag, children: []*element{integer(tagEnumerated, code), octets(tagOctetString, ""), octets(tagOctetString, text)}}
			}
			switch op.id {
			case classApplication | constructed | 0: // bind
				dn, pw := string(op.children[1].content), string(op.children[2].content)
				s.mu.Lock()
				s.binds = append(s.binds, dn)
				want, ok := s.pw[dn]
				s.mu.Unlock()
				if pw == "" && dn != "" || !ok || want != pw {
					reply(done(1, ResultInvalidCredentials, "invalid credentials"))
					continue
				}
				bound = dn
				reply(done(1, ResultSuccess, ""))
			case classApplication | constructed | 23: // StartTLS: this server has no certificate
				reply(done(24, 2, "no TLS in the test server"))
			case classApplication | 2:
				return
			case classApplication | constructed | 3: // search
				if bound == "" {
					reply(done(5, 50, "bind first"))
					continue
				}
				s.search(op, msg, reply, done)
			}
		}
	}
}

func (s *server) search(op, msg *element, reply func(*element, ...*element), done func(byte, int, string) *element) {
	base := strings.ToLower(string(op.children[0].content))
	scope, _ := op.children[1].int()
	filter := op.children[6]
	var want []string
	for _, a := range op.children[7].children {
		want = append(want, strings.ToLower(string(a.content)))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.filters = append(s.filters, string(op.children[6].bytes()))
	var hits []entry
	for _, e := range s.entries {
		dn := strings.ToLower(e.dn)
		inBase := dn == base || (scope != ScopeBase && strings.HasSuffix(dn, ","+base))
		if inBase && matches(e, filter) {
			hits = append(hits, e)
		}
	}
	if scope == ScopeBase && len(hits) == 0 {
		reply(done(5, ResultNoSuchObject, "no such object"))
		return
	}
	start := 0
	var paged bool
	if len(msg.children) > 2 {
		for _, ctl := range msg.children[2].children {
			if string(ctl.children[0].content) == oidPaged {
				paged = true
				val, _, _ := decode(ctl.children[len(ctl.children)-1].content, 0)
				if ck := val.children[1].content; len(ck) > 0 {
					start = int(ck[0])
				}
			}
		}
	}
	end := len(hits)
	var cookie []byte
	if paged && end-start > s.pageMax {
		end = start + s.pageMax
		cookie = []byte{byte(end)}
	}
	if paged {
		s.pages++
	}
	for _, e := range hits[start:end] {
		attrs := &element{id: tagSequence | constructed}
		for name, vals := range e.attrs {
			if len(want) > 0 && !slices.Contains(want, name) {
				continue
			}
			set := &element{id: tagSet | constructed}
			for _, v := range vals {
				set.children = append(set.children, octets(tagOctetString, v))
			}
			attrs.children = append(attrs.children, seq(tagSequence, octets(tagOctetString, name), set))
		}
		reply(&element{id: classApplication | constructed | 4, children: []*element{octets(tagOctetString, e.dn), attrs}})
	}
	var ctls []*element
	if paged {
		val := seq(tagSequence, integer(tagInteger, len(hits)), octets(tagOctetString, string(cookie)))
		ctls = append(ctls, seq(tagSequence, octets(tagOctetString, oidPaged), octets(tagOctetString, string(val.bytes()))))
	}
	reply(done(5, ResultSuccess, ""), ctls...)
}

func person(uid, name, mail string, memberOf ...string) entry {
	return entry{dn: "uid=" + uid + ",ou=people,dc=corp", attrs: map[string][]string{"uid": {uid}, "cn": {name}, "mail": {mail}, "objectclass": {"person"}}}
}

func group(cn string, members ...string) entry {
	return entry{dn: "cn=" + cn + ",ou=groups,dc=corp", attrs: map[string][]string{"cn": {cn}, "member": members, "objectclass": {"groupOfNames"}}}
}

func fixture(s *server) {
	s.pw["cn=search,dc=corp"] = "search-pw"
	s.pw["uid=priya,ou=people,dc=corp"] = "priya-pw"
	s.pw["uid=omar,ou=people,dc=corp"] = "omar-pw"
	s.entries = []entry{
		{dn: "cn=search,dc=corp", attrs: map[string][]string{"cn": {"search"}}},
		person("priya", "Priya Raman", "priya@corp.example"),
		person("omar", "Omar", "omar@corp.example"),
		person("lee", "Lee", "lee@corp.example"),
		group("platform-admins", "uid=priya,ou=people,dc=corp"),
		group("team-a-ops", "uid=omar,ou=people,dc=corp", "cn=contractors,ou=groups,dc=corp"),
		group("contractors", "uid=lee,ou=people,dc=corp"),
		group("everyone", "cn=platform-admins,ou=groups,dc=corp", "cn=team-a-ops,ou=groups,dc=corp"),
		group("empty"),
	}
}

func TestDirectoryRefusesCleartextWithoutStartTLS(t *testing.T) {
	o := Options{Config: Config{URL: "ldap://ldap.example"}, UserBase: "a", GroupBase: "b"}
	if _, err := New(o); err == nil || !strings.Contains(err.Error(), "clear") {
		t.Fatalf("ldap:// without startTLS: %v", err)
	}
	o.URL = "ldaps://ldap.example"
	if _, err := New(o); err != nil {
		t.Fatalf("ldaps://: %v", err)
	}
	o.UserFilter = "(uid=x)"
	if _, err := New(o); err == nil {
		t.Fatal("a filter with no placeholder was accepted")
	}
}

// The test server has no TLS, so these use ldap:// with StartTLS off, which
// New refuses; the options are made by hand, as a deployment cannot.
func directory(s *server) *Directory {
	o := s.opts()
	o.StartTLS = false
	return &Directory{o: withDefaults(o)}
}

func withDefaults(o Options) Options {
	o.UserFilter, o.UsernameAttr, o.NameAttr, o.EmailAttr = "(uid=%s)", "uid", "cn", "mail"
	o.GroupFilter, o.GroupAttr, o.MemberAttr = "(member=%s)", "cn", "member"
	return o
}

func TestAuthenticateFindsBindsAsThePersonAndResolvesNestedGroups(t *testing.T) {
	s := startServer(t)
	fixture(s)
	d := directory(s)
	ctx := context.Background()

	p, err := d.Authenticate(ctx, "omar", "omar-pw")
	if err != nil {
		t.Fatal(err)
	}
	if p.Username != "omar" || p.Name != "Omar" || p.Email != "omar@corp.example" || p.Ref != "uid=omar,ou=people,dc=corp" {
		t.Errorf("person: %+v", p)
	}
	// Direct: team-a-ops. Nested: everyone, through team-a-ops.
	if !slices.Equal(p.Groups, []string{"everyone", "team-a-ops"}) {
		t.Errorf("groups: %v", p.Groups)
	}
	s.mu.Lock()
	binds := slices.Clone(s.binds)
	s.mu.Unlock()
	if !slices.Contains(binds, "uid=omar,ou=people,dc=corp") || binds[0] != "cn=search,dc=corp" {
		t.Errorf("binds: %v", binds)
	}

	// Without nesting only the direct groups.
	d.o.Nested = false
	if p, _ = d.Authenticate(ctx, "omar", "omar-pw"); !slices.Equal(p.Groups, []string{"team-a-ops"}) {
		t.Errorf("flat groups: %v", p.Groups)
	}
	d.o.Nested = true

	// Wrong password, unknown person, empty password and a filter-breaking
	// name are all the same refusal.
	for _, c := range [][2]string{{"omar", "nope"}, {"nobody", "x"}, {"omar", ""}, {"", "x"}, {"*", "omar-pw"}, {"omar)(uid=*", "omar-pw"}, {"o*", "omar-pw"}} {
		if _, err := d.Authenticate(ctx, c[0], c[1]); !errors.Is(err, auth.ErrBadCredentials) {
			t.Errorf("%q: %v", c, err)
		}
	}
	// An empty password never reaches the server as a bind: LDAP would take
	// it as an anonymous bind and succeed.
	s.mu.Lock()
	before := len(s.binds)
	s.mu.Unlock()
	if _, err := d.Authenticate(ctx, "omar", ""); !errors.Is(err, auth.ErrBadCredentials) {
		t.Errorf("empty password: %v", err)
	}
	s.mu.Lock()
	if len(s.binds) != before {
		t.Errorf("an empty password caused a bind: %v", s.binds[before:])
	}
	s.mu.Unlock()
	c, err := Dial(ctx, d.o.Config)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Bind("uid=omar,ou=people,dc=corp", ""); err == nil {
		t.Error("Bind accepted an empty password for a DN")
	}
	c.Close()

	// Groups again, from the reference.
	g, err := d.Groups(ctx, "uid=lee,ou=people,dc=corp")
	if err != nil || !slices.Equal(g, []string{"contractors", "everyone", "team-a-ops"}) {
		t.Errorf("Groups: %v %v", g, err)
	}
}

func TestMembersWalksListedGroupsAndNestedOnes(t *testing.T) {
	s := startServer(t)
	fixture(s)
	d := directory(s)
	got, err := d.Members(context.Background(), []string{"platform-admins", "team-a-ops", "empty", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string][]string{}
	for _, p := range got {
		by[p.Username] = p.Groups
	}
	if len(got) != 3 || !slices.Equal(by["priya"], []string{"platform-admins"}) || !slices.Equal(by["omar"], []string{"team-a-ops"}) || !slices.Equal(by["lee"], []string{"team-a-ops"}) {
		t.Fatalf("members: %+v", got)
	}
	d.o.Nested = false
	got, _ = d.Members(context.Background(), []string{"team-a-ops"})
	if len(got) != 1 || got[0].Username != "omar" {
		t.Fatalf("flat members: %+v", got)
	}
}

func TestSearchFollowsThePages(t *testing.T) {
	s := startServer(t)
	for i := 0; i < 7; i++ {
		s.entries = append(s.entries, person("u"+string(rune('a'+i)), "U", "u@x"))
	}
	s.pw["cn=search,dc=corp"] = "search-pw"
	s.entries = append(s.entries, entry{dn: "cn=search,dc=corp", attrs: map[string][]string{"cn": {"search"}}})
	d := directory(s)
	c, err := d.service(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	es, err := c.Search(SearchRequest{Base: "ou=people,dc=corp", Scope: ScopeSub, Filter: "(objectClass=person)", Attrs: []string{"uid"}, PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 7 || s.pages != 4 {
		t.Fatalf("%d entries in %d pages", len(es), s.pages)
	}
}

func TestDirectoryDownAndSearchAccountRefused(t *testing.T) {
	s := startServer(t)
	fixture(s)
	d := directory(s)
	d.o.BindPassword = "wrong"
	if _, err := d.Authenticate(context.Background(), "omar", "omar-pw"); !errors.Is(err, auth.ErrDirectoryDown) {
		t.Errorf("a refused search account: %v (people must not be told they typed the wrong password)", err)
	}
	s.ln.Close()
	d.o.BindPassword = "search-pw"
	if _, err := d.Authenticate(context.Background(), "omar", "omar-pw"); !errors.Is(err, auth.ErrDirectoryDown) {
		t.Errorf("a server that is gone: %v", err)
	}
}
