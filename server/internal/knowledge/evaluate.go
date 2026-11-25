package knowledge

import (
	"context"
	"fmt"
	"strings"
)

// The search's measurement (phase-13.1-ai, part B, KN-60): a set of
// questions, each with the passage that answers it (its source, its
// item's path or address, a section's heading), asked of each way of
// searching, keyword, fuzzy, semantic and the three ranked together, as
// ask_knowledge asks (6 hits): how often the passage is among the hits,
// and at which rank. hack/knowledge-eval.py sends the owner's questions
// to a console (POST /api/v1/knowledge/evaluate) and prints the report;
// no question is kept.

// EvalQuestion is a question and the passage that answers it.
type EvalQuestion struct {
	Question string `json:"question"`
	// Source is the source, by id or name; "wiki" (or "page") for an
	// approved page.
	Source string `json:"source"`
	// Path is the item's path in its source, its address, or the end of
	// either (a page's path for the wiki); Heading, optional, a section's
	// heading, or the end of its heading path.
	Path    string `json:"path"`
	Heading string `json:"heading,omitempty"`
}

// EvalModes are the ways of searching a measurement compares.
var EvalModes = []string{ModeKeyword, ModeFuzzy, ModeSemantic, "hybrid"}

// EvalAnswer is how one question went in each way of searching.
type EvalAnswer struct {
	Question string `json:"question"`
	// Ranks are the expected passage's rank in each way (1 to the
	// limit), 0 when it is not among the hits.
	Ranks map[string]int `json:"ranks"`
	// Found are the hybrid search's hits, as "source › path › heading".
	Found []string `json:"found,omitempty"`
}

// EvalReport is a measurement's outcome.
type EvalReport struct {
	Limit     int          `json:"limit"`
	Questions int          `json:"questions"`
	Answers   []EvalAnswer `json:"answers"`
	// Hits are, for each way, how many questions found their passage
	// among the hits; Recall the same as a share.
	Hits   map[string]int     `json:"hits"`
	Recall map[string]float64 `json:"recall"`
	// Semantic says the semantic search ran: an embedding model chosen,
	// pgvector there and its endpoint answering.
	Semantic bool   `json:"semantic"`
	Note     string `json:"note,omitempty"`
}

// MaxEvalQuestions bounds a measurement.
const MaxEvalQuestions = 500

// Evaluate measures the search with questions: an Admin's or a
// SuperAdmin's; nothing is kept.
func (s *Service) Evaluate(ctx context.Context, c Caller, questions []EvalQuestion) (EvalReport, error) {
	if !c.MayAdd() {
		return EvalReport{}, wrapf(ErrDenied, "only an Admin or a SuperAdmin measures the search")
	}
	if s.pg == nil {
		return EvalReport{}, errNoIndex
	}
	switch {
	case len(questions) == 0:
		return EvalReport{}, invalid("give the questions, each with the passage that answers it")
	case len(questions) > MaxEvalQuestions:
		return EvalReport{}, invalid("at most %d questions at a time", MaxEvalQuestions)
	}
	set, err := s.Settings(ctx)
	if err != nil {
		return EvalReport{}, err
	}
	names := map[string]string{}
	for _, x := range set.Sources {
		names[strings.ToLower(x.Name)] = x.ID
	}
	const limit = 6
	rep := EvalReport{Limit: limit, Questions: len(questions), Answers: []EvalAnswer{}, Hits: map[string]int{}, Recall: map[string]float64{}}
	for i, q := range questions {
		if strings.TrimSpace(q.Question) == "" || strings.TrimSpace(q.Source) == "" || strings.TrimSpace(q.Path) == "" {
			return EvalReport{}, invalid("question %d: give the question, the source and the path of the passage that answers it", i+1)
		}
		a := EvalAnswer{Question: q.Question, Ranks: map[string]int{}}
		for _, mode := range EvalModes {
			var modes []string
			if mode != "hybrid" {
				modes = []string{mode}
			}
			hits, err := s.searchModes(ctx, q.Question, "", limit, modes)
			if err != nil {
				return EvalReport{}, err
			}
			for r, h := range hits {
				if evalMatch(q, h, names) {
					a.Ranks[mode] = r + 1
					rep.Hits[mode]++
					break
				}
			}
			if mode == "hybrid" {
				for _, h := range hits {
					a.Found = append(a.Found, hitWords(h))
				}
			}
		}
		rep.Answers = append(rep.Answers, a)
	}
	for _, mode := range EvalModes {
		rep.Recall[mode] = float64(rep.Hits[mode]) / float64(len(questions))
	}
	if v, _ := s.queryVector(ctx, questions[0].Question); len(v) > 0 {
		rep.Semantic = true
	} else {
		rep.Note = "semantic search did not run: no embedding model is chosen, the database has no pgvector, or the endpoint did not answer"
	}
	return rep, nil
}

// evalMatch says whether a hit is the passage a question expects.
func evalMatch(q EvalQuestion, h Hit, names map[string]string) bool {
	src := strings.ToLower(strings.TrimSpace(q.Source))
	want := strings.ToLower(strings.TrimSpace(q.Path))
	heading := strings.ToLower(strings.TrimSpace(q.Heading))
	if h.Kind == "page" {
		if src != "wiki" && src != "page" {
			return false
		}
		return strings.HasSuffix(strings.ToLower(h.Path), want) && (heading == "" || strings.Contains(strings.ToLower(h.Text), heading))
	}
	id, item, _ := strings.Cut(h.Source, ":")
	if id != q.Source && names[src] != id {
		return false
	}
	item, _, _ = strings.Cut(item, "#")
	item, url := strings.ToLower(item), strings.ToLower(h.URL)
	if !strings.HasSuffix(item, want) && !strings.HasSuffix(url, want) && item != want {
		return false
	}
	return heading == "" || strings.HasSuffix(strings.ToLower(h.Heading), heading) || strings.HasSuffix(strings.ToLower(h.Title), heading)
}

// hitWords names a hit for the report.
func hitWords(h Hit) string {
	if h.Kind == "page" {
		return "wiki › " + h.Path
	}
	w := h.Source
	if h.Heading != "" {
		w = strings.SplitN(w, "#", 2)[0] + " › " + h.Heading
	}
	return fmt.Sprintf("%s (%s)", w, h.Title)
}
