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

// Unreachable and Note are what a call's error is made of (note), for the
// test of what a person is shown.
func Unreachable(err error) bool { return unreachable(err) }

func (d *DB) Note(err error) error { return d.note(err) }
