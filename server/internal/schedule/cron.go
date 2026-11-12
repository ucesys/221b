package schedule

import (
	"fmt"
	"strconv"
	"strings"
)

// bits is a set of small numbers: minutes, hours, days, months, weekdays.
type bits uint64

func (b bits) has(i int) bool { return b&(1<<uint(i)) != 0 }

// cron is a parsed expression. Like cron, when both the day of the month
// and the day of the week are restricted, a day that matches either runs.
type cron struct {
	minute, hour, dom, month, dow bits
	domAll, dowAll                bool
}

func (c cron) dayMatches(dom, dow int) bool {
	switch {
	case c.domAll && c.dowAll:
		return true
	case c.domAll:
		return c.dow.has(dow)
	case c.dowAll:
		return c.dom.has(dom)
	}
	return c.dom.has(dom) || c.dow.has(dow)
}

var macros = map[string]string{
	"@hourly":   "0 * * * *",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@weekly":   "0 0 * * 0",
	"@monthly":  "0 0 1 * *",
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
}

var (
	monthNames = []string{"", "jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}
	fieldNames = []string{"minute", "hour", "day of the month", "month", "day of the week"}
)

// ParseCron checks a cron expression: five fields, or a macro.
func ParseCron(expr string) error {
	_, err := parseCron(expr)
	return err
}

func parseCron(expr string) (cron, error) {
	expr = strings.TrimSpace(expr)
	if m, ok := macros[strings.ToLower(expr)]; ok {
		expr = m
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return cron{}, fmt.Errorf("the cron expression %q needs five fields: minute, hour, day of the month, month, day of the week", expr)
	}
	var c cron
	var err error
	ranges := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	names := [][]string{nil, nil, nil, monthNames, dayNames}
	out := []*bits{&c.minute, &c.hour, &c.dom, &c.month, &c.dow}
	for i, field := range f {
		if *out[i], err = parseField(field, ranges[i][0], ranges[i][1], names[i]); err != nil {
			return cron{}, fmt.Errorf("the cron expression's %s %q: %w", fieldNames[i], field, err)
		}
	}
	// 7 is Sunday too.
	if c.dow.has(7) {
		c.dow |= 1
	}
	c.domAll = f[2] == "*" || f[2] == "?"
	c.dowAll = f[4] == "*" || f[4] == "?"
	return c, nil
}

func parseField(field string, lo, hi int, names []string) (bits, error) {
	var b bits
	for _, part := range strings.Split(field, ",") {
		rng, step := part, 1
		r, s, stepped := strings.Cut(part, "/")
		if stepped {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("the step %q is not a whole number above 0", s)
			}
			rng, step = r, n
		}
		from, to := lo, hi
		switch {
		case rng == "*" || rng == "?":
		case strings.Contains(rng, "-"):
			a, z, _ := strings.Cut(rng, "-")
			var err error
			if from, err = value(a, lo, hi, names); err != nil {
				return 0, err
			}
			if to, err = value(z, lo, hi, names); err != nil {
				return 0, err
			}
			if to < from {
				return 0, fmt.Errorf("the range %s runs backwards", rng)
			}
		default:
			v, err := value(rng, lo, hi, names)
			if err != nil {
				return 0, err
			}
			from = v
			// "5/15" is 5, 20, 35, 50; "5" alone is 5.
			if !stepped {
				to = v
			}
		}
		for v := from; v <= to; v += step {
			b |= 1 << uint(v)
		}
	}
	return b, nil
}

func value(s string, lo, hi int, names []string) (int, error) {
	if names != nil && s != "" {
		if i := indexOf(names, strings.ToLower(s)); i >= 0 {
			return i, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < lo || v > hi {
		return 0, fmt.Errorf("%q is not %d to %d", s, lo, hi)
	}
	return v, nil
}
