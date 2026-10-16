package ldap

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ucesys/221b/server/internal/auth"
)

// Options configure the directory (auth.ldap in values, RB-19).
type Options struct {
	Config
	// BindDN and BindPassword are the account that searches. The password
	// comes from a Secret and goes nowhere else (X-04).
	BindDN       string
	BindPassword string

	UserBase     string
	UserFilter   string // (uid=%s): %s is the login, escaped
	UsernameAttr string // uid
	NameAttr     string // cn
	EmailAttr    string // mail

	GroupBase   string
	GroupFilter string // (member=%s): %s is the DN of the person or group, escaped
	GroupAttr   string // cn
	MemberAttr  string // member
	// Nested resolves groups of groups (RB-22).
	Nested   bool
	PageSize int
}

// Directory is an LDAP server as 221B's sign-in uses it.
type Directory struct {
	o Options
}

var _ auth.Directory = (*Directory)(nil)

// New checks the options and returns the directory. Nothing is sent yet.
func New(o Options) (*Directory, error) {
	if o.URL == "" {
		return nil, errors.New("auth.ldap.url is needed")
	}
	if o.UserBase == "" || o.GroupBase == "" {
		return nil, errors.New("auth.ldap.userSearch.base and groupSearch.base are needed")
	}
	if strings.HasPrefix(o.URL, "ldap://") && !o.StartTLS {
		return nil, errors.New("auth.ldap.url is ldap:// without startTLS: passwords would travel in the clear; use ldaps:// or turn startTLS on")
	}
	def := func(p *string, v string) {
		if *p == "" {
			*p = v
		}
	}
	def(&o.UserFilter, "(uid=%s)")
	def(&o.UsernameAttr, "uid")
	def(&o.NameAttr, "cn")
	def(&o.EmailAttr, "mail")
	def(&o.GroupFilter, "(member=%s)")
	def(&o.GroupAttr, "cn")
	def(&o.MemberAttr, "member")
	if !strings.Contains(o.UserFilter, "%s") || !strings.Contains(o.GroupFilter, "%s") {
		return nil, errors.New("auth.ldap userSearch.filter and groupSearch.filter must contain %s")
	}
	if o.PageSize == 0 {
		o.PageSize = 500
	}
	if _, err := compileFilter(strings.ReplaceAll(o.UserFilter, "%s", "x")); err != nil {
		return nil, fmt.Errorf("auth.ldap.userSearch.filter: %w", err)
	}
	if _, err := compileFilter(strings.ReplaceAll(o.GroupFilter, "%s", "x")); err != nil {
		return nil, fmt.Errorf("auth.ldap.groupSearch.filter: %w", err)
	}
	return &Directory{o: o}, nil
}

// URL is the server.
func (d *Directory) URL() string { return d.o.URL }

// down says an error is the network's or the server's, not a refusal.
func down(err error) error {
	var le *Error
	if errors.As(err, &le) && le.Code != ResultSizeLimit {
		return err
	}
	return fmt.Errorf("%w: %s", auth.ErrDirectoryDown, err.Error())
}

// service opens a connection bound as the search account.
func (d *Directory) service(ctx context.Context) (*Conn, error) {
	c, err := Dial(ctx, d.o.Config)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", auth.ErrDirectoryDown, err.Error())
	}
	if d.o.BindDN != "" {
		if err := c.Bind(d.o.BindDN, d.o.BindPassword); err != nil {
			c.Close()
			var le *Error
			if errors.As(err, &le) {
				return nil, fmt.Errorf("%w: the search account was refused (%s)", auth.ErrDirectoryDown, le.Error())
			}
			return nil, fmt.Errorf("%w: %s", auth.ErrDirectoryDown, err.Error())
		}
	}
	return c, nil
}

func (d *Directory) attrs() []string {
	return []string{d.o.UsernameAttr, d.o.NameAttr, d.o.EmailAttr}
}

func (d *Directory) person(e Entry) auth.Person {
	return auth.Person{Ref: e.DN, Username: e.First(d.o.UsernameAttr), Name: e.First(d.o.NameAttr), Email: e.First(d.o.EmailAttr)}
}

// Authenticate finds the person, binds as them, and reads their groups.
func (d *Directory) Authenticate(ctx context.Context, login, password string) (auth.Person, error) {
	if login == "" || password == "" {
		return auth.Person{}, auth.ErrBadCredentials
	}
	svc, err := d.service(ctx)
	if err != nil {
		return auth.Person{}, err
	}
	defer svc.Close()
	found, err := svc.Search(SearchRequest{Base: d.o.UserBase, Scope: ScopeSub, Filter: strings.ReplaceAll(d.o.UserFilter, "%s", Escape(login)), Attrs: d.attrs(), SizeLimit: 2})
	if err != nil {
		return auth.Person{}, down(err)
	}
	if len(found) != 1 {
		// None, or several: either way this login names no one person.
		return auth.Person{}, auth.ErrBadCredentials
	}
	p := d.person(found[0])
	if p.Username == "" {
		p.Username = login
	}
	// The person's own bind, on a connection of its own, proves the password.
	user, err := Dial(ctx, d.o.Config)
	if err != nil {
		return auth.Person{}, fmt.Errorf("%w: %s", auth.ErrDirectoryDown, err.Error())
	}
	berr := user.Bind(p.Ref, password)
	user.Close()
	if berr != nil {
		var le *Error
		if errors.As(berr, &le) {
			return auth.Person{}, auth.ErrBadCredentials
		}
		return auth.Person{}, down(berr)
	}
	groups, err := d.groupsOf(svc, p.Ref)
	if err != nil {
		return auth.Person{}, down(err)
	}
	p.Groups = groups
	return p, nil
}

// groupsOf lists the names of the groups a DN is in; with nested groups on,
// the groups of those groups too, until nothing new turns up.
func (d *Directory) groupsOf(c *Conn, dn string) ([]string, error) {
	var names []string
	seen := map[string]bool{}
	queue := []string{dn}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		es, err := c.Search(SearchRequest{Base: d.o.GroupBase, Scope: ScopeSub, Filter: strings.ReplaceAll(d.o.GroupFilter, "%s", Escape(next)), Attrs: []string{d.o.GroupAttr}, PageSize: d.o.PageSize})
		if err != nil {
			return nil, err
		}
		for _, e := range es {
			key := strings.ToLower(e.DN)
			if seen[key] {
				continue
			}
			seen[key] = true
			if n := e.First(d.o.GroupAttr); n != "" && !slices.Contains(names, n) {
				names = append(names, n)
			}
			if d.o.Nested {
				queue = append(queue, e.DN)
			}
		}
	}
	slices.Sort(names)
	return names, nil
}

// Groups reads a person's groups again.
func (d *Directory) Groups(ctx context.Context, ref string) ([]string, error) {
	svc, err := d.service(ctx)
	if err != nil {
		return nil, err
	}
	defer svc.Close()
	g, err := d.groupsOf(svc, ref)
	if err != nil {
		return nil, down(err)
	}
	return g, nil
}

// Members reads the people in the listed groups, nested groups included
// when that is on, and tells each the listed groups they are in.
func (d *Directory) Members(ctx context.Context, groups []string) ([]auth.Person, error) {
	svc, err := d.service(ctx)
	if err != nil {
		return nil, err
	}
	defer svc.Close()
	people := map[string]*auth.Person{}
	var order []string
	for _, name := range groups {
		es, err := svc.Search(SearchRequest{Base: d.o.GroupBase, Scope: ScopeSub, Filter: "(" + d.o.GroupAttr + "=" + Escape(name) + ")", Attrs: []string{d.o.GroupAttr, d.o.MemberAttr}, PageSize: d.o.PageSize})
		if err != nil {
			return nil, down(err)
		}
		visited := map[string]bool{}
		for _, g := range es {
			if err := d.walk(svc, g.Values(d.o.MemberAttr), name, visited, people, &order, 0); err != nil {
				return nil, down(err)
			}
		}
	}
	out := make([]auth.Person, 0, len(order))
	for _, k := range order {
		p := *people[k]
		slices.Sort(p.Groups)
		out = append(out, p)
	}
	return out, nil
}

// walk adds the people among member DNs, and goes into member groups when
// nesting is on.
func (d *Directory) walk(c *Conn, members []string, listed string, visited map[string]bool, people map[string]*auth.Person, order *[]string, depth int) error {
	if depth > 10 {
		return nil
	}
	for _, m := range members {
		key := strings.ToLower(m)
		if visited[key] {
			continue
		}
		visited[key] = true
		es, err := c.Search(SearchRequest{Base: m, Scope: ScopeBase, Filter: "(objectClass=*)", Attrs: append(d.attrs(), d.o.MemberAttr)})
		if err != nil {
			var le *Error
			if errors.As(err, &le) && le.Code == ResultNoSuchObject {
				continue
			}
			return err
		}
		for _, e := range es {
			if nested := e.Values(d.o.MemberAttr); len(nested) > 0 {
				if d.o.Nested {
					if err := d.walk(c, nested, listed, visited, people, order, depth+1); err != nil {
						return err
					}
				}
				continue
			}
			p := d.person(e)
			if p.Username == "" {
				continue
			}
			cur, ok := people[strings.ToLower(e.DN)]
			if !ok {
				cur = &p
				people[strings.ToLower(e.DN)] = cur
				*order = append(*order, strings.ToLower(e.DN))
			}
			if !slices.Contains(cur.Groups, listed) {
				cur.Groups = append(cur.Groups, listed)
			}
		}
	}
	return nil
}
