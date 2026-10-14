// What every table in the console shares: column definitions, sorting,
// searching with suggestions, and the exports (X-13). The tabs supply
// their columns; DataTable and TableToolbar do the rest.

import type { ReactNode } from 'react'
import { compareNatural, compareValues, fileStamp } from './format'
import type { Cell } from './xlsx'

export type SortDir = 'asc' | 'desc'

/** One exported column: Excel and CSV get the cell, JSON gets json when set. */
export interface ExportValue {
  head: string
  cell: Cell
  json?: unknown
}

export interface ColumnDef<R> {
  id: string
  label: string
  /** How the sort tooltip names the column; the label, lower-cased, when absent. */
  sortName?: string
  /** Grid track, and a narrower one while a details panel is open. */
  width: string
  compactWidth?: string
  /** Minimum width in px, for the table's min-width. */
  min: number
  compactMin?: number
  right?: boolean
  /** Dropped while a details panel is open, as in the prototype. */
  wideOnly?: boolean
  /** Off until picked in the Columns menu. */
  hiddenByDefault?: boolean
  defaultDir?: SortDir
  /** Columns sharing a group get one heading above theirs (Services' External). */
  group?: string
  /** The first column: always shown, and the row's accessible name. */
  sortValue: (r: R, now: number) => number | string | null
  cell: (r: R, now: number) => ReactNode
  /** The export columns this column becomes; one or more. */
  export: (r: R, now: number) => ExportValue[]
}

export interface SortState {
  col: string
  dir: SortDir
}

/** The columns on screen: the first always, then the user's choice, less the wide-only ones while a panel is open. */
export function visibleColumns<R>(cols: ColumnDef<R>[], hidden: ReadonlySet<string>, compact: boolean): ColumnDef<R>[] {
  return cols.filter((c, i) => i === 0 || (!hidden.has(c.id) && !(compact && c.wideOnly)))
}

export function defaultHidden<R>(cols: ColumnDef<R>[]): Set<string> {
  return new Set(cols.filter((c) => c.hiddenByDefault).map((c) => c.id))
}

export function gridTemplate<R>(cols: ColumnDef<R>[], compact: boolean): { template: string; minWidth: number } {
  return {
    template: cols.map((c) => (compact ? (c.compactWidth ?? c.width) : c.width)).join(' '),
    minWidth: cols.reduce((s, c) => s + (compact ? (c.compactMin ?? c.min) : c.min), 0),
  }
}

/** Sorts by a column; empty values last, ties in natural order of the first column. */
export function sortRows<R>(rows: R[], cols: ColumnDef<R>[], sort: SortState, now: number): R[] {
  const col = cols.find((c) => c.id === sort.col) ?? cols[0]
  const first = cols[0]
  if (!col || !first) return rows
  const sign = sort.dir === 'asc' ? 1 : -1
  const keyed = rows.map((r) => ({ r, v: col.sortValue(r, now), n: String(first.sortValue(r, now) ?? '') }))
  keyed.sort((a, b) => {
    const an = a.v == null || a.v === ''
    const bn = b.v == null || b.v === ''
    if (an !== bn) return an ? 1 : -1
    const c = an ? 0 : compareValues(a.v as number | string, b.v as number | string)
    return c ? c * sign : compareNatural(a.n, b.n)
  })
  return keyed.map((k) => k.r)
}

/** Toggles a sort: the same column reverses, another starts in its default direction. */
export function nextSort<R>(cols: ColumnDef<R>[], sort: SortState, col: string): SortState {
  if (sort.col === col) return { col, dir: sort.dir === 'asc' ? 'desc' : 'asc' }
  return { col, dir: cols.find((c) => c.id === col)?.defaultDir ?? 'asc' }
}

// ---- search ----

/** A term the search box matches and suggests: "label", "key=value". */
export interface Term {
  kind: string
  text: string
}

export interface Suggestion extends Term {
  count: number
}

/** Splits a query into the words that must all match. */
export function queryWords(query: string): string[] {
  return query.trim().toLowerCase().split(/\s+/).filter(Boolean)
}

/** A row matches when every word of the query is found in one of its terms. */
export function matchesQuery(terms: Term[], words: string[]): boolean {
  if (!words.length) return true
  return words.every((w) => terms.some((t) => t.text.toLowerCase().includes(w)))
}

/**
 * The terms that complete the last word typed, with how many rows carry
 * each, most common first: names, labels, taints, namespaces and statuses
 * as the table's terms say.
 */
export function suggestions<R>(rows: R[], terms: (r: R) => Term[], query: string, limit = 8): Suggestion[] {
  const words = queryWords(query)
  const last = query.endsWith(' ') ? '' : (words[words.length - 1] ?? '')
  if (!last) return []
  const before = query.endsWith(' ') ? words : words.slice(0, -1)
  const counts = new Map<string, Suggestion>()
  for (const r of rows) {
    const ts = terms(r)
    if (!matchesQuery(ts, before)) continue
    const seen = new Set<string>()
    for (const t of ts) {
      const lower = t.text.toLowerCase()
      if (lower === last || !lower.includes(last) || seen.has(lower)) continue
      seen.add(lower)
      const k = t.kind + '\0' + lower
      const s = counts.get(k)
      if (s) s.count++
      else counts.set(k, { kind: t.kind, text: t.text, count: 1 })
    }
  }
  const out = [...counts.values()]
  out.sort((a, b) => {
    // Prefix matches first, then the common ones, then alphabetically.
    const ap = a.text.toLowerCase().startsWith(last) ? 0 : 1
    const bp = b.text.toLowerCase().startsWith(last) ? 0 : 1
    return ap - bp || b.count - a.count || compareNatural(a.text, b.text)
  })
  return out.slice(0, limit)
}

/** Replaces the last word of a query with a suggestion. */
export function applySuggestion(query: string, s: Suggestion): string {
  if (query.endsWith(' ') || !query.trim()) return query + s.text + ' '
  const words = query.trimEnd().split(/\s+/)
  words[words.length - 1] = s.text
  return words.join(' ') + ' '
}

// ---- exports ----

export type ExportFormat = 'xlsx' | 'csv' | 'json'

export const EXPORT_FORMATS: { id: ExportFormat; label: string; ext: string; mime: string }[] = [
  { id: 'xlsx', label: 'Excel', ext: '.xlsx', mime: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet' },
  { id: 'csv', label: 'CSV', ext: '.csv', mime: 'text/csv;charset=utf-8' },
  { id: 'json', label: 'JSON', ext: '.json', mime: 'application/json' },
]

/** The export table: a header row and one row per screen row, column by column. */
export function exportTable<R>(rows: R[], cols: ColumnDef<R>[], now: number): { head: string[]; rows: Cell[][]; json: Record<string, unknown>[] } {
  const head: string[] = []
  const first = rows[0]
  const sample = first === undefined ? [] : cols.flatMap((c) => c.export(first, now))
  for (const v of sample) head.push(v.head)
  const out: Cell[][] = []
  const json: Record<string, unknown>[] = []
  for (const r of rows) {
    const values = cols.flatMap((c) => c.export(r, now))
    if (!head.length) for (const v of values) head.push(v.head)
    out.push(values.map((v) => v.cell))
    json.push(Object.fromEntries(values.map((v) => [v.head, v.json !== undefined ? v.json : v.cell])))
  }
  return { head, rows: out, json }
}

function csvField(c: Cell): string {
  if (c == null) return ''
  const s = typeof c === 'string' ? c : String(c)
  return /[",\r\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s
}

/** CSV with a byte-order mark and CRLF line ends, so Excel opens it as UTF-8. */
export function csvText(head: string[], rows: Cell[][]): string {
  const lines = [head, ...rows].map((r) => r.map(csvField).join(','))
  return '﻿' + lines.join('\r\n') + '\r\n'
}

/** A file name: cluster-nodes-20261013-1403.xlsx. */
export function exportFileName(cluster: string | undefined, what: string, format: ExportFormat): string {
  const name = (cluster || 'cluster').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
  const ext = EXPORT_FORMATS.find((f) => f.id === format)?.ext ?? ''
  return `${name}-${what}-${fileStamp()}${ext}`
}
