package issues

import (
	"math"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Matching (IS-10, IS-12): a report is plain words, so it is matched by
// its words. Each known issue is a document of its title, its symptoms
// and its services' names, aliases and addresses; each service one of its
// name, aliases, description and addresses. Words are weighted by how
// rare they are among the documents, BM25's way, with a few documents
// that hold nothing assumed besides, so that a word in the only known
// issue still counts as rare. A word misspelt still counts, by how many
// of its three-letter pieces it shares (trigrams). The confidence, 0 to
// 100, is how much of the best-covered symptom the report says, and how
// much of the report the known issue explains; a host or address the
// report names that the known issue's services run on counts more than
// any word.

// Candidate is a known issue or a service a text may be about, with how
// sure the match is.
type Candidate struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Confidence int     `json:"confidence"`
	Score      float64 `json:"score"`
	// Why names the words and addresses that matched.
	Why []string `json:"why,omitempty"`
}

// hostPattern finds addresses in text: a URL, or a name with dots in it.
var hostPattern = regexp.MustCompile(`(?i)\b(?:[a-z][a-z0-9+.-]*://)?((?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z][a-z0-9-]*[a-z0-9])(?::\d+)?(/[^\s"'<>)]*)?`)

// stopWords say nothing about what is wrong.
var stopWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a about above after again all am an and any are as at be because been before being below between both but by can cannot could did do does doing down during each few for from further had has have having he her here hers him his how i if in into is it its itself just me more most my no nor not of off on once only or other our out over own same she should so some such than that the their them then there these they this those through to too under until up very was we were what when where which while who whom why will with would you your yours since today now still please hi hello thanks thank also get got getting seem seems again keeps keep im ive dont doesnt cant cannot isnt wont`) {
		stopWords[w] = true
	}
}

// weakWords are said of most problems, so they count for half.
var weakWords = map[string]bool{"error": true, "work": true, "page": true, "problem": true, "issue": true, "broken": true, "fail": true, "show": true, "use": true, "try": true, "load": true, "open": true, "app": true}

// stem takes common endings off a word, so "hangs", "hanging" and "hanged"
// meet.
func stem(w string) string {
	if len(w) <= 3 || strings.IndexFunc(w, unicode.IsDigit) >= 0 {
		return w
	}
	for _, suf := range []string{"ing", "ed", "es", "s"} {
		if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 3 {
			if suf == "s" && strings.HasSuffix(w, "ss") {
				return w
			}
			if suf == "es" && !strings.HasSuffix(w, "ches") && !strings.HasSuffix(w, "shes") && !strings.HasSuffix(w, "sses") && !strings.HasSuffix(w, "xes") {
				// "times" is "time"+"s", not "tim"+"es".
				continue
			}
			return w[:len(w)-len(suf)]
		}
	}
	return w
}

// words are a text's words, lower case and stemmed, stop words left out,
// each once, in order.
func words(text string) []string {
	text = strings.ReplaceAll(strings.ToLower(text), "'", "")
	seen := map[string]bool{}
	var out []string
	for _, f := range strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(f) < 2 && !unicode.IsDigit(rune(f[0])) || stopWords[f] {
			continue
		}
		w := stem(f)
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// hosts are the addresses a text names, lower case, without a scheme or
// port: "shop.example.com" and "shop.example.com/checkout".
func hosts(text string) []string {
	var out []string
	for _, m := range hostPattern.FindAllStringSubmatch(text, -1) {
		h := strings.ToLower(m[1])
		// A version or a number with dots is not a host.
		if strings.IndexFunc(h, unicode.IsLetter) < 0 || strings.Count(h, ".") == 0 {
			continue
		}
		out = append(out, h)
		if p := strings.TrimRight(m[2], "/.,;:"); p != "" && p != "/" {
			out = append(out, h+strings.ToLower(p))
		}
	}
	return out
}

// hostOf is the host of an address a service runs on: a URL or a name.
func hostOf(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	if u, err := url.Parse(v); err == nil && u.Host != "" {
		return strings.TrimPrefix(u.Hostname(), "www.")
	}
	h, _, _ := strings.Cut(v, "/")
	return strings.TrimPrefix(h, "www.")
}

// trigrams are a word's three-letter pieces, with its ends marked.
func trigrams(w string) map[string]bool {
	p := " " + w + " "
	out := map[string]bool{}
	for i := 0; i+3 <= len(p); i++ {
		out[p[i:i+3]] = true
	}
	return out
}

// similar is how alike two words are, 0 to 1, by their trigrams.
func similar(a, b string) float64 {
	if a == b {
		return 1
	}
	if len(a) < 4 || len(b) < 4 {
		return 0
	}
	ta, tb := trigrams(a), trigrams(b)
	both := 0
	for t := range ta {
		if tb[t] {
			both++
		}
	}
	return float64(both) / float64(len(ta)+len(tb)-both)
}

// fuzzy is the least likeness at which a misspelt word counts.
const fuzzy = 0.45

// best is how well a set of words holds w: 1 when it has it, a misspelt
// likeness otherwise.
func best(w string, set map[string]bool) float64 {
	if set[w] {
		return 1
	}
	top := 0.0
	for x := range set {
		if s := similar(w, x); s >= fuzzy && s > top {
			top = s
		}
	}
	return top
}

// corpus weighs words by how many documents hold them.
type corpus struct {
	n  int
	df map[string]int
	// avg is the average document length, for BM25.
	avg float64
}

// background is the number of empty documents assumed besides the real
// ones, so a word is rare unless many known issues say it.
const background = 10

func newCorpus(docs [][]string) corpus {
	c := corpus{n: len(docs), df: map[string]int{}}
	total := 0
	for _, d := range docs {
		total += len(d)
		for _, w := range d {
			c.df[w]++
		}
	}
	if len(docs) > 0 {
		c.avg = float64(total) / float64(len(docs))
	}
	return c
}

// idf is BM25's inverse document frequency, over the documents and the
// background, halved for words said of most problems.
func (c corpus) idf(w string) float64 {
	n := float64(c.n + background)
	df := float64(c.df[w])
	v := math.Log(1 + (n-df+0.5)/(df+0.5))
	if weakWords[w] {
		v /= 2
	}
	return v
}

// bm25 scores a document for a query, BM25 with k1 1.2 and b 0.75; every
// word appears once in a document, so the term frequency is 1 or a
// misspelt likeness.
func (c corpus) bm25(query []string, doc map[string]bool, length int) float64 {
	const k1, b = 1.2, 0.75
	avg := c.avg
	if avg == 0 {
		avg = 1
	}
	score := 0.0
	for _, w := range query {
		tf := best(w, doc)
		if tf == 0 {
			continue
		}
		score += c.idf(w) * tf * (k1 + 1) / (tf + k1*(1-b+b*float64(length)/avg))
	}
	return score
}

// cover is how much of the words of part the other set holds, weighed by
// rarity: 1 when it holds all of them.
func (c corpus) cover(part []string, other map[string]bool) (float64, []string) {
	var got, all float64
	var hit []string
	for _, w := range part {
		weight := c.idf(w)
		all += weight
		if b := best(w, other); b > 0 {
			got += weight * b
			hit = append(hit, w)
		}
	}
	if all == 0 {
		return 0, nil
	}
	return got / all, hit
}

func set(ws []string) map[string]bool {
	out := make(map[string]bool, len(ws))
	for _, w := range ws {
		out[w] = true
	}
	return out
}

// matchDoc is one known issue or service as matching sees it.
type matchDoc struct {
	id, title string
	// parts are the phrases, each of which a report may say all of: a
	// symptom, the title, a service's name or alias.
	parts [][]string
	words []string
	hosts map[string]bool
}

// confidence works out how sure a match of query words and hosts to a
// document is: the best-covered part counts most, then how much of the
// report the document explains, and an address it runs on more than
// either.
func (c corpus) confidence(q []string, qhosts []string, d matchDoc) (int, []string) {
	ds := set(d.words)
	partBest := 0.0
	var why []string
	for _, p := range d.parts {
		if cov, hit := c.cover(p, set(q)); cov > partBest {
			partBest, why = cov, hit
		}
	}
	qcov, _ := c.cover(q, ds)
	hostHit := 0.0
	for _, h := range qhosts {
		base, _, _ := strings.Cut(h, "/")
		if d.hosts[h] || d.hosts[strings.TrimPrefix(base, "www.")] {
			hostHit = 1
			why = append(why, h)
			break
		}
	}
	conf := 0.65*partBest + 0.35*qcov + 0.3*hostHit
	return min(100, int(math.Round(conf*100))), why
}

// rank scores every document for a text and returns those of some
// confidence, the most likely first.
func rank(text string, docs []matchDoc) []Candidate {
	q, qh := words(text), hosts(text)
	if len(q) == 0 && len(qh) == 0 {
		return nil
	}
	all := make([][]string, len(docs))
	for i, d := range docs {
		all[i] = d.words
	}
	c := newCorpus(all)
	var out []Candidate
	for _, d := range docs {
		conf, why := c.confidence(q, qh, d)
		if conf < 20 {
			continue
		}
		out = append(out, Candidate{ID: d.id, Title: d.title, Confidence: conf, Score: math.Round(c.bm25(q, set(d.words), len(d.words))*100) / 100, Why: why})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Confidence != out[j].Confidence {
			return out[i].Confidence > out[j].Confidence
		}
		return out[i].Score > out[j].Score
	})
	return out
}

// serviceHosts are the hosts and addresses a service runs on.
func serviceHosts(s *ServiceDef) map[string]bool {
	out := map[string]bool{}
	for _, r := range s.RunsOn {
		switch r.Kind {
		case "url", "hostname", "host":
			v := strings.ToLower(strings.TrimSpace(r.Value))
			h := hostOf(v)
			if h != "" {
				out[h] = true
			}
			if u, err := url.Parse(v); err == nil && u.Host != "" && strings.Trim(u.Path, "/") != "" {
				out[h+strings.TrimRight(u.Path, "/")] = true
			}
		}
	}
	return out
}

// serviceDoc is a service as matching sees it.
func serviceDoc(s *ServiceDef) matchDoc {
	d := matchDoc{id: s.ID, title: s.Name, hosts: serviceHosts(s)}
	all := append([]string{s.Name, s.Description}, s.Aliases...)
	d.words = words(strings.Join(all, " "))
	d.parts = append(d.parts, words(s.Name))
	for _, a := range s.Aliases {
		d.parts = append(d.parts, words(a))
	}
	return d
}

// knownDoc is a known issue as matching sees it, with the names, aliases
// and addresses of the services it affects.
func knownMatchDoc(k *KnownIssue, services func(string) *ServiceDef) matchDoc {
	d := matchDoc{id: k.ID, title: k.Title, hosts: map[string]bool{}}
	text := []string{k.Title}
	text = append(text, k.Symptoms...)
	d.parts = append(d.parts, words(k.Title))
	for _, s := range k.Symptoms {
		d.parts = append(d.parts, words(s))
		for _, h := range hosts(s) {
			d.hosts[h] = true
		}
	}
	for _, id := range k.Services {
		if s := services(id); s != nil {
			text = append(text, s.Name)
			text = append(text, s.Aliases...)
			for h := range serviceHosts(s) {
				d.hosts[h] = true
			}
		}
	}
	d.words = words(strings.Join(text, " "))
	return d
}

// matchKnown ranks the open known issues for a text, leaving out the ones
// named in skip.
func (st *state) matchKnown(text string, skip []string) []Candidate {
	var docs []matchDoc
	for i := range st.known.Known {
		k := &st.known.Known[i]
		if !k.Open() || contains(skip, k.ID) {
			continue
		}
		docs = append(docs, knownMatchDoc(k, st.service))
	}
	return rank(text, docs)
}

// matchService ranks the services for a text.
func (st *state) matchService(text string) []Candidate {
	var docs []matchDoc
	for i := range st.config.Services {
		docs = append(docs, serviceDoc(&st.config.Services[i]))
	}
	return rank(text, docs)
}

// serviceThreshold is the confidence at which a report's service is
// taken from its text; below, the service stays unknown (IS-07).
const serviceThreshold = 50

// likeness is how alike two reports read, 0 to 1: the cosine of their
// words weighed by rarity among the reports compared.
func (c corpus) likeness(a, b []string) float64 {
	sb := set(b)
	var dot, na, nb float64
	for _, w := range a {
		x := c.idf(w)
		na += x * x
		if sb[w] {
			dot += x * x
		}
	}
	for _, w := range b {
		x := c.idf(w)
		nb += x * x
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / math.Sqrt(na*nb)
}

// duplicateLikeness is how alike two reports on one service must read to
// be taken for the same problem (IS-12).
const duplicateLikeness = 0.5

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
