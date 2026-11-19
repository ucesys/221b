package settings_test

import "github.com/jackc/pgx/v5"

// pgxForEach scans each row into v and calls fn.
func pgxForEach(rows pgx.Rows, v *string, fn func()) (int, error) {
	n := 0
	_, err := pgx.ForEachRow(rows, []any{v}, func() error { n++; fn(); return nil })
	return n, err
}
