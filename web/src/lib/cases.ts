// The Cases area (Phase 12, CS-01 to CS-05): who sees it and which of its
// tabs, what waits for a person in it, and the issue service's view of who
// is signed in, which the console's pages share.
import { useCallback, useSyncExternalStore } from 'react'
import type { Me } from '../api/types'
import { fetchIssueMe, type IssueMe, type OpKnown } from '../api/issues'
import type { CasesTab } from './router'

// ---------- who is signed in, as the issue service sees them ----------

let issueMe: IssueMe | null = null
let issueMeAsked = false
let issueMeSettled = false
const meListeners = new Set<() => void>()

function askIssueMe() {
  if (issueMeAsked) return
  issueMeAsked = true
  // Off (the chart's issues.enabled) or unreachable, there is nothing to
  // report to and no report to triage: the links and tabs stay away.
  fetchIssueMe(true).then(
    (m) => {
      issueMe = m
      issueMeSettled = true
      for (const fn of meListeners) fn()
    },
    () => {
      issueMeSettled = true
      for (const fn of meListeners) fn()
    },
  )
}

/** Who is signed in, as the issue service sees them, once per page; null while unknown or when the service is off. */
export function useIssueMe(): IssueMe | null {
  return useSyncExternalStore(subscribeMe, readMe, readMe)
}

function subscribeMe(fn: () => void) {
  meListeners.add(fn)
  askIssueMe()
  return () => {
    meListeners.delete(fn)
  }
}

const readMe = () => issueMe

/** Whether the issue service has answered (or failed to): until then, what it allows is not known yet. */
export function useIssueMeSettled(): boolean {
  return useSyncExternalStore(subscribeMe, readSettled, readSettled)
}

const readSettled = () => issueMeSettled

/** For tests: says who the issue service sees, or forgets it. */
export function setIssueMeForTests(m: IssueMe | null, asked = m !== null): void {
  issueMe = m
  issueMeAsked = asked
  issueMeSettled = asked
  for (const fn of meListeners) fn()
}

/** Whether the person may report a problem: the Report permission, or an operator's role (IS-20). */
export function mayReport(m: IssueMe | null): boolean {
  return !!m && (m.report || m.operator)
}

// ---------- the area and its tabs (CS-01, CS-02) ----------

export interface CasesAccess {
  /** Whether the area is offered at all. */
  show: boolean
  /** Decides approvals: today a SuperAdmin (the approval permission). */
  approver: boolean
  /** Triages every report and writes known issues (issues.operatorRoles). */
  operator: boolean
  /** Holds the Report permission: their own and their services' reports. */
  reporter: boolean
  /** A console-wide role (SuperAdmin, SuperViewer): runs, jobs and the wiki. */
  console: boolean
  /** The tabs offered, in order. */
  tabs: CasesTab[]
}

/**
 * Who sees Cases and which tabs (ADR-88): anyone with something there.
 * Approvers and operators get the Inbox; anyone who may report gets
 * Reports and Known issues, scoped as the issue service scopes them; the
 * console-wide roles get Runs, Jobs and Knowledge. A person who holds
 * only the Reporter role never reaches the console (IS-20).
 */
export function casesAccess(me: Me | null, im: IssueMe | null): CasesAccess {
  const approver = !!me && me.admin
  const operator = !!im?.operator
  const reporter = mayReport(im)
  const console = !!me && (me.admin || (me.roles ?? []).includes('superViewer'))
  const tabs: CasesTab[] = []
  if (approver || operator || console || reporter) tabs.push('inbox')
  if (console) tabs.push('jobs')
  if (console || operator || reporter) tabs.push('mycroft')
  return { show: tabs.length > 0, approver, operator, reporter, console, tabs }
}

// ---------- what waits for a person (CS-03, CS-04, IB-01 to IB-03) ----------

// --- Inbox as one table (Phase 12, wave 5, IN-01 to IN-04) ---

/** The kinds of the Inbox's rows (IN-02): what waits for a person, or did. Findings are not a kind since wave 6: each becomes a case or joins one. Since CF-87 a suggested validation rule is one, a SuperAdmin's. */
export type InboxKind = 'case' | 'proposal' | 'approval' | 'known' | 'knowledge' | 'rule'

/** The kinds, with a row's word and the plural the Inbox's errors use; "Proposed change" is what the agents' proposals are called. Since wave 6c no filter offers them (CF-38). */
export const INBOX_KINDS: { id: InboxKind; label: string; plural: string }[] = [
  { id: 'case', label: 'Case', plural: 'Cases' },
  { id: 'proposal', label: 'Proposed change', plural: 'Proposed changes' },
  { id: 'approval', label: 'Approval', plural: 'Approvals' },
  { id: 'known', label: 'Known issue', plural: 'Known issues' },
  { id: 'knowledge', label: 'Knowledge draft', plural: 'Knowledge drafts' },
  { id: 'rule', label: 'Suggested rule', plural: 'Suggested rules' },
]

// The groups' names before the Inbox was one table, which old links and
// Administration › Approvals' address still use: ?kind=approvals.
const OLD_KINDS: Record<string, InboxKind> = { approvals: 'approval', proposals: 'proposal', cases: 'case', reports: 'case' }

/** The kind an address asks for, old group names too; null for none or one unknown. */
export function inboxKindOf(v: string | null): InboxKind | null {
  if (!v) return null
  if (INBOX_KINDS.some((k) => k.id === v)) return v as InboxKind
  return OLD_KINDS[v] ?? null
}

/** Whose: everything, the person's own, or what nobody has taken. */
export type Who = 'all' | 'mine' | 'unassigned'

/** What the Inbox orders an item by (IB-03). */
export interface InboxOrder {
  /** Waits for someone (not decided, not done). */
  waiting: boolean
  /** P0 to P4; none sorts after P4. */
  priority?: number
  /** Since when it has waited; for what was done, when. */
  since: string
}

/**
 * The Inbox's order (IB-03, IN-03): what waits first, then by priority
 * (P0 first), then the oldest waiting first; after them what was done,
 * newest first. Equal items keep their order.
 */
export function inboxOrder(a: InboxOrder, b: InboxOrder): number {
  if (a.waiting !== b.waiting) return a.waiting ? -1 : 1
  if (!a.waiting) return b.since.localeCompare(a.since)
  return (a.priority ?? 6) - (b.priority ?? 6) || a.since.localeCompare(b.since)
}

/**
 * The same order as one number, smallest first, for a table column's sort
 * (IN-03): waiting rows by priority, then since when; done rows after
 * every waiting one, newest first.
 */
export function inboxOrderKey(o: InboxOrder): number {
  const at = Date.parse(o.since) || 0
  // Milliseconds since 1970 stay below 1e13 until the year 2286.
  return o.waiting ? (o.priority ?? 6) * 1e13 + at : 1e14 + (1e13 - at)
}

// --- end Inbox as one table ---

/** The known issues an agent proposed, waiting for an operator to accept them. */
export function knownDrafts(list: OpKnown[], me: string, who: Who = 'all'): OpKnown[] {
  return list.filter((k) => k.draft && (who === 'all' || (who === 'mine' ? k.owner?.id === me : !k.owner)))
}

/**
 * Whether a known issue is pending for its owner (wave 6): resolved, but
 * the agents still saw it, so it is investigating again and waits for the
 * person who owns it.
 */
export const knownReopened = (k: OpKnown, me: string) => !k.draft && !!me && k.owner?.id === me && k.status === 'investigating' && !!k.stillPresent

/** The known issues pending in the Inbox: the agents' proposals, and those back for their owner. */
export function knownPending(list: OpKnown[], me: string): OpKnown[] {
  return list.filter((k) => k.draft || knownReopened(k, me))
}

// ---------- a list read again on a timer, shared ----------

interface Shared<T> {
  value: T | null
  error: string
  listeners: Set<() => void>
  timer?: ReturnType<typeof setInterval>
  load: () => void
  /** Starts the reading with the first listener and stops it with the last. */
  subscribe: (fn: () => void, every: number) => () => void
  snapshot: { value: T | null; error: string }
}

const shared = new Map<string, Shared<unknown>>()

/**
 * A list several parts of the page show (the top bar's count and the
 * Inbox), read once for all of them and again every `every` ms while any
 * shows it; reload reads it at once, after a change.
 */
export function useShared<T>(key: string | null, load: (background: boolean) => Promise<T>, every = 30_000): { value: T | null; error: string; reload: () => void } {
  const live = key ? entryFor(key, load) : null
  // One subscription per list and rate, so a render does not start the
  // reading again.
  const subscribe = useCallback((fn: () => void) => (live ? live.subscribe(fn, every) : () => undefined), [live, every])
  const snap = useSyncExternalStore(
    subscribe,
    () => live?.snapshot ?? EMPTY,
    () => live?.snapshot ?? EMPTY,
  )
  return { value: snap.value as T | null, error: snap.error, reload: () => live?.load() }
}

const EMPTY = { value: null, error: '' }

function entryFor<T>(key: string, load: (background: boolean) => Promise<T>): Shared<T> {
  let e = shared.get(key) as Shared<T> | undefined
  if (e) return e
  const made: Shared<T> = {
    value: null,
    error: '',
    listeners: new Set(),
    snapshot: EMPTY as { value: T | null; error: string },
    subscribe: () => () => undefined,
    load: () => {
      load(made.value !== null).then(
        (v) => {
          made.value = v
          made.error = ''
          made.snapshot = { value: v, error: '' }
          for (const fn of made.listeners) fn()
        },
        (err: unknown) => {
          made.error = err instanceof Error ? err.message : String(err)
          made.snapshot = { value: made.value, error: made.error }
          for (const fn of made.listeners) fn()
        },
      )
    },
  }
  made.subscribe = (fn, every) => {
    made.listeners.add(fn)
    if (made.listeners.size === 1) {
      made.load()
      made.timer = setInterval(made.load, every)
    }
    return () => {
      made.listeners.delete(fn)
      if (made.listeners.size === 0) clearInterval(made.timer)
    }
  }
  e = made
  shared.set(key, e as Shared<unknown>)
  return e
}

/** For tests: forgets every shared list. */
export function forgetSharedForTests(): void {
  for (const e of shared.values()) clearInterval(e.timer)
  shared.clear()
}
