package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ucesys/221b/server/internal/audit"
	"github.com/ucesys/221b/server/internal/knowledge"
	"github.com/ucesys/221b/server/internal/scrub"
)

// --- One search across Mycroft (Phase 12.1, KN-35) ---
//
// GET /api/v1/aiops/mycroft/search?q=&kind=&limit= searches, for the
// person asking, what AIOps › Mycroft holds, ranked together
// (knowledge.Index.Find): the wiki's approved pages and the sources'
// excerpts for who reads the wiki; the known issues as the issue service
// lists them to them (drafts and resolved ones for an operator only); the
// fixes for who reads the cases; the accepted exceptions only for who may
// read them, as Sherlock's ask_knowledge decides (readsExceptions,
// KN-32). kind is a comma list of the kinds to list (none: all); the
// answer counts every kind the person may search, and says which those
// are. Records are scrubbed of anything that looks like a credential
// (X-01) before they are searched or shown.

const mycroftSearchPath = "/api/v1/aiops/mycroft/search"

// maxSearchQuery bounds a query, in bytes.
const maxSearchQuery = 300

// searchRecordText bounds what one record gives the search.
const searchRecordText = 4000

func (s *Server) mycroftSearchRoutes() {
	s.handle("GET "+mycroftSearchPath, "mycroft-search", s.getMycroftSearch)
}

// mycroftFound is the search's answer.
type mycroftFound struct {
	knowledge.Found
	// Kinds are the kinds the person may search, in FindKinds' order.
	Kinds []string `json:"kinds"`
}

func (s *Server) getMycroftSearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	query := audit.Clip(strings.TrimSpace(q.Get("q")), maxSearchQuery)
	limit, _ := strconv.Atoi(q.Get("limit"))
	var asked []string
	for _, k := range strings.Split(q.Get("kind"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			asked = append(asked, k)
		}
	}
	may := map[string]bool{}
	var records []knowledge.Record
	add := func(kind, id, title, status string, date time.Time, text string) {
		records = append(records, knowledge.Record{Kind: kind, ID: id, Title: scrub.Text(title), Status: status, Date: date, Text: audit.Clip(scrub.Text(text), searchRecordText)})
	}
	if s.opts.Knowledge != nil {
		if c, ok := s.knowledgeCaller(r); ok && c.Reader {
			may["page"], may["excerpt"] = true, true
		}
	}
	if s.opts.Issues != nil {
		if c, ok := s.issueCaller(r); ok {
			may[knowledge.KindKnownIssue] = true
			if list, err := s.opts.Issues.Known(ctx, c, c.Operator); err == nil && query != "" {
				for _, k := range list {
					status := string(k.Status)
					switch {
					case k.Draft:
						status = "draft"
					case k.Inactive:
						status = "inactive"
					}
					var b strings.Builder
					for _, x := range k.Symptoms {
						b.WriteString(x + "\n\n")
					}
					if len(k.Services) > 0 {
						b.WriteString("Services: " + strings.Join(k.Services, ", ") + ".\n\n")
					}
					for _, f := range k.Keys {
						if f.Object != "" {
							b.WriteString(strings.TrimSpace(f.Cluster+" "+f.Object) + "\n\n")
						}
					}
					if k.Workaround != "" {
						b.WriteString("Workaround: " + k.Workaround + "\n\n")
					}
					if k.Expected != "" {
						b.WriteString("Fix expected: " + k.Expected + "\n\n")
					}
					for _, u := range k.Updates {
						b.WriteString(u.Text + "\n\n")
					}
					add(knowledge.KindKnownIssue, k.ID, k.Title, status, k.Updated, b.String())
				}
			}
		}
	}
	if s.opts.Fixes != nil {
		if c, ok := s.fixesCaller(r); ok && (c.Read || c.Change) {
			may[knowledge.KindFix] = true
			if list, err := s.opts.Fixes.List(ctx); err == nil && query != "" {
				for _, f := range list {
					var b strings.Builder
					for _, x := range []string{f.Symptoms, f.Cause, f.Steps, strings.Join(f.Commands, "\n"), f.Prevention, strings.TrimSpace(f.Cluster + " " + f.Resource)} {
						if x != "" {
							b.WriteString(x + "\n\n")
						}
					}
					add(knowledge.KindFix, f.ID, f.Title, f.Status, f.Updated, b.String())
				}
			}
		}
	}
	if s.opts.Lessons != nil && s.readsExceptions(r) {
		may[knowledge.KindException] = true
		if list, err := s.opts.Lessons.Exceptions(ctx); err == nil && query != "" {
			now := time.Now()
			for _, e := range list {
				text := fmt.Sprintf("%s\n\n%s %s, validation rule %s.\n\nJustification: %s", e.Detail, e.Cluster, e.Resource, e.Check, e.Reason)
				add(knowledge.KindException, e.ID, e.Title, e.State(now), e.At, text)
			}
		}
	}
	if len(may) == 0 {
		jsonError(w, http.StatusForbidden, "Your roles let you search none of Mycroft’s wiki, known issues, fixes or exceptions.")
		return
	}
	kinds := slices.DeleteFunc(slices.Clone(knowledge.FindKinds), func(k string) bool { return !may[k] })
	// A kind the person may not search is never listed, asked or not.
	list := slices.DeleteFunc(asked, func(k string) bool { return !may[k] })
	if len(asked) > 0 && len(list) == 0 {
		list = []string{"none"}
	}
	if len(asked) == 0 {
		list = kinds
	}
	var found knowledge.Found
	if s.opts.Knowledge != nil && may["page"] {
		found = s.opts.Knowledge.Find(query, list, limit, records)
	} else {
		found = knowledge.NewIndex(nil, nil, 0, time.Time{}).Find(query, list, limit, records)
	}
	b, _ := json.Marshal(mycroftFound{Found: found, Kinds: kinds})
	writeJSON(w, r, b)
}
