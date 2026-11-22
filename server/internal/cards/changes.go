package cards

import (
	"context"
	"slices"

	"github.com/ucesys/221b/server/internal/cardid"
	"github.com/ucesys/221b/server/internal/kv"
)

// Changes is where the store keeps the proposed changes (DB-79): what the
// store does with them, each operation reading and writing the changes it
// names and no other. Two implementations: CardsPG, the model's tables,
// where every console keeps them; and kvChanges, one JSON document of a
// kv.Backend, kept in memory by the tests and read once from the
// ConfigMap of before by the copy.
type Changes interface {
	// All returns every change kept, oldest first, with the number the
	// last change was given.
	All(ctx context.Context) (record, error)
	// Get returns the change id names (its id the one way, PC-12);
	// ErrNotFound when none is kept.
	Get(ctx context.Context, id string) (Card, error)
	// Edit changes the change id names: edit is given it, and what edit
	// leaves is kept, and nothing else is written. An error from edit
	// keeps nothing, and is returned with the change as edit left it.
	// Two edits of one change take turns, so the second sees what the
	// first kept; edits of two changes do not wait for each other.
	Edit(ctx context.Context, id string, edit func(*Card) error) (Card, error)
	// Add adds a change. add is given the changes that wait for a
	// decision (Open) and the number the next change is given, and
	// returns the change to add, if any, and those of the waiting changes
	// it changed: they are kept, and no other change is written. Two adds
	// take turns. The newest decided changes are kept, keepFinished of
	// them; older ones go when a change is added.
	Add(ctx context.Context, add func(open []Card, next int) (*Card, []Card, error)) error
	// Change edits every change at once, with the counter the ids come
	// from: for what is rare and reads or changes many (cases renamed, a
	// restore). It takes the turn of every other write.
	Change(ctx context.Context, edit func(*record) error) error
}

// changesIn finds the repository a store's backend keeps the changes in:
// the database's, when the key of the changes is one (the console's
// wiring, a test over the database); else the backend's own document.
func changesIn(b kv.Backend) Changes {
	if k, ok := b.(*kv.Keyed); ok {
		if c, ok := k.Keys[CardsKey].(Changes); ok {
			return c
		}
	}
	return kvChanges{b}
}

// keepRecent drops the oldest decided changes past keepFinished.
func keepRecent(rec *record) {
	finished := 0
	for i := len(rec.Cards) - 1; i >= 0; i-- {
		if !rec.Cards[i].Finished() {
			continue
		}
		if finished++; finished > keepFinished {
			rec.Cards = slices.Delete(rec.Cards, i, i+1)
		}
	}
}

// kvChanges keeps the changes as one JSON document under CardsKey.
type kvChanges struct{ b kv.Backend }

func (k kvChanges) All(ctx context.Context) (record, error) {
	rec, _, err := loadJSON[record](ctx, k.b, CardsKey)
	rec.canon()
	return rec, err
}

func (k kvChanges) Get(ctx context.Context, id string) (Card, error) {
	rec, err := k.All(ctx)
	if err != nil {
		return Card{}, err
	}
	id = cardid.Canon(id)
	for _, c := range rec.Cards {
		if c.ID == id {
			return c, nil
		}
	}
	return Card{}, ErrNotFound
}

func (k kvChanges) Edit(ctx context.Context, id string, edit func(*Card) error) (Card, error) {
	var out Card
	id = cardid.Canon(id)
	err := k.Change(ctx, func(rec *record) error {
		for i := range rec.Cards {
			if rec.Cards[i].ID == id {
				err := edit(&rec.Cards[i])
				out = rec.Cards[i]
				return err
			}
		}
		return ErrNotFound
	})
	return out, err
}

func (k kvChanges) Add(ctx context.Context, add func(open []Card, next int) (*Card, []Card, error)) error {
	return k.Change(ctx, func(rec *record) error {
		var open []Card
		for _, c := range rec.Cards {
			if c.Open() {
				open = append(open, c)
			}
		}
		c, changed, err := add(open, rec.Next+1)
		if err != nil {
			return err
		}
		for _, x := range changed {
			if i := slices.IndexFunc(rec.Cards, func(y Card) bool { return y.ID == x.ID }); i >= 0 {
				rec.Cards[i] = x
			}
		}
		if c != nil {
			rec.Next++
			rec.Cards = append(rec.Cards, *c)
			keepRecent(rec)
		}
		return nil
	})
}

func (k kvChanges) Change(ctx context.Context, edit func(*record) error) error {
	return changeJSON(ctx, k.b, CardsKey, func(rec *record) error {
		rec.canon()
		return edit(rec)
	})
}
