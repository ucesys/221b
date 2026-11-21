package issues

import (
	"context"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
)

// The list of cases as a query (DB-88). The lists (the issue API's list
// routes, the console's Cases and the Inbox) read the rows of the cases
// that pass the filter and that the caller may see, not every case's
// row: the query says in SQL what visible and Filter.match say in Go,
// and a line is made from its row as the index makes it.

// caseLister is a store of cases that lists them with a query; the
// service uses it in place of filtering the index when its store is one.
type caseLister interface {
	listLines(ctx context.Context, c Caller, f Filter) ([]Line, error)
	noActionCounts(ctx context.Context) (map[string]int, error)
}

// lineColumns are the columns of a case's row a line is made from: every
// one but its text, which a line does not show; with the case's id first
// and its watchers' ids last.
func lineColumns() string {
	var row caseRow
	cols := db.Columns(&row)
	for i, c := range cols {
		if c == "body" {
			cols[i] = "'' AS body"
		}
	}
	return "case_id::text, " + strings.Join(cols, ", ") +
		", ARRAY(SELECT p.person_key FROM case_people p WHERE p.case_id = cases.case_id AND p.role = 'watcher' ORDER BY p.position)" +
		", ARRAY(SELECT l.kind || ' ' || l.linked_case_number FROM case_links l WHERE l.case_id = cases.case_id AND l.kind <> '" + linkSimilar + "')"
}

// linksOfLine reads the links lineColumns gives as "<kind> <number>".
func linksOfLine(pairs []string) caseLinks {
	var out caseLinks
	for _, p := range pairs {
		kind, n, _ := strings.Cut(p, " ")
		number, _ := strconv.Atoi(n)
		out = append(out, caseLink{kind: kind, number: number})
	}
	return out
}

// outcomesOf are the outcomes stored that read as o (CF-90).
func outcomesOf(o Outcome) []string {
	canon := o.Canonical()
	out := []string{string(canon)}
	for _, x := range []Outcome{legacyNotAProblem, legacyNoFixNeeded} {
		if x.Canonical() == canon {
			out = append(out, string(x))
		}
	}
	return out
}

// listQuery builds the WHERE of the list: its conditions and their
// arguments.
type listQuery struct {
	conds []string
	args  []any
}

// arg adds an argument and returns its placeholder.
func (q *listQuery) arg(v any) string {
	q.args = append(q.args, v)
	return "$" + strconv.Itoa(len(q.args))
}

func (q *listQuery) where(cond string) { q.conds = append(q.conds, cond) }

// The parts of a line the conditions name, as SQL over a row of cases.
const (
	// A person the line names by id: the filer is "" where there is none.
	sqlFiledBy = "coalesce(cases.filed_by_key, '')"
	// Kind: a case without a source is a report.
	sqlKind = "CASE WHEN cases.source = '' THEN '" + CaseReport + "' ELSE cases.source END"
	// CaseType: a case without a type is other.
	sqlType = "CASE WHEN cases.type = '' THEN '" + TypeOther + "' ELSE cases.type END"
	// What the words of a query are looked for in: the number twice, the
	// line's title, the reporter's label, the object, the cluster, the
	// check of a finding's key and the incident, lower case.
	sqlHay = "lower('#' || cases.case_number || ' C-' || cases.case_number || ' ' || cases.line_title || ' ' || " +
		"CASE WHEN cases.reporter_name <> '' THEN cases.reporter_name ELSE cases.reporter_key END || ' ' || cases.object || ' ' || cases.cluster_id || " +
		"CASE WHEN cases.key_cluster_id IS NOT NULL THEN ' ' || cases.key_check ELSE '' END || " +
		"CASE WHEN cases.incident <> '' THEN ' ' || cases.incident ELSE '' END)"
)

// watchedBy is the condition that the person whose id is at p follows the case.
func watchedBySQL(p string) string {
	return "EXISTS (SELECT 1 FROM case_people w WHERE w.case_id = cases.case_id AND w.role = 'watcher' AND w.person_key = " + p + ")"
}

// build turns who may see and the filter into conditions; false when no
// case can pass, so nothing need be read.
func (q *listQuery) build(c Caller, f Filter) bool {
	q.where("cases.is_case_kept")
	// visible (IS-21).
	if !c.Operator {
		if c.ID == "" {
			return false
		}
		id := q.arg(c.ID)
		see := "cases.reporter_key = " + id + " OR " + sqlFiledBy + " = " + id + " OR " + watchedBySQL(id)
		if !c.System {
			see += " OR (cases.service_key <> '' AND EXISTS (SELECT 1 FROM services s WHERE s.service_id = cases.service_key AND " + id + " = ANY (s.contacts)))"
		}
		q.where("(" + see + ")")
	}
	// Filter.match, in its order.
	if f.Mine {
		id := q.arg(c.ID)
		q.where("(cases.reporter_key = " + id + " OR " + sqlFiledBy + " = " + id + " OR " + watchedBySQL(id) + ")")
	}
	if f.Status != "" {
		q.where("cases.status = " + q.arg(string(f.Status)))
	}
	if f.Outcome != "" {
		q.where("cases.outcome = ANY (" + q.arg(outcomesOf(f.Outcome)) + ")")
	}
	if f.Open || f.Pending {
		q.where("cases.status <> '" + string(StatusClosed) + "'")
	}
	if f.Service != "" {
		q.where("cases.service_key = " + q.arg(f.Service))
	}
	if f.Priority != nil {
		q.where("cases.priority = " + q.arg(*f.Priority))
	}
	if f.Known != "" {
		q.where("cases.known_issue_key = " + q.arg(f.Known))
	}
	if f.Owner != "" {
		q.where("cases.owner_key = " + q.arg(f.Owner))
	}
	if f.Involved {
		// Line.Involves: nobody is involved without an id.
		if c.ID == "" {
			return false
		}
		id := q.arg(c.ID)
		q.where("(cases.reporter_key = " + id + " OR " + sqlFiledBy + " = " + id + " OR cases.owner_key = " + id + " OR " + watchedBySQL(id) + " OR " + id + " = ANY (cases.involved))")
	}
	if f.Source != "" {
		q.where(sqlKind + " = " + q.arg(f.Source))
	}
	if f.Agents {
		q.where("cases.source = '" + CaseFinding + "'")
	}
	if f.People {
		q.where("cases.source <> '" + CaseFinding + "'")
	}
	if f.Cluster != "" {
		q.where("cases.cluster_id = " + q.arg(f.Cluster))
	}
	if f.Unassigned {
		q.where("cases.owner_key IS NULL")
	}
	if f.Type != "" {
		q.where(sqlType + " = " + q.arg(f.Type))
	}
	for _, w := range strings.Fields(strings.ToLower(f.Query)) {
		q.where("strpos(" + sqlHay + ", lower(" + q.arg(w) + ")) > 0")
	}
	return true
}

// listLines are the lines of the cases a caller may see that pass a
// filter, newest first.
func (s *CasesPG) listLines(ctx context.Context, c Caller, f Filter) ([]Line, error) {
	var q listQuery
	if !q.build(c, f) {
		return nil, nil
	}
	var out []Line
	err := s.DB.Do(ctx, func(dq db.Querier) error {
		rows, err := dq.Query(ctx, "SELECT "+lineColumns()+" FROM cases WHERE "+strings.Join(q.conds, " AND ")+" ORDER BY cases.case_number DESC, cases.object_name DESC", q.args...)
		if err != nil {
			return err
		}
		out, err = scanLines(rows)
		return err
	})
	return out, err
}

// scanLines makes a line of each row lineColumns reads, as the index
// does.
func scanLines(rows pgx.Rows) ([]Line, error) {
	var out []Line
	var row caseRow
	var id string
	var watchers, links []string
	_, err := pgx.ForEachRow(rows, append(append([]any{&id}, db.Fields(&row)...), &watchers, &links), func() error {
		out = append(out, rowLine(&row, watchers, linksOfLine(links)))
		return nil
	})
	return out, err
}

// rowLine is the line of a case's row, with its watchers' ids and the
// cases it names.
func rowLine(row *caseRow, watchers []string, links caseLinks) Line {
	c := rowToCase(row)
	links.set(&c)
	for _, w := range watchers {
		c.Watchers = append(c.Watchers, Person{ID: w})
	}
	l := lineOf(&c)
	l.Title, l.Involved = row.LineTitle, pgListOf(row.Involved)
	return l
}

// noActionCounts counts the agent-found cases closed as no action
// needed, by cluster and check, as "<cluster> <check>" (CF-13).
func (s *CasesPG) noActionCounts(ctx context.Context) (map[string]int, error) {
	out := map[string]int{}
	err := s.DB.Do(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, `SELECT key_cluster_id, key_check, count(*) FROM cases
			WHERE is_case_kept AND source = $1 AND key_cluster_id IS NOT NULL AND outcome = ANY ($2) GROUP BY key_cluster_id, key_check`, CaseFinding, outcomesOf(OutcomeNoAction))
		if err != nil {
			return err
		}
		var cluster, check string
		var n int
		_, err = pgx.ForEachRow(rows, []any{&cluster, &check, &n}, func() error {
			out[cluster+" "+check] += n
			return nil
		})
		return err
	})
	return out, err
}
