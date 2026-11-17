package issues

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Live sources in matching (0.12.1, KN-43): a report no known issue
// explains is checked against the live knowledge sources too, pages kept
// outside the console such as a known-problems page. The part of a page
// that matches the report at the match threshold or more is noted on
// the case, for the operators, as a link: nothing is closed by it, and
// the reporter is told nothing of it.

// LivePage is a page of a live knowledge source as matching reads it: its
// source, title, address and text in parts (paragraphs).
type LivePage struct {
	Source, Title, URL string
	Parts              []string
}

// LiveFunc reads the live sources' pages for a report's words.
type LiveFunc func(ctx context.Context, text string) ([]LivePage, error)

// SetLive gives matching the live sources; nil takes them away.
func (s *Service) SetLive(fn LiveFunc) {
	if fn == nil {
		s.live.Store(nil)
		return
	}
	s.live.Store(&fn)
}

// liveTime bounds one check of the live sources.
const liveTime = 40 * time.Second

// matchLive checks a report against the live sources in the background,
// and notes the best page that matches it confidently.
func (s *Service) matchLive(n int, text string) {
	fn := s.live.Load()
	if fn == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), liveTime)
		defer cancel()
		pages, err := (*fn)(ctx, text)
		if err != nil || len(pages) == 0 {
			if err != nil {
				s.log.Warn("a report could not be checked against the live sources", "report", n, "err", err)
			}
			return
		}
		set, err := s.Settings(ctx)
		if err != nil {
			return
		}
		best, ok := bestLive(text, pages, set.MatchThreshold)
		if !ok {
			return
		}
		note := fmt.Sprintf("A live source may know this problem: “%s” in %s, %s (%d%% confidence", clip(best.page.Title, 200), clip(best.page.Source, 120), best.page.URL, best.c.Confidence)
		if len(best.c.Why) > 0 {
			note += ", on: " + strings.Join(best.c.Why[:min(len(best.c.Why), 6)], ", ")
		}
		note += ")."
		err = s.update(ctx, func(st *state, ch *change) (int, error) {
			r := st.report(n)
			if r == nil {
				return 0, nil
			}
			_ = addMessage(r, Message{At: s.now(), Kind: FromSystem, Internal: true, Text: note})
			return saveReports, nil
		})
		if err != nil {
			s.log.Warn("a live source's match could not be noted", "report", n, "err", err)
		}
	}()
}

// sentenceEnd ends a sentence of a live page's part.
var sentenceEnd = regexp.MustCompile(`[.;:!?]\s+|\n`)

type liveMatch struct {
	page LivePage
	c    Candidate
}

// bestLive ranks every part of every page for a report's text, as known
// issues are ranked (each sentence of a part as a symptom), and gives
// the best at threshold or more.
func bestLive(text string, pages []LivePage, threshold int) (liveMatch, bool) {
	var docs []matchDoc
	var of []int
	for i, p := range pages {
		for _, part := range p.Parts {
			d := matchDoc{id: strconv.Itoa(len(docs)), title: p.Title, hosts: map[string]bool{}}
			// Each sentence is a symptom a report may say all of.
			for _, s := range sentenceEnd.Split(part, -1) {
				if ws := words(s); len(ws) > 0 {
					d.parts = append(d.parts, ws)
				}
			}
			for _, h := range hosts(part) {
				d.hosts[h] = true
			}
			d.words = words(p.Title + " " + part)
			docs = append(docs, d)
			of = append(of, i)
		}
	}
	cands := rank(text, docs)
	if len(cands) == 0 || cands[0].Confidence < threshold {
		return liveMatch{}, false
	}
	i, _ := strconv.Atoi(cands[0].ID)
	return liveMatch{page: pages[of[i]], c: cands[0]}, true
}
