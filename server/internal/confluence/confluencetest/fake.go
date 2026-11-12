// Package confluencetest is a fake Confluence for tests: spaces and pages
// in memory, reached as Cloud (REST v2 under /wiki/api/v2, with basic
// authentication) or as Data Center (REST under /rest/api, with a bearer
// token), with page versions checked on every update as Confluence does.
package confluencetest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Page is what the fake keeps of a page.
type Page struct {
	ID       string
	Space    string
	ParentID string
	Title    string
	Version  int
	Modified time.Time
	Body     string
}

// Fake is the server.
type Fake struct {
	*httptest.Server
	// Email and Token are what requests must carry: basic email:token on
	// Cloud paths, a bearer Token on Data Center paths.
	Email, Token string
	// Throttle answers this many requests with 429 before serving any.
	Throttle int
	// Fail, when set, answers a request instead (a status, or 0 to serve).
	Fail func(r *http.Request) int
	Now  func() time.Time

	mu       sync.Mutex
	spaces   map[string]string // key → id
	homes    map[string]string // key → home page id
	pages    map[string]*Page
	next     int
	Requests map[string]int
}

// New starts a fake.
func New(email, token string) *Fake {
	f := &Fake{Email: email, Token: token, spaces: map[string]string{}, homes: map[string]string{}, pages: map[string]*Page{}, next: 100, Requests: map[string]int{}, Now: time.Now}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

// NewTLS starts a fake on HTTPS; its Client trusts it.
func NewTLS(email, token string) *Fake {
	f := &Fake{Email: email, Token: token, spaces: map[string]string{}, homes: map[string]string{}, pages: map[string]*Page{}, next: 100, Requests: map[string]int{}, Now: time.Now}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	return f
}

// AddSpace adds a space with a home page, and returns the home's id.
func (f *Fake) AddSpace(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	f.spaces[key] = strconv.Itoa(f.next)
	home := f.addLocked(key, "", key+" Home", "")
	f.homes[key] = home
	return home
}

// AddPage adds a page as if a person wrote it, and returns its id.
func (f *Fake) AddPage(space, parent, title, body string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addLocked(space, parent, title, body)
}

func (f *Fake) addLocked(space, parent, title, body string) string {
	f.next++
	id := strconv.Itoa(f.next)
	f.pages[id] = &Page{ID: id, Space: space, ParentID: parent, Title: title, Version: 1, Modified: f.Now().UTC(), Body: body}
	return id
}

// Edit changes a page as a person would: its next version.
func (f *Fake) Edit(id, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.pages[id]; p != nil {
		p.Body, p.Version, p.Modified = body, p.Version+1, f.Now().UTC()
	}
}

// Get returns a copy of a page.
func (f *Fake) Get(id string) (Page, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.pages[id]
	if p == nil {
		return Page{}, false
	}
	return *p, true
}

// Find returns a copy of the page with a title in a space.
func (f *Fake) Find(space, title string) (Page, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.pages {
		if p.Space == space && p.Title == title {
			return *p, true
		}
	}
	return Page{}, false
}

// Pages returns copies of every page of a space, by id.
func (f *Fake) Pages(space string) []Page {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Page
	for _, p := range f.pages {
		if p.Space == space {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"statusCode": code, "message": msg})
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	cloud := strings.HasPrefix(r.URL.Path, "/wiki/")
	if cloud {
		if u, p, ok := r.BasicAuth(); !ok || u != f.Email || p != f.Token {
			fail(w, http.StatusUnauthorized, "Unauthorized")
			return
		}
	} else if r.Header.Get("Authorization") != "Bearer "+f.Token {
		fail(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	f.mu.Lock()
	f.Requests[r.Method]++
	throttled := f.Throttle > 0
	if throttled {
		f.Throttle--
	}
	f.mu.Unlock()
	if throttled {
		w.Header().Set("Retry-After", "1")
		fail(w, http.StatusTooManyRequests, "Rate limited")
		return
	}
	if f.Fail != nil {
		if code := f.Fail(r); code != 0 {
			fail(w, code, "failed")
			return
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if cloud {
		f.cloud(w, r, strings.TrimPrefix(r.URL.Path, "/wiki/api/v2"))
	} else {
		f.dc(w, r, strings.TrimPrefix(r.URL.Path, "/rest/api"))
	}
}

func (f *Fake) v2(p *Page, body bool) map[string]any {
	out := map[string]any{"id": p.ID, "title": p.Title, "spaceId": f.spaces[p.Space], "parentId": p.ParentID, "status": "current",
		"version": map[string]any{"number": p.Version, "createdAt": p.Modified.Format(time.RFC3339)},
		"_links":  map[string]string{"webui": "/spaces/" + p.Space + "/pages/" + p.ID, "base": f.URL + "/wiki"}}
	if body {
		out["body"] = map[string]any{"storage": map[string]string{"value": p.Body, "representation": "storage"}}
	}
	return out
}

func (f *Fake) v1(p *Page, body bool) map[string]any {
	out := map[string]any{"id": p.ID, "type": "page", "title": p.Title, "space": map[string]string{"key": p.Space},
		"version": map[string]any{"number": p.Version, "when": p.Modified.Format(time.RFC3339)},
		"_links":  map[string]string{"webui": "/pages/viewpage.action?pageId=" + p.ID, "base": f.URL}}
	var anc []map[string]string
	for id := p.ParentID; id != ""; {
		anc = append([]map[string]string{{"id": id}}, anc...)
		if q := f.pages[id]; q != nil {
			id = q.ParentID
		} else {
			id = ""
		}
	}
	out["ancestors"] = anc
	if body {
		out["body"] = map[string]any{"storage": map[string]string{"value": p.Body, "representation": "storage"}}
	}
	return out
}

func (f *Fake) spaceOfID(id string) string {
	for k, v := range f.spaces {
		if v == id {
			return k
		}
	}
	return ""
}

func (f *Fake) children(parent string) []*Page {
	var out []*Page
	for _, p := range f.pages {
		if p.ParentID == parent {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (f *Fake) titleTaken(space, title, except string) bool {
	for _, p := range f.pages {
		if p.Space == space && p.Title == title && p.ID != except {
			return true
		}
	}
	return false
}

type writeBody struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	SpaceID  string `json:"spaceId"`
	ParentID string `json:"parentId"`
	Space    struct {
		Key string `json:"key"`
	} `json:"space"`
	Ancestors []struct {
		ID string `json:"id"`
	} `json:"ancestors"`
	Version struct {
		Number int `json:"number"`
	} `json:"version"`
	Body struct {
		Value   string `json:"value"`
		Storage struct {
			Value string `json:"value"`
		} `json:"storage"`
	} `json:"body"`
}

func (f *Fake) write(w http.ResponseWriter, r *http.Request, id string, cloud bool) {
	var in writeBody
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "not JSON")
		return
	}
	body := in.Body.Value
	if !cloud {
		body = in.Body.Storage.Value
	}
	parent := in.ParentID
	if !cloud && len(in.Ancestors) > 0 {
		parent = in.Ancestors[len(in.Ancestors)-1].ID
	}
	var p *Page
	if id == "" {
		space := in.Space.Key
		if cloud {
			space = f.spaceOfID(in.SpaceID)
		}
		if _, ok := f.spaces[space]; !ok {
			fail(w, http.StatusNotFound, "no space")
			return
		}
		if f.titleTaken(space, in.Title, "") {
			fail(w, http.StatusBadRequest, "A page with this title already exists")
			return
		}
		if parent == "" {
			parent = f.homes[space]
		}
		p = f.pages[f.addLocked(space, parent, in.Title, body)]
	} else {
		if p = f.pages[id]; p == nil {
			fail(w, http.StatusNotFound, "no page")
			return
		}
		if in.Version.Number != p.Version+1 {
			fail(w, http.StatusConflict, fmt.Sprintf("Version must be incremented when updating a page. Current version is: %d", p.Version))
			return
		}
		if f.titleTaken(p.Space, in.Title, p.ID) {
			fail(w, http.StatusBadRequest, "A page with this title already exists")
			return
		}
		p.Title, p.Body, p.Version, p.Modified = in.Title, body, p.Version+1, f.Now().UTC()
		if parent != "" {
			p.ParentID = parent
		}
	}
	if cloud {
		writeJSON(w, http.StatusOK, f.v2(p, true))
	} else {
		writeJSON(w, http.StatusOK, f.v1(p, true))
	}
}

func (f *Fake) cloud(w http.ResponseWriter, r *http.Request, path string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && path == "/spaces":
		key := r.URL.Query().Get("keys")
		var res []map[string]string
		if id, ok := f.spaces[key]; ok {
			res = append(res, map[string]string{"id": id, "key": key, "name": key, "homepageId": f.homes[key]})
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": res})
	case r.Method == http.MethodGet && len(parts) == 3 && parts[0] == "spaces" && parts[2] == "pages":
		space := f.spaceOfID(parts[1])
		var list []*Page
		for _, p := range f.pages {
			if p.Space == space {
				list = append(list, p)
			}
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Modified.After(list[j].Modified) })
		f.page(w, r, list, f.v2)
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "pages":
		p := f.pages[parts[1]]
		if p == nil {
			fail(w, http.StatusNotFound, "no page")
			return
		}
		writeJSON(w, http.StatusOK, f.v2(p, true))
	case r.Method == http.MethodGet && len(parts) == 3 && parts[0] == "pages" && parts[2] == "children":
		f.page(w, r, f.children(parts[1]), f.v2)
	case r.Method == http.MethodPost && path == "/pages":
		f.write(w, r, "", true)
	case r.Method == http.MethodPut && len(parts) == 2 && parts[0] == "pages":
		f.write(w, r, parts[1], true)
	default:
		fail(w, http.StatusNotFound, "no such path "+path)
	}
}

// page answers one page of a listing: Cloud's with a cursor, Data
// Center's with start; two at a time, to test the paging.
func (f *Fake) page(w http.ResponseWriter, r *http.Request, list []*Page, as func(*Page, bool) map[string]any) {
	start, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	if s := r.URL.Query().Get("start"); s != "" {
		start, _ = strconv.Atoi(s)
	}
	end := min(start+2, len(list))
	res := []map[string]any{}
	for _, p := range list[min(start, len(list)):end] {
		res = append(res, as(p, false))
	}
	links := map[string]string{}
	if end < len(list) {
		q := r.URL.Query()
		if r.URL.Query().Has("start") {
			q.Set("start", strconv.Itoa(end))
		} else {
			q.Set("cursor", strconv.Itoa(end))
		}
		links["next"] = r.URL.Path + "?" + q.Encode()
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": res, "size": len(res), "_links": links})
}

func (f *Fake) dc(w http.ResponseWriter, r *http.Request, path string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "space":
		id, ok := f.spaces[parts[1]]
		if !ok {
			fail(w, http.StatusNotFound, "no space")
			return
		}
		n, _ := strconv.Atoi(id)
		writeJSON(w, http.StatusOK, map[string]any{"id": n, "key": parts[1], "name": parts[1], "homepage": map[string]string{"id": f.homes[parts[1]]}})
	case r.Method == http.MethodGet && path == "/content":
		space := r.URL.Query().Get("spaceKey")
		var list []*Page
		for _, p := range f.pages {
			if p.Space == space {
				list = append(list, p)
			}
		}
		sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
		f.page(w, r, list, f.v1)
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "content":
		p := f.pages[parts[1]]
		if p == nil {
			fail(w, http.StatusNotFound, "no page")
			return
		}
		writeJSON(w, http.StatusOK, f.v1(p, true))
	case r.Method == http.MethodGet && len(parts) == 4 && parts[0] == "content" && parts[2] == "child" && parts[3] == "page":
		f.page(w, r, f.children(parts[1]), f.v1)
	case r.Method == http.MethodPost && path == "/content":
		f.write(w, r, "", false)
	case r.Method == http.MethodPut && len(parts) == 2 && parts[0] == "content":
		f.write(w, r, parts[1], false)
	default:
		fail(w, http.StatusNotFound, "no such path "+path)
	}
}
