// The issue service's API as the report page uses it (IS-23,
// docs/issues-api.yaml). A 401 sends the browser to sign in and back here.
import { call, post } from '../api/call'

// --- One flow for findings and cases (Phase 12, wave 6) ---
/** New: nobody has taken it; In progress: someone owns it; Closed: with exactly one outcome. */
export type Status = 'new' | 'inProgress' | 'closed'
/** How a case closed: fixed, the same as another, no action needed (not a problem, or it went away), a known issue, or an accepted exception. */
export type Outcome = 'fix' | 'duplicate' | 'noAction' | 'knownIssue' | 'exception'
/** The outcomes noAction replaced (CF-90): the server reads them as noAction, and so does this page if it meets one. */
export type OldOutcome = 'notAProblem' | 'noFixNeeded'
/** An outcome as it is now: notAProblem and noFixNeeded are noAction (CF-90). */
export const outcomeNow = (o: Outcome | OldOutcome): Outcome => (o === 'notAProblem' || o === 'noFixNeeded' ? 'noAction' : o)
export type KnownStatus = 'investigating' | 'identified' | 'fixInProgress' | 'resolved'

export interface Person {
  id: string
  name?: string
  email?: string
  system?: boolean
}

export interface Message {
  id: number
  at: string
  kind: 'reporter' | 'operator' | 'system'
  from: Person
  text: string
}

export interface KnownUpdate {
  at: string
  by: Person
  text: string
  status?: KnownStatus
}

/** A known issue as a reporter sees it. */
export interface Known {
  id: string
  title: string
  status: KnownStatus
  workaround?: string
  expected?: string
  kind: 'normal' | 'planned'
  window?: { start: string; end: string }
  link?: string
  updated: string
  lastUpdate?: KnownUpdate
  following: boolean
  /** Made inactive by the housekeeping as a duplicate of another; the server leaves these out for reporters (CF-63). */
  inactive?: boolean
}

/** A report as its reporter sees it. */
export interface Report {
  number: number
  text?: string
  scrubbed?: boolean
  pruned?: boolean
  reporter: Person
  filedBy?: Person
  created: string
  updated: string
  status: Status
  statusAt: string
  outcome?: Outcome | OldOutcome
  /** The explanation the case was closed with. */
  reason?: string
  /** 0 (P0, an outage) to 4 (P4, a question). */
  priority: number
  knownIssue?: string
  serviceName?: string
  known?: Known
  messages?: Message[]
  plain?: string
  waiting?: string
  mine: boolean
  watching: boolean
  canReopen: boolean
}

/** What the sender of a report is told. */
export interface Receipt {
  report: Report
  known?: Known
  removed: boolean
}

/** Who the page is for, and the settings it shows. */
export interface IssueMe {
  person: Person
  report: boolean
  operator: boolean
  admin: boolean
  company: string
  statusPage: string
  reportURL: string
  /** The connected clusters a report may be about: id and display name only (CF-64). */
  clusters?: Cluster[]
}

export interface Cluster {
  id: string
  name: string
}

/** What a report may say besides its text: for, when support files it for someone else; the priority (1 to 4) and the cluster the reporter chose (CF-64). */
export interface ReportChoice {
  for?: string
  priority?: number
  cluster?: string
}

export interface Notice {
  at: string
  report?: number
  known?: string
  text: string
}

const base = '/api/issues/v1/'

export const fetchIssueMe = () => call<IssueMe>(base + 'me')

export const fetchMine = (background = false) => call<{ reports: Report[] }>(base + 'reports', {}, background).then((r) => r.reports)

export const fetchKnown = (background = false) => call<{ known: Known[] }>(base + 'known', {}, background).then((r) => r.known)

/** Sends a report, with what else the person chose; left out, the server's defaults apply. */
export const sendReport = (text: string, choice: ReportChoice = {}) =>
  call<Receipt>(
    base + 'reports',
    post({
      text,
      ...(choice.for ? { for: { id: choice.for } } : {}),
      ...(choice.priority ? { priority: choice.priority } : {}),
      ...(choice.cluster ? { cluster: choice.cluster } : {}),
    }),
  )

export const unlinkReport = (n: number) => call<Report>(`${base}reports/${n}/unlink`, { method: 'POST' })

/** Opens a closed case again: the reporter says what still happens. */
export const reopenReport = (n: number, reason: string) => call<Report>(`${base}reports/${n}/reopen`, post({ reason }))

export const addMessage = (n: number, text: string) => call<Report>(`${base}reports/${n}/messages`, post({ text }))

export const followKnown = (id: string, on: boolean) => call<Known>(`${base}known/${encodeURIComponent(id)}/follow`, { method: on ? 'PUT' : 'DELETE' })

export const fetchUpdates = (since: string) => call<{ updates: Notice[]; now: string }>(`${base}updates?since=${encodeURIComponent(since)}`, {}, true)

/** A case's status as its reporter reads it, with the tone of its pill: an open case that waits for their answer says so (CF-67). */
export function statusPill(r: Pick<Report, 'status' | 'outcome' | 'knownIssue' | 'waiting'>): { text: string; tone: 'ok' | 'warn' | 'err' | 'info' | 'muted' } {
  if (r.waiting && r.status !== 'closed') return { text: 'Waiting for your answer', tone: 'warn' }
  switch (r.status) {
    case 'new':
      return { text: 'Received', tone: 'muted' }
    case 'inProgress':
      return { text: 'Being worked on', tone: 'info' }
    case 'closed':
      if (r.outcome === 'fix') return { text: 'Fixed', tone: 'ok' }
      if (r.outcome === 'knownIssue') return { text: 'A known problem', tone: 'warn' }
      return { text: 'Closed: ' + (r.outcome ? outcomeWords[outcomeNow(r.outcome)] : 'done'), tone: 'muted' }
    default:
      return { text: String(r.status), tone: 'muted' }
  }
}

/** The priorities, 0 to 4, everywhere: cases, the API, this page and the SLA's targets. */
export const PRIORITY_NAMES: Record<number, string> = {
  0: 'Outage',
  1: 'Blocking',
  2: 'Service degradation',
  3: 'Preemptive',
  4: 'Question',
}

/**
 * The priorities a reporter may choose, 1 to 4, each with what it means in
 * a few words so not everything is P1 (CF-64). P0, an outage, is the
 * operators' call; not chosen, a report is P2.
 */
export const REPORT_PRIORITIES: [number, string][] = [
  [1, "you can't work, and nothing gets around it"],
  [2, 'slow or partly broken, but you can work'],
  [3, 'not broken yet, but will be if left'],
  [4, 'a question or a request'],
]

/** A priority in words: "P2: Service degradation". */
export const priorityText = (p: number) => (PRIORITY_NAMES[p] ? `P${p}: ${PRIORITY_NAMES[p]}` : `P${p}`)

/** The outcomes as a reporter reads them, after "Closed: ". */
export const outcomeWords: Record<Outcome, string> = {
  fix: 'fixed',
  duplicate: 'same as another case',
  noAction: 'no action needed',
  knownIssue: 'a known problem',
  exception: 'accepted as it is',
}

// Words too common to match a problem by.
const COMMON = new Set(['about', 'after', 'again', 'also', 'because', 'been', 'cannot', 'could', 'does', 'doesn', 'every', 'from', 'have', 'into', 'just', 'like', 'more', 'only', 'other', 'please', 'since', 'some', 'than', 'that', 'there', 'their', 'them', 'they', 'this', 'when', 'where', 'which', 'while', 'with', 'will', 'would', 'your'])

const words = (t: string) => new Set((t.toLowerCase().match(/[a-z0-9][a-z0-9-]{3,}/g) ?? []).filter((w) => !COMMON.has(w)))

/**
 * The known problems what a person types may be (wave 6): open known
 * issues sharing words with it, best first, at most three. Only known
 * issues and their workarounds, never anyone's cases; the server matches
 * again when the case is sent.
 */
export function matchKnown(text: string, list: Known[]): Known[] {
  const said = words(text)
  if (said.size < 2) return []
  const scored: [Known, number][] = []
  for (const k of list) {
    if (k.status === 'resolved' || k.inactive) continue
    const title = words(k.title)
    let hits = 0
    for (const w of title) if (said.has(w)) hits++
    // A workaround's words count half.
    for (const w of words(k.workaround ?? '')) if (said.has(w) && !title.has(w)) hits += 0.5
    const n = title.size || 1
    if (hits >= 2 || hits / n >= 0.5) scored.push([k, hits / n + hits / 10])
  }
  return scored.sort((a, b) => b[1] - a[1]).slice(0, 3).map(([k]) => k)
}

/** A known issue's status as people read it, with its tone. */
export function knownPill(k: Pick<Known, 'status' | 'kind'>): { text: string; tone: 'ok' | 'warn' | 'err' | 'info' | 'muted' } {
  switch (k.status) {
    case 'investigating':
      return { text: 'Investigating', tone: 'err' }
    case 'identified':
      return { text: 'Identified', tone: 'info' }
    case 'fixInProgress':
      return { text: 'Fix in progress', tone: 'warn' }
    case 'resolved':
      return { text: 'Resolved', tone: 'ok' }
  }
}
