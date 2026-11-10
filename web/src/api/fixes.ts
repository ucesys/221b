// The saved fixes (Phase 12, wave 6g), listed in AIOps › Mycroft › Fixes.
// A fix is saved when a case is closed as Fixed with Save fix on: what the
// problem looked like, its cause, the steps and commands that worked, and
// how to prevent it. Active fixes go to the agents (case analysis,
// Sherlock's knowledge search); pending ones (they contradict another fix
// nobody reviewed yet) and inactive ones do not. Housekeeping marks
// duplicates inactive and contradictions pending; a person accepts,
// deactivates, edits or deletes them.
import { call } from './call'

export type FixStatus = 'active' | 'pending' | 'inactive'

/** Why a fix is pending or inactive: a duplicate, a contradiction, or a person turned it off. */
export type FixReason = 'duplicate' | 'contradicts' | 'person'

/** A case the fix was saved or seen in, newest last. */
export interface FixSeen {
  case: string
  at: string
  cluster?: string
  by?: string
}

export interface Fix {
  id: string
  title: string
  /** The agents' key of the problem. */
  key?: string
  /** Where it was first seen. */
  cluster?: string
  resource?: string
  symptoms?: string
  cause?: string
  steps?: string
  /** The changes that worked. */
  commands?: string[]
  prevention?: string
  status: FixStatus
  /** Absent while active. */
  reason?: FixReason
  /** With reason duplicate: "F-2". */
  duplicateOf?: string
  /** While pending: the fixes it contradicts. */
  contradicts?: string[]
  cases?: FixSeen[]
  created: string
  updated: string
  createdBy?: string
  updatedBy?: string
}

/** What a person may change; what is left out stays. Active on a pending fix accepts it. */
export interface FixPatch {
  title?: string
  symptoms?: string
  cause?: string
  steps?: string
  commands?: string[]
  prevention?: string
  status?: 'active' | 'inactive'
}

const enc = encodeURIComponent
const base = '/api/v1/cases/fixes'

/** Every fix, whatever its status, newest updated first; mayChange for those who work the cases. */
export const fetchFixes = (background = false) => call<{ fixes: Fix[]; mayChange: boolean }>(base, {}, background)
export const fetchFix = (id: string) => call<Fix>(`${base}/${enc(id)}`)
export const saveFix = (id: string, patch: FixPatch) => call<Fix>(`${base}/${enc(id)}`, { method: 'PATCH', body: JSON.stringify(patch) })
export const deleteFix = (id: string) => call<Fix>(`${base}/${enc(id)}`, { method: 'DELETE' })

/** Where a fix opens: AIOps › Mycroft › Fixes. */
export const fixHref = (id: string) => `#/cases/mycroft/fixes/${enc(id)}`
