// What the gear flags: reviews that came in for this person to approve,
// and decisions someone else made on what they asked for, since they last
// opened Approvals. When they last did is kept in this browser, as the
// server's time of the newest row they saw.
import type { ApprovalRow } from '../api/automation'
import { readJSON, writeJSON } from './storage'

const KEY = '221b.approvals.seen'
/** Fired on window when Approvals has been seen, so the gear clears at once. */
export const SEEN_EVENT = '221b-approvals-seen'

/** When a row last changed for the person: asked for while it waits, decided after. */
const timeOf = (r: ApprovalRow) => (r.state === 'pending' ? r.requestedAt : r.approvedAt) ?? ''

/** The newest row's time, or '' for none. */
export function newestSeen(items: ApprovalRow[]): string {
  let out = ''
  for (const r of items) if (timeOf(r) > out) out = timeOf(r)
  return out
}

/** What is new for the person after the mark: reviews to approve, and decisions on theirs. */
export function unseen(items: ApprovalRow[], seen: string | null): { waiting: number; decided: number } {
  const out = { waiting: 0, decided: 0 }
  if (seen === null) return out
  for (const r of items) {
    if (timeOf(r) <= seen) continue
    if (r.state === 'pending' && !r.own) out.waiting++
    else if (r.state !== 'pending' && r.mine) out.decided++
  }
  return out
}

export function seenAt(): string | null {
  const v = readJSON<unknown>(KEY, null)
  return typeof v === 'string' ? v : null
}

/** Records that every row up to these rows' newest has been seen. */
export function markSeen(items: ApprovalRow[]): void {
  const at = newestSeen(items)
  const was = seenAt()
  if (was === null || at > was) writeJSON(KEY, at)
  window.dispatchEvent(new Event(SEEN_EVENT))
}

/** The first look sets the mark at what exists now, so older rows never flag. */
export function startSeen(items: ApprovalRow[]): string {
  const was = seenAt()
  if (was !== null) return was
  const at = newestSeen(items)
  writeJSON(KEY, at)
  return at
}
