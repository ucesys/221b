import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { ClusterInfo, NodeRow } from '../api/types'
import { fileStamp } from '../lib/format'
import {
  COLUMN_BY_ID,
  capacityCards,
  exportSheet,
  filterRows,
  gridTemplate,
  sortRows,
  summaryLine,
  visibleColumns,
  type ColumnId,
  type RoleFilter,
  type SortDir,
} from '../lib/nodes'
import { readJSON, writeJSON } from '../lib/storage'
import { download, xlsxBytes } from '../lib/xlsx'
import { CapacityCard } from './CapacityCard'
import { ColumnsMenu } from './ColumnsMenu'
import { ExcelIcon, SearchIcon } from './Icons'
import { NodePanel } from './NodePanel'
import { NodeTable } from './NodeTable'

interface Props {
  rows: NodeRow[]
  cluster: ClusterInfo | null
  unscheduled: number
  now: number
  onToast: (msg: string) => void
}

const HIDDEN_KEY = '221b.nodes.hiddenColumns'

export function NodesTab({ rows, cluster, unscheduled, now, onToast }: Props) {
  const [role, setRole] = useState<RoleFilter>('all')
  const [query, setQuery] = useState('')
  const [sort, setSort] = useState<{ col: ColumnId; dir: SortDir }>({ col: 'name', dir: 'asc' })
  const [hidden, setHidden] = useState<Set<ColumnId>>(() => new Set(readJSON<ColumnId[]>(HIDDEN_KEY, []).filter((id) => id in COLUMN_BY_ID)))
  const [selected, setSelected] = useState<string | null>(null)
  const rowEls = useRef(new Map<string, HTMLButtonElement>())
  const lastSelected = useRef<string | null>(null)

  const changeHidden = (next: Set<ColumnId>) => {
    setHidden(next)
    writeJSON(HIDDEN_KEY, [...next])
  }

  const shown = useMemo(() => sortRows(filterRows(rows, role, query), sort.col, sort.dir, now), [rows, role, query, sort, now])
  const scope = shown.length < rows.length ? shown : rows
  const metrics = cluster?.metrics === 'available'
  const metricsNote = cluster?.metricsMessage ?? ''
  const cards = useMemo(() => capacityCards(scope, scope === rows ? unscheduled : 0, metrics, metricsNote), [scope, rows, unscheduled, metrics, metricsNote])
  const selectedRow = selected ? rows.find((r) => r.name === selected) ?? null : null
  const compact = selectedRow !== null
  // Stable between renders, so rows only re-render when their own data changes.
  const cols = useMemo(() => visibleColumns(hidden, compact), [hidden, compact])
  const { template, minWidth } = useMemo(() => gridTemplate(cols, compact), [cols, compact])
  const cp = rows.filter((r) => r.role === 'control-plane').length

  const onSort = (col: ColumnId) =>
    setSort((s) => (s.col === col ? { col, dir: s.dir === 'asc' ? 'desc' : 'asc' } : { col, dir: COLUMN_BY_ID[col].defaultDir }))

  const close = useCallback(() => {
    const name = lastSelected.current
    setSelected(null)
    // Return focus to the row the panel was opened from.
    requestAnimationFrame(() => {
      if (name) rowEls.current.get(name)?.focus()
    })
  }, [])

  const onOpen = useCallback(
    (name: string) => {
      if (lastSelected.current === name && selected === name) {
        close()
        return
      }
      lastSelected.current = name
      setSelected(name)
    },
    [selected, close],
  )

  const rowRef = useCallback((name: string, el: HTMLButtonElement | null) => {
    if (el) rowEls.current.set(name, el)
    else rowEls.current.delete(name)
  }, [])

  // Escape closes the panel from anywhere on the page.
  useEffect(() => {
    if (!selected) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !e.defaultPrevented) close()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [selected, close])

  const exportXlsx = () => {
    const sheet = exportSheet(shown, cols)
    const name = (cluster?.name ?? 'cluster').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
    const file = `${name}-nodes-${fileStamp()}.xlsx`
    try {
      download(xlsxBytes('Nodes', sheet.head, sheet.rows), file)
      onToast(`Downloaded ${file}`)
    } catch {
      onToast('Couldn’t build the spreadsheet.')
    }
  }

  const segs: [RoleFilter, string, number][] = [
    ['all', 'All', rows.length],
    ['control-plane', 'Control plane', cp],
    ['worker', 'Workers', rows.length - cp],
  ]

  return (
    <div className="rk-split" style={{ flex: '1 1 auto', minHeight: 0, display: 'flex', alignItems: 'stretch' }}>
      <div className="rk-col" style={{ flex: '1 1 auto', minWidth: 0, display: 'flex', flexDirection: 'column' }}>
        <section aria-label="Cluster capacity" style={{ padding: '20px 24px 2px', display: 'flex', flexDirection: 'column', gap: 12 }}>
          <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'baseline', gap: '4px 12px' }}>
            <h1 style={{ margin: 0, fontSize: 18, fontWeight: 600, letterSpacing: '-0.01em' }}>Nodes</h1>
            <span style={{ fontSize: 12.5, color: 'var(--fg3)' }} data-testid="summary">
              {rows.length ? summaryLine(rows, shown) : cluster?.synced ? 'This cluster has no nodes.' : 'Waiting for the first list of nodes…'}
            </span>
          </div>
          {rows.length > 0 && (
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(210px, 100%), 1fr))', gap: 12 }}>
              {cards.map((c) => (
                <CapacityCard key={c.label} c={c} />
              ))}
            </div>
          )}
        </section>
        <section aria-label="Node list" style={{ padding: '16px 24px 28px', display: 'flex', flexDirection: 'column', gap: 10 }}>
          <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '10px 12px' }}>
            <div className="rk-seg" style={{ marginRight: 'auto' }} role="group" aria-label="Show nodes by role">
              {segs.map(([id, label, n]) => (
                <button key={id} aria-pressed={role === id} onClick={() => setRole(id)}>
                  {label} <span style={{ color: 'var(--fg3)', fontWeight: 400 }}>{n}</span>
                </button>
              ))}
            </div>
            <ColumnsMenu hidden={hidden} onChange={changeHidden} compact={compact} />
            <label className="rk-field" style={{ width: 260, maxWidth: '100%' }}>
              <SearchIcon />
              <span className="rk-sr">Filter nodes</span>
              <input type="search" placeholder="Filter by name, label or taint" value={query} onChange={(e) => setQuery(e.target.value)} />
            </label>
            <button className="rk-btn rk-xl" onClick={exportXlsx} title="Download the nodes in view as an Excel workbook" disabled={!shown.length}>
              <ExcelIcon />
              Excel
            </button>
          </div>
          <NodeTable
            rows={shown}
            cols={cols}
            template={template}
            minWidth={minWidth}
            sortCol={sort.col}
            sortDir={sort.dir}
            onSort={onSort}
            selected={selected}
            onOpen={onOpen}
            rowRef={rowRef}
            now={now}
          />
          <p style={{ margin: 0, fontSize: 11.5, color: 'var(--fg3)' }}>
            Bars: light = requested, solid = in use; the percentage is requested of allocatable. Click a node for its details.
          </p>
        </section>
      </div>
      {selectedRow && <NodePanel r={selectedRow} metrics={metrics} metricsNote={metricsNote} now={now} onClose={close} />}
    </div>
  )
}
