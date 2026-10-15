import { useCallback, useMemo, useState } from 'react'
import { defaultHidden, exportFileName, exportTable, csvText, matchesQuery, nextSort, queryWords, sortRows, visibleColumns, type ColumnDef, type ExportFormat, type SortState, type Term } from './table'
import { usePref } from './prefs'
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
}

/** The state every table keeps: sort, query, hidden columns, and the rows that result. */
export function useTable<R>({ id, columns, rows, terms, now, compact = false, defaultSort, query: outerQuery, onQuery }: Options<R>): TableState<R> {
  const key = `221b.${id}.hidden`
  const defaults = useMemo(() => defaultHidden(columns), [columns])
  const [sort, setSort] = useState<SortState>(defaultSort)
  const [innerQuery, setInnerQuery] = useState('')
  const query = outerQuery ?? innerQuery
  const setQuery = onQuery ?? setInnerQuery
  // Kept in this browser, or with the person's account (lib/prefs).
  const [saved, setSaved] = usePref<string[] | null>(key, null)
  const hidden = useMemo(() => {
    if (!saved) return new Set(defaults)
    const ids = new Set(columns.map((c) => c.id))
    return new Set(saved.filter((c) => ids.has(c)))
  }, [saved, columns, defaults])
  const setHidden = useCallback((h: Set<string>) => setSaved([...h]), [setSaved])
  const onSort = useCallback((col: string) => setSort((s) => nextSort(columns, s, col)), [columns])
  const cols = useMemo(() => visibleColumns(columns, hidden, compact), [columns, hidden, compact])
  const words = useMemo(() => queryWords(query), [query])
  const filtered = useMemo(() => (words.length ? rows.filter((r) => matchesQuery(terms(r), words)) : rows), [rows, terms, words])
  const shown = useMemo(() => sortRows(filtered, columns, sort, now), [filtered, columns, sort, now])
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
