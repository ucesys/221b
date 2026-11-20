// The state of the console's database (DB-15): GET /api/v1/database, for
// anyone signed in. A console can lose its database while it runs; it
// goes on serving sign-in and what it reads live from the clusters.
import { call } from './call'

export interface DatabaseState {
  /** False for a console with no database at all. */
  configured: boolean
  available: boolean
  /** Since when it has been as it is. */
  since?: string
  /** Why it is away, in the console's words; for a SuperAdmin only. */
  error?: string
  schemaVersion?: number
  domains?: string[]
}

export const fetchDatabase = (background = false) => call<DatabaseState>('/api/v1/database', {}, background)
