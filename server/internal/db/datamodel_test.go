package db_test

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ucesys/221b/server/internal/db"
	"github.com/ucesys/221b/server/internal/db/dbtest"
)

// dataModelPage is the page that lists every table and column of the
// schema, from this package's directory.
const dataModelPage = "../../../docs/data-model.md"

// A column as the page or the schema has it: whether it is (part of) the
// primary key, the table its foreign key points at ("" for none), whether
// that key may be empty, and for the page whether it is marked as a key
// into another domain and which records it names by a plain id.
type modelColumn struct {
	pk       bool
	fk       string
	optional bool
	across   bool
	names    []string
}

type modelTable struct {
	domain  string
	line    int
	order   []string
	columns map[string]modelColumn
}

// A foreign key as one line of text, the same for the page and the schema.
func modelKey(table string, cols []string, to string, toCols []string, onDelete string) string {
	return fmt.Sprintf("%s (%s) -> %s (%s) ON DELETE %s", table, strings.Join(cols, ", "), to, strings.Join(toCols, ", "), onDelete)
}

type modelPage struct {
	tables, domains, tiers, own, steps, keys int
	// What the page lists: the model's tables by name, the database's
	// own, the domains in order, and the foreign keys.
	model    map[string]*modelTable
	ownSet   map[string]*modelTable
	domainOf []string
	tierOf   []string
	keyList  []string
}

var (
	modelCounts   = regexp.MustCompile("(\\d+) tables in (\\d+) domains and (\\d+) tiers, and (\\d+) tables of the database's own")
	modelSteps    = regexp.MustCompile("steps `0001` to `(\\d{4})`")
	modelKeyCount = regexp.MustCompile(`^The (\d+) foreign keys of the schema`)
	modelRow      = regexp.MustCompile("^\\| `([a-z0-9_]+)` \\| ([^|]+) \\| ([^|]+) \\|$")
	modelItem     = regexp.MustCompile("^`([a-z0-9_]+)`( PK)?(?: (FK\\??) → `([a-z0-9_]+)`( ↗)?)?(?: ≈ ((?:`[a-z0-9_]+`)(?: or `[a-z0-9_]+`)*))?(?: \\([^()]*\\))?")
	modelKeyRow   = regexp.MustCompile("^\\| `([a-z0-9_]+)` \\| ((?:`[a-z0-9_]+`)(?:, `[a-z0-9_]+`)*) \\| `([a-z0-9_]+)` \\(((?:`[a-z0-9_]+`)(?:, `[a-z0-9_]+`)*)\\) \\| `(CASCADE|SET NULL|RESTRICT|NO ACTION)` \\|$")
)

func modelNames(list, sep string) []string {
	var out []string
	for _, s := range strings.Split(list, sep) {
		out = append(out, strings.Trim(s, "`"))
	}
	return out
}

// parseModelPage reads the page. Its format is fixed: under a heading
// "## … tier" each "### " heading is a domain, and every line of a table
// there is the header, the rule under it, or a row
//
//	| `table` | what it holds | `column` MARKS (words), `column` … |
//
// where MARKS are, in this order, " PK", " FK → `table`" or
// " FK? → `table`" with " ↗" after it, and " ≈ `table`" (or more tables
// joined by " or "). The section "The database's own tables" has rows
// of the same kind, and "Every foreign key" rows of its own. A line of a
// table that is none of these is an error, so a typo cannot hide a table.
func parseModelPage(text string) (*modelPage, []string) {
	p := &modelPage{model: map[string]*modelTable{}, ownSet: map[string]*modelTable{}}
	var errs []string
	bad := func(line int, format string, args ...any) {
		errs = append(errs, fmt.Sprintf("line %d: %s", line, fmt.Sprintf(format, args...)))
	}
	if m := modelCounts.FindAllStringSubmatch(text, -1); len(m) == 1 {
		p.tables, _ = strconv.Atoi(m[0][1])
		p.domains, _ = strconv.Atoi(m[0][2])
		p.tiers, _ = strconv.Atoi(m[0][3])
		p.own, _ = strconv.Atoi(m[0][4])
	} else {
		errs = append(errs, fmt.Sprintf("the opening must say once \"N tables in N domains and N tiers, and N tables of the database's own\": found %d times", len(m)))
	}
	if m := modelSteps.FindAllStringSubmatch(text, -1); len(m) == 1 {
		p.steps, _ = strconv.Atoi(m[0][1])
	} else {
		errs = append(errs, fmt.Sprintf("the opening must say once \"steps `0001` to `NNNN`\": found %d times", len(m)))
	}
	p.keys = -1
	section, domain := "", ""
	for i, line := range strings.Split(text, "\n") {
		n := i + 1
		switch {
		case strings.HasPrefix(line, "## "):
			title := strings.TrimPrefix(line, "## ")
			domain = ""
			switch {
			case strings.HasSuffix(title, " tier"):
				section = "tier"
				p.tierOf = append(p.tierOf, title)
			case title == "The database's own tables":
				section = "own"
			case title == "Every foreign key":
				section = "keys"
			default:
				section = ""
			}
			continue
		case strings.HasPrefix(line, "### ") && section == "tier":
			domain = strings.TrimPrefix(line, "### ")
			if slices.Contains(p.domainOf, domain) {
				bad(n, "the domain %q has two headings", domain)
			}
			p.domainOf = append(p.domainOf, domain)
			continue
		}
		if section == "keys" {
			if m := modelKeyCount.FindStringSubmatch(line); m != nil {
				p.keys, _ = strconv.Atoi(m[1])
			}
		}
		if section == "" || !strings.HasPrefix(line, "|") {
			continue
		}
		if section == "keys" {
			if line == "| From | Columns | To | On delete |" || line == "| --- | --- | --- | --- |" {
				continue
			}
			m := modelKeyRow.FindStringSubmatch(line)
			if m == nil {
				bad(n, "not a row of the foreign keys (| `table` | `column`, … | `table` (`column`, …) | `CASCADE` |): %.80s", line)
				continue
			}
			p.keyList = append(p.keyList, modelKey(m[1], modelNames(m[2], ", "), m[3], modelNames(m[4], ", "), m[5]))
			continue
		}
		if line == "| Table | Holds | Columns |" || line == "| --- | --- | --- |" {
			continue
		}
		m := modelRow.FindStringSubmatch(line)
		if m == nil {
			bad(n, "not a row of a table (| `table` | what it holds | `column` …, `column` … |): %.80s", line)
			continue
		}
		name := m[1]
		if section == "tier" && domain == "" {
			bad(n, "the table %s is under no domain's heading", name)
			continue
		}
		if p.model[name] != nil || p.ownSet[name] != nil {
			bad(n, "the table %s has two rows", name)
			continue
		}
		t := &modelTable{domain: domain, line: n, columns: map[string]modelColumn{}}
		rest := m[3]
		for rest != "" {
			it := modelItem.FindStringSubmatch(rest)
			if it == nil {
				bad(n, "table %s: cannot read a column at: %.60s", name, rest)
				break
			}
			rest = rest[len(it[0]):]
			if rest != "" {
				if !strings.HasPrefix(rest, ", ") {
					bad(n, "table %s: after the column %s, expected \", \" or the end of the row, found: %.40s", name, it[1], rest)
					break
				}
				rest = rest[2:]
				if rest == "" {
					bad(n, "table %s: the row ends in a comma", name)
				}
			}
			c := modelColumn{pk: it[2] != "", fk: it[4], optional: it[3] == "FK?", across: it[5] != ""}
			if it[6] != "" {
				c.names = modelNames(it[6], " or ")
			}
			if _, twice := t.columns[it[1]]; twice {
				bad(n, "table %s: the column %s is listed twice", name, it[1])
				continue
			}
			t.columns[it[1]] = c
			t.order = append(t.order, it[1])
		}
		if section == "own" {
			p.ownSet[name] = t
		} else {
			p.model[name] = t
		}
	}
	return p, errs
}

// The page of the data model lists every table and column of the schema,
// with the keys marked, and the schema has nothing the page does not list
// (DB-68). A step that changes a table changes the page with it.
func TestTheDataModelIsTheSchema(t *testing.T) {
	d := dbtest.Open(t)
	ctx := ctxT(t)
	raw, err := os.ReadFile(dataModelPage)
	if err != nil {
		t.Fatalf("reading the data model's page: %v", err)
	}
	page, errs := parseModelPage(string(raw))
	for _, e := range errs {
		t.Errorf("%s, %s", dataModelPage, e)
	}
	if len(errs) != 0 {
		t.Fatalf("the page cannot be read: fix the lines above first")
	}

	// The schema, from the catalog.
	schema := map[string]*modelTable{}
	rows, err := d.Pool().Query(ctx, `SELECT c.relname, a.attname, a.attnotnull FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace JOIN pg_attribute a ON a.attrelid = c.oid
		WHERE n.nspname = 'public' AND c.relkind = 'r' AND a.attnum > 0 AND NOT a.attisdropped ORDER BY c.relname, a.attnum`)
	if err != nil {
		t.Fatal(err)
	}
	notNull := map[string]bool{}
	var table, column string
	var filled bool
	if _, err := pgx.ForEachRow(rows, []any{&table, &column, &filled}, func() error {
		if schema[table] == nil {
			schema[table] = &modelTable{columns: map[string]modelColumn{}}
		}
		schema[table].columns[column] = modelColumn{}
		schema[table].order = append(schema[table].order, column)
		notNull[table+"."+column] = filled
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rows, err = d.Pool().Query(ctx, `SELECT c.relname, con.contype::text, coalesce(f.relname, ''), CASE con.confdeltype
			WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL' WHEN 'r' THEN 'RESTRICT' WHEN 'd' THEN 'SET DEFAULT' ELSE 'NO ACTION' END,
			ARRAY(SELECT a.attname::text FROM unnest(con.conkey) WITH ORDINALITY k(num, ord)
				JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.num ORDER BY k.ord),
			ARRAY(SELECT a.attname::text FROM unnest(con.confkey) WITH ORDINALITY k(num, ord)
				JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.num ORDER BY k.ord)
		FROM pg_constraint con JOIN pg_class c ON c.oid = con.conrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_class f ON f.oid = con.confrelid
		WHERE n.nspname = 'public' AND con.contype IN ('p', 'f') ORDER BY c.relname, con.conname`)
	if err != nil {
		t.Fatal(err)
	}
	var kind, to, onDelete string
	var cols, toCols []string
	var schemaKeys []string
	if _, err := pgx.ForEachRow(rows, []any{&table, &kind, &to, &onDelete, &cols, &toCols}, func() error {
		for _, c := range cols {
			x := schema[table].columns[c]
			if kind == "p" {
				x.pk = true
			} else {
				x.fk, x.optional = to, !notNull[table+"."+c]
			}
			schema[table].columns[c] = x
		}
		if kind == "f" {
			schemaKeys = append(schemaKeys, modelKey(table, cols, to, toCols, onDelete))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var diffs []string
	diff := func(format string, args ...any) { diffs = append(diffs, fmt.Sprintf(format, args...)) }

	// What the opening says.
	steps, _ := db.Migrations()
	if page.steps != len(steps) {
		diff("the opening says the steps go to %04d; the schema's last step is %04d", page.steps, len(steps))
	}
	if page.tables != len(page.model) {
		diff("the opening says %d tables; the domains list %d", page.tables, len(page.model))
	}
	if page.domains != len(page.domainOf) {
		diff("the opening says %d domains; the page has %d (%s)", page.domains, len(page.domainOf), strings.Join(page.domainOf, ", "))
	}
	if page.tiers != len(page.tierOf) {
		diff("the opening says %d tiers; the page has %d (%s)", page.tiers, len(page.tierOf), strings.Join(page.tierOf, ", "))
	}
	if page.own != len(page.ownSet) {
		diff("the opening says %d tables of the database's own; that section lists %d", page.own, len(page.ownSet))
	}
	for _, domain := range page.domainOf {
		if !slices.ContainsFunc(sortedTables(page.model), func(name string) bool { return page.model[name].domain == domain }) {
			diff("the domain %q lists no table", domain)
		}
	}
	if len(page.model)+len(page.ownSet) != len(schema) {
		diff("the page lists %d tables with the database's own; the schema has %d", len(page.model)+len(page.ownSet), len(schema))
	}

	// Every table, both ways.
	listed := map[string]*modelTable{}
	for name, tb := range page.model {
		listed[name] = tb
	}
	for name, tb := range page.ownSet {
		listed[name] = tb
	}
	for _, name := range sortedTables(schema) {
		if listed[name] == nil {
			diff("table %s is in the schema and not on the page", name)
		}
	}
	for _, name := range sortedTables(listed) {
		tb := listed[name]
		in := schema[name]
		if in == nil {
			diff("table %s (line %d) is on the page and not in the schema", name, tb.line)
			continue
		}
		// Every column, both ways, and its marks.
		for _, c := range in.order {
			if _, ok := tb.columns[c]; !ok {
				diff("table %s (line %d): the column %s is in the schema and not in the table's row", name, tb.line, c)
			}
		}
		for _, c := range tb.order {
			want, ok := in.columns[c]
			if !ok {
				diff("table %s (line %d): the column %s is in the table's row and not in the schema", name, tb.line, c)
				continue
			}
			got := tb.columns[c]
			where := fmt.Sprintf("table %s (line %d): the column %s", name, tb.line, c)
			switch {
			case want.pk && !got.pk:
				diff("%s is part of the primary key in the schema and is not marked PK", where)
			case !want.pk && got.pk:
				diff("%s is marked PK and is not part of the primary key in the schema", where)
			}
			switch {
			case want.fk != "" && got.fk == "":
				diff("%s is a foreign key to %s in the schema and is not marked FK", where, want.fk)
			case want.fk == "" && got.fk != "":
				diff("%s is marked FK → %s and is no foreign key in the schema", where, got.fk)
			case want.fk != got.fk:
				diff("%s is marked FK → %s; in the schema it points to %s", where, got.fk, want.fk)
			case want.fk != "" && want.optional && !got.optional:
				diff("%s may be empty in the schema: mark it FK?, not FK", where)
			case want.fk != "" && !want.optional && got.optional:
				diff("%s is never empty in the schema: mark it FK, not FK?", where)
			}
			if got.fk != "" && want.fk == got.fk {
				target := listed[got.fk]
				across := tb.domain != "" && target != nil && target.domain != tb.domain
				switch {
				case across && !got.across:
					diff("%s points into another domain (%s, in %s) and is not marked ↗", where, got.fk, target.domain)
				case !across && got.across:
					diff("%s is marked ↗ and %s is in its own domain", where, got.fk)
				}
			}
			if len(got.names) != 0 && (want.fk != "" || got.fk != "") {
				diff("%s is marked ≈ (an id kept as plain data) and is a foreign key", where)
			}
			for _, x := range got.names {
				if schema[x] == nil {
					diff("%s is marked ≈ %s, which is no table of the schema", where, x)
				}
			}
		}
	}

	// The list of foreign keys, both ways.
	if page.keys != len(page.keyList) {
		diff("the list of foreign keys says it has %d; it lists %d", page.keys, len(page.keyList))
	}
	sort.Strings(schemaKeys)
	pageKeys := slices.Clone(page.keyList)
	sort.Strings(pageKeys)
	for _, k := range schemaKeys {
		if !slices.Contains(pageKeys, k) {
			diff("the foreign key %s is in the schema and not in the page's list", k)
		}
	}
	for i, k := range pageKeys {
		if !slices.Contains(schemaKeys, k) {
			diff("the foreign key %s is in the page's list and not in the schema", k)
		}
		if i > 0 && pageKeys[i-1] == k {
			diff("the foreign key %s is in the page's list twice", k)
		}
	}

	for _, s := range diffs {
		t.Errorf("%s: %s", dataModelPage, s)
	}
	if len(diffs) != 0 {
		t.Errorf("%d differences between the data model's page and the schema: a step that changes the schema changes docs/data-model.md with it (DB-68)", len(diffs))
	}
}

func sortedTables(m map[string]*modelTable) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
