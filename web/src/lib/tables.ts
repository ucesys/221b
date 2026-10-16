// The tables on screen, by their id, so Sherlock can read and set their
// sort, search text and columns (P6U-01), and the row it highlights. Every
// table built with useTable registers here while it is mounted. Nothing
// here changes how a table behaves when nobody drives it.

import type { SortState } from './table'

export interface TableHandle {
  id: string
  /** What can be driven: a table that only holds column choices for others drives nothing else. */
  drives: ReadonlySet<'sort' | 'search' | 'columns'>
  columns: { id: string; label: string; first: boolean }[]
  sort(): SortState
  setSort(s: SortState): void
  query(): string
  setQuery(q: string): void
  hidden(): ReadonlySet<string>
  /** Sherlock's column choice, kept until reload or the person's own (P6U-11); null for none. */
  override(): ReadonlySet<string> | null
  setOverride(h: Set<string> | null): void
  shown(): number
  total(): number
  /** The names of the first rows shown: the first column, with the namespace where the table has one. */
  names(n: number): string[]
  /** The row shown with this name, if any. */
  find(name: string): object | null
}

const tables = new Map<string, TableHandle>()
const order: string[] = []

/** Registers a table while it is mounted; the latest one with an id wins. */
export function registerTable(h: TableHandle): () => void {
  tables.set(h.id, h)
  const i = order.indexOf(h.id)
  if (i >= 0) order.splice(i, 1)
  order.push(h.id)
  return () => {
    if (tables.get(h.id) === h) {
      tables.delete(h.id)
      const j = order.indexOf(h.id)
      if (j >= 0) order.splice(j, 1)
    }
  }
}

/** The tables on screen, in the order they mounted. */
export function tableHandles(): TableHandle[] {
  return order.map((id) => tables.get(id)).filter((t): t is TableHandle => !!t)
}

export function tableHandle(id: string): TableHandle | undefined {
  return tables.get(id)
}

// ---- who changed the view ----

let driving = 0
let drivenAt = 0
const userListeners = new Set<() => void>()

/** Runs a change Sherlock makes, so it does not count as the person's. */
export function drive<T>(fn: () => T): T {
  driving++
  drivenAt = Date.now()
  try {
    return fn()
  } finally {
    driving--
  }
}

/** Whether Sherlock changed the view a moment ago: what opens then does not take focus (P6U-09). */
export function drivenRecently(ms = 1500): boolean {
  return Date.now() - drivenAt < ms
}

/** Whether a change now is Sherlock's. */
export function isDriving(): boolean {
  return driving > 0
}

/** A table the person sorted, searched or changed the columns of. */
export function noteUserChange(): void {
  if (driving) return
  for (const fn of userListeners) fn()
}

export function onUserChange(fn: () => void): () => void {
  userListeners.add(fn)
  return () => userListeners.delete(fn)
}

// ---- the highlighted row (view_highlight) ----

export interface Highlight {
  table: string
  row: object
  /** A new highlight of the same row scrolls to it again. */
  seq: number
}

let highlight: Highlight | null = null
let seq = 0
let clearTimer: ReturnType<typeof setTimeout> | undefined
const highlightListeners = new Set<() => void>()

/** The rows array a table shows, by its table id, so DataTable knows which table it draws. */
export const tableOfRows = new WeakMap<object, string>()

export const HIGHLIGHT_MS = 4000

export function setHighlight(table: string, row: object): void {
  highlight = { table, row, seq: ++seq }
  clearTimeout(clearTimer)
  clearTimer = setTimeout(() => {
    highlight = null
    for (const fn of highlightListeners) fn()
  }, HIGHLIGHT_MS)
  for (const fn of highlightListeners) fn()
}

export function currentHighlight(): Highlight | null {
  return highlight
}

export function onHighlight(fn: () => void): () => void {
  highlightListeners.add(fn)
  return () => highlightListeners.delete(fn)
}
