// What the gear flags: reviews that came in for this person to approve,
// and decisions someone else made on what they asked for, since they last
// opened Approvals. When they last did is kept in this browser, for each
// person who signs in on it, as the server's time of the newest row they
// saw.
import type { ApprovalRow } from '../api/automation'
import { readJSON, writeJSON } from './storage'

const KEY = '221b.approvals.seen'
/** Fired on window when Approvals has been seen, so the gear clears at once. */
export const SEEN_EVENT = '221b-approvals-seen'

// Whose mark it is: set by the top bar once it knows who signed in.
let who = ''

/** Makes the marks this person's. */
export function seenFor(user: string): void {
  who = user
}

const key = () => (who ? `${KEY}.${who}` : KEY)

/** When a row last changed for the person: asked for while it waits, decided after. */
const timeOf = (r: ApprovalRow) => (r.state === 'pending' ? r.requestedAt : r.approvedAt) ?? ''

/**
 * Orders two of the server's times, which drop trailing zeros of their
 * fractions, so their text does not sort: by the second, then the fraction.
 * '' is before every time.
 */
export function compareTimes(a: string, b: string): number {
  if (a === b) return 0
  if (a === '') return -1
  if (b === '') return 1
  const split = (s: string): [number, string] => {
    const m = /\.(\d+)/.exec(s)
    return [Date.parse(s.replace(/\.\d+/, '')), (m?.[1] ?? '').padEnd(9, '0')]
  }
  const [as, af] = split(a)
  const [bs, bf] = split(b)
  if (as !== bs) return as < bs ? -1 : 1
  return af < bf ? -1 : af > bf ? 1 : 0
}

/** The newest row's time, or '' for none. */
export function newestSeen(items: ApprovalRow[]): string {
  let out = ''
  for (const r of items) if (compareTimes(timeOf(r), out) > 0) out = timeOf(r)
  return out
}

/** What is new for the person after the mark: reviews to approve, and decisions on theirs. */
export function unseen(items: ApprovalRow[], seen: string | null): { waiting: number; decided: number } {
  const out = { waiting: 0, decided: 0 }
  if (seen === null) return out
  for (const r of items) {
    if (compareTimes(timeOf(r), seen) <= 0) continue
    if (r.state === 'pending' && !r.own) out.waiting++
    else if (r.state !== 'pending' && r.mine) out.decided++
  }
  return out
}

export function seenAt(): string | null {
  let v = readJSON<unknown>(key(), null)
  // A mark kept before marks were per person carries over once.
  if (v === null && who) v = readJSON<unknown>(KEY, null)
  return typeof v === 'string' ? v : null
}

/** Records that every row up to these rows' newest has been seen. */
export function markSeen(items: ApprovalRow[]): void {
  const at = newestSeen(items)
  const was = seenAt()
  if (was === null || compareTimes(at, was) > 0) writeJSON(key(), at)
  window.dispatchEvent(new Event(SEEN_EVENT))
}

/** The first look sets the mark at what exists now, so older rows never flag. */
export function startSeen(items: ApprovalRow[]): string {
  const was = seenAt()
  if (was !== null) return was
  const at = newestSeen(items)
  writeJSON(key(), at)
  return at
}
