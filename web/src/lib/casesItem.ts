// The item open in Cases (Phase 12, IB-11, IB-12, IN-06): a case, a
// proposed change, an approval, a draft, a run, a job. Sherlock is told about it with the
// view (lib/uiControl.ts viewState), and the dock follows its cluster, so
// "this case" in a question is the one open. A drawer says what it shows
// with useCasesItem; what the address names is known without it.
import { useEffect, useSyncExternalStore } from 'react'
import { proposalId } from '../api/cards'
import type { Route } from './router'

export type CasesItemKind = 'case' | 'proposal' | 'approval' | 'knowledge' | 'run' | 'job' | 'report' | 'known' | 'exception' | 'page'

export interface CasesItem {
  kind: CasesItemKind
  id: string
  /** The cluster it is about, when it is about one. */
  cluster?: string
  /** One line: its title, status and priority, as the page shows them. */
  summary?: string
}

// The items drawers show, the newest last: a drawer over another names
// the one on top.
const open: { key: number; item: CasesItem }[] = []
const listeners = new Set<() => void>()
let seq = 0
let top: CasesItem | null = null

function changed() {
  top = open.length ? open[open.length - 1]!.item : null
  for (const fn of listeners) fn()
}

/** Says, while the calling drawer is open, which item it shows. */
export function useCasesItem(item: CasesItem | null): void {
  const key = item ? JSON.stringify(item) : ''
  useEffect(() => {
    if (!key) return
    const mine = { key: ++seq, item: JSON.parse(key) as CasesItem }
    open.push(mine)
    changed()
    return () => {
      const i = open.indexOf(mine)
      if (i >= 0) open.splice(i, 1)
      changed()
    }
  }, [key])
}

/** The item a drawer says it shows now, or null. */
export function openCasesItem(): CasesItem | null {
  return top
}

function subscribe(fn: () => void) {
  listeners.add(fn)
  return () => {
    listeners.delete(fn)
  }
}

export function useOpenCasesItem(): CasesItem | null {
  return useSyncExternalStore(subscribe, openCasesItem, openCasesItem)
}

/** The kinds of the Inbox's drawers, as its addresses name them: #/cases/inbox/<kind>/<id>. */
const INBOX_ITEMS = new Set<string>(['case', 'proposal', 'approval', 'known', 'knowledge', 'report'])

/** What a Cases address names open: #/cases/inbox/case/12, #/cases/mycroft/known/KI-3, #/cases/mycroft/wiki/pages/runbooks/dns.md, #/cases/jobs/runs/r-9… */
export function casesItemOf(route: Pick<Route, 'view' | 'path' | 'params'>): CasesItem | null {
  if (route.view !== 'cases') return null
  const [page, a, b] = route.path
  const p = route.params
  // The Inbox's addresses (IN-06), and the query its links had before.
  if (page === 'inbox' && a && b && INBOX_ITEMS.has(a)) {
    // A report is the case of the same number (CU-01).
    if (a === 'case' || a === 'report') return b === 'new' ? null : { kind: 'case', id: /^\d+$/.test(b) ? `C-${b}` : b }
    // A proposed change's c<n> of before is its PC-<n> (CF-56).
    return { kind: a as CasesItemKind, id: a === 'proposal' ? proposalId(b) : b }
  }
  if (p.get('proposal')) return { kind: 'proposal', id: proposalId(p.get('proposal')!) }
  if (p.get('report')) return { kind: 'case', id: `C-${p.get('report')!}` }
  if (page === 'mycroft' && a === 'known' && b && b !== 'new') return { kind: 'known', id: b }
  if (page === 'mycroft' && a === 'exceptions' && b) return { kind: 'exception', id: b }
  if (page === 'mycroft' && a === 'wiki' && b === 'pages' && route.path.length > 3) return { kind: 'page', id: route.path.slice(3).join('/') }
  if (page === 'jobs' && a === 'runs' && b) return { kind: 'run', id: b }
  if (page === 'jobs' && a && a !== 'runs') return { kind: 'job', id: a }
  return null
}

/** The item open: what a drawer says, else what the address names. */
export function currentCasesItem(route: Pick<Route, 'view' | 'path' | 'params'>): CasesItem | null {
  if (route.view !== 'cases') return null
  return top ?? casesItemOf(route)
}

/** For tests: forgets what drawers said. */
export function forgetCasesItemsForTests(): void {
  open.length = 0
  changed()
}
