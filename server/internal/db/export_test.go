package db

import "context"

// MigrateTo applies the schema's steps up to the one numbered last and no
// further: what a test of a later step needs, to put rows in before it.
func (d *DB) MigrateTo(ctx context.Context, last int) (int, error) {
	steps, err := Migrations()
	if err != nil {
		return 0, err
	}
	var upTo []Migration
	for _, m := range steps {
		if m.ID <= last {
			upTo = append(upTo, m)
		}
	}
	return d.migrate(ctx, upTo)
}
