import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { defaultHidden, exportFileName, exportTable, csvText, matchesQuery, nextSort, queryWords, sortRows, visibleColumns, type ColumnDef, type ExportFormat, type SortState, type Term } from './table'
import { usePref } from './prefs'
import { noteUserChange, registerTable, tableOfRows, type TableHandle } from './tables'
import { download, xlsxBytes } from './xlsx'

export interface TableState<R> {
  sort: SortState
  onSort: (col: string) => void
  query: string
  setQuery: (q: string) => void
  hidden: Set<string>
  setHidden: (h: Set<string>) => void
  defaults: Set<string>
  /** The columns on screen. */
  cols: ColumnDef<R>[]
  /** The rows on screen: filtered by the query and sorted. */
  shown: R[]
  /** Downloads the rows and columns on screen. */
  exportRows: (format: ExportFormat, cluster: string | undefined, what: string, onToast: (msg: string) => void) => void
}

interface Options<R> {
  /** Remembers the hidden columns per browser under this key. */
  id: string
  columns: ColumnDef<R>[]
  rows: R[]
  terms: (r: R) => Term[]
  now: number
  compact?: boolean
  defaultSort: SortState
  /** The table's query when it comes from the URL; otherwise it is kept here. */
  query?: string
  onQuery?: (q: string) => void
  /** What Sherlock may drive (P6U-01); all by default. A table that only holds column choices for others drives just its columns. */
  drives?: ('sort' | 'search' | 'columns')[]
}

const ALL_DRIVES: ('sort' | 'search' | 'columns')[] = ['sort', 'search', 'columns']

/** The state every table keeps: sort, query, hidden columns, and the rows that result. */
export function useTable<R>({ id, columns, rows, terms, now, compact = false, defaultSort, query: outerQuery, onQuery, drives = ALL_DRIVES }: Options<R>): TableState<R> {
  const key = `221b.${id}.hidden`
  const defaults = useMemo(() => defaultHidden(columns), [columns])
  const [sort, setSort] = useState<SortState>(defaultSort)
  const [innerQuery, setInnerQuery] = useState('')
  const query = outerQuery ?? innerQuery
  const rawSetQuery = onQuery ?? setInnerQuery
  const setQuery = useCallback(
    (q: string) => {
      noteUserChange()
      rawSetQuery(q)
    },
    [rawSetQuery],
  )
  // Kept in this browser (lib/prefs).
  const [saved, setSaved] = usePref<string[] | null>(key, null)
  // Sherlock's column choice lasts until reload or the person's own (P6U-11).
  const [override, setOverride] = useState<Set<string> | null>(null)
  const hidden = useMemo(() => {
    if (override) return override
    if (!saved) return new Set(defaults)
    const ids = new Set(columns.map((c) => c.id))
    return new Set(saved.filter((c) => ids.has(c)))
  }, [saved, columns, defaults, override])
  const setHidden = useCallback(
    (h: Set<string>) => {
      noteUserChange()
      setOverride(null)
      setSaved([...h])
    },
    [setSaved],
  )
  const onSort = useCallback(
    (col: string) => {
      noteUserChange()
      setSort((s) => nextSort(columns, s, col))
    },
    [columns],
  )
  const cols = useMemo(() => visibleColumns(columns, hidden, compact), [columns, hidden, compact])
  const words = useMemo(() => queryWords(query), [query])
  const filtered = useMemo(() => (words.length ? rows.filter((r) => matchesQuery(terms(r), words)) : rows), [rows, terms, words])
  const shown = useMemo(() => sortRows(filtered, columns, sort, now), [filtered, columns, sort, now])
  tableOfRows.set(shown, id)

  // Registered under its id while mounted, read through a ref so the
  // handle always sees this render's state.
  const latest = useRef({ sort, query, hidden, override, shown, rows, columns, now, rawSetQuery })
  useLayoutEffect(() => {
    latest.current = { sort, query, hidden, override, shown, rows, columns, now, rawSetQuery }
  })
  const drivesKey = drives.join(',')
  useEffect(() => {
    const nameOf = (r: R): string => {
      const { columns, now } = latest.current
      const first = String(columns[0]?.sortValue(r, now) ?? '')
      const ns = columns.find((c) => c.id === 'namespace' || c.id === 'ns')
      const nsv = ns ? ns.sortValue(r, now) : null
      return typeof nsv === 'string' && nsv ? `${nsv}/${first}` : first
    }
    const h: TableHandle = {
      id,
      drives: new Set(drivesKey.split(',') as ('sort' | 'search' | 'columns')[]),
      get columns() {
        return latest.current.columns.map((c, i) => ({ id: c.id, label: c.label, first: i === 0 }))
      },
      sort: () => latest.current.sort,
      setSort: (s) => setSort(s),
      query: () => latest.current.query,
      setQuery: (q) => latest.current.rawSetQuery(q),
      hidden: () => latest.current.hidden,
      override: () => latest.current.override,
      setOverride: (o) => setOverride(o),
      shown: () => latest.current.shown.length,
      total: () => latest.current.rows.length,
      names: (n) => latest.current.shown.slice(0, n).map(nameOf),
      find: (name) => {
        const want = name.trim().toLowerCase()
        const shown = latest.current.shown
        const r = shown.find((x) => nameOf(x).toLowerCase() === want) ?? shown.find((x) => nameOf(x).toLowerCase().endsWith('/' + want))
        return (r as object | undefined) ?? null
      },
    }
    return registerTable(h)
  }, [id, drivesKey])

  const exportRows = useCallback(
    (format: ExportFormat, cluster: string | undefined, what: string, onToast: (msg: string) => void) => {
      const file = exportFileName(cluster, what, format)
      try {
        const t = exportTable(shown, cols, now)
        if (format === 'xlsx') download(xlsxBytes(what.replace(/[^A-Za-z0-9 ]/g, ' ').slice(0, 31) || 'Sheet', t.head, t.rows), file)
        else if (format === 'csv') download(csvText(t.head, t.rows), file, 'text/csv;charset=utf-8')
        else download(JSON.stringify(t.json, null, 2) + '\n', file, 'application/json')
        onToast(`Downloaded ${file}`)
      } catch {
        onToast(`Couldn’t build the ${format === 'xlsx' ? 'spreadsheet' : format.toUpperCase() + ' file'}.`)
      }
    },
    [shown, cols, now],
  )
  return { sort, onSort, query, setQuery, hidden, setHidden, defaults, cols, shown, exportRows }
}
