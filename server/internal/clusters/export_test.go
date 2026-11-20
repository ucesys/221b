package clusters

import "github.com/ucesys/221b/server/internal/db"

// DatabaseOf is the database a store is over, for the tests.
func DatabaseOf(s *PGStore) *db.DB { return s.db }
