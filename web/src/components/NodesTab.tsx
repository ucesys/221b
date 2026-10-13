import { useCallback, useEffect, useMemo } from 'react'
import { useView } from '../api/live'
import type { NodeRow } from '../api/types'
import { NODE_COLUMNS, nodeLabel, nodeTerms } from '../columns/nodes'
import { facetMatch, facetsActive, type FacetSelection, type RoleFilter } from '../lib/nodes'
import { listParam, navigate, withParams } from '../lib/router'
import { useTable } from '../lib/useTable'
import { ColumnsMenu } from './ColumnsMenu'
import { DataTable } from './DataTable'
import { ExportMenu } from './ExportMenu'
import { FacetPicker } from './FacetPicker'
import { NodePanel } from './NodePanel'
import { SearchBox } from './SearchBox'
import type { TabProps } from './tabs'

const ROLES: RoleFilter[] = ['all', 'control-plane', 'worker']

/**
 * The Nodes tab. Its filters live in the URL (#/nodes?role=&q=&label=&taint=
 * &open=), so links from other tabs and the browser's history open the
 * right view.
 */
export function NodesTab({ cluster, now, onToast, route }: TabProps) {
  const { rows, loaded } = useView<NodeRow>('nodes')
  const p = route.params
  const role: RoleFilter = ROLES.includes(p.get('role') as RoleFilter) ? (p.get('role') as RoleFilter) : 'all'
  const query = p.get('q') ?? ''
  const facets = useMemo<FacetSelection>(() => ({ labels: listParam(p, 'label'), taints: listParam(p, 'taint') }), [p])
  const selected = p.get('open')
  const set = useCallback((changes: Record<string, string | string[] | undefined>) => navigate(withParams(route, changes), true), [route])

  const inScope = useMemo(() => rows.filter((r) => (role === 'all' || r.role === role) && (!facetsActive(facets) || facetMatch(r, facets))), [rows, role, facets])
  const selectedRow = selected ? (rows.find((r) => r.name === selected) ?? null) : null
  const compact = selectedRow !== null
  const table = useTable<NodeRow>({ id: 'nodes', columns: NODE_COLUMNS, rows: inScope, terms: nodeTerms, now, compact, defaultSort: { col: 'name', dir: 'asc' }, query, onQuery: (q) => set({ q }) })
  const metrics = cluster?.metrics === 'available'
  const cp = rows.filter((r) => r.role === 'control-plane').length

  const close = useCallback(() => {
    const name = selected
    set({ open: undefined })
    // Return focus to the row the panel was opened from.
    requestAnimationFrame(() => {
      if (name) document.querySelector<HTMLElement>(`[data-node="${CSS.escape(name)}"]`)?.focus()
    })
  }, [selected, set])

  const onOpen = useCallback((name: string) => set({ open: selected === name ? undefined : name }), [selected, set])

  // Escape closes the panel from anywhere on the page.
  useEffect(() => {
    if (!selected) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !e.defaultPrevented) close()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [selected, close])

  const segs: [RoleFilter, string, number][] = [
    ['all', 'All', rows.length],
    ['control-plane', 'Control plane', cp],
    ['worker', 'Workers', rows.length - cp],
  ]
  const shownText = !rows.length
    ? loaded && cluster?.synced
      ? 'This cluster has no nodes.'
      : 'Waiting for the first list of nodes…'
    : table.shown.length < rows.length
      ? `Showing ${table.shown.length} of ${rows.length} nodes`
      : ''

  return (
    <main className="rk-pane" aria-label="Nodes" style={{ flex: '999 1 560px', minWidth: 0, display: 'flex', flexDirection: 'column' }}>
      <div className="rk-split" style={{ flex: '1 1 auto', minHeight: 0, display: 'flex', alignItems: 'stretch' }}>
        <div className="rk-col" style={{ flex: '1 1 auto', minWidth: 0, display: 'flex', flexDirection: 'column' }}>
          <section aria-label="Node list" style={{ padding: '18px 24px 28px', display: 'flex', flexDirection: 'column', gap: 10 }}>
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '10px 12px' }}>
              <div className="rk-seg" role="group" aria-label="Show nodes by role">
                {segs.map(([id, label, n]) => (
                  <button key={id} aria-pressed={role === id} onClick={() => set({ role: id === 'all' ? undefined : id })}>
                    {label} <span style={{ color: 'var(--fg3)', fontWeight: 400 }}>{n}</span>
                  </button>
                ))}
              </div>
              <SearchBox rows={inScope} terms={nodeTerms} value={table.query} onChange={table.setQuery} placeholder="Search by name, label or taint" label="Search nodes" testId="node-search" />
              <FacetPicker nodes={rows} value={facets} onChange={(f) => set({ label: f.labels, taint: f.taints })} />
              <span style={{ fontSize: 12, color: 'var(--fg3)', whiteSpace: 'nowrap' }} data-testid="shown">
                {shownText}
              </span>
              <div className="rk-tools" style={{ marginLeft: 'auto', gap: 8 }}>
                <ColumnsMenu cols={NODE_COLUMNS} hidden={table.hidden} defaults={table.defaults} onChange={table.setHidden} compact={compact} />
                <ExportMenu onExport={(f) => table.exportRows(f, cluster?.name, 'nodes', onToast)} disabled={!table.shown.length} what="the nodes in view" />
              </div>
            </div>
            <DataTable<NodeRow> label="Nodes" rows={table.shown} cols={table.cols} rowKey={(r) => r.name} rowLabel={nodeLabel} sort={table.sort} onSort={table.onSort} selected={selected} onOpen={onOpen} compact={compact} now={now} empty="No nodes match this filter." rowAttr="node" />
            <p style={{ margin: 0, fontSize: 11.5, color: 'var(--fg3)' }}>
              Bars: light = requested, solid = in use; the percentage is requested of allocatable. Free = allocatable − requested. Click a node for its details, or its pod count for its pods.
            </p>
          </section>
        </div>
        {selectedRow && <NodePanel r={selectedRow} metrics={metrics} metricsNote={cluster?.metricsMessage ?? ''} uptimeNote={cluster?.uptimeMessage ?? ''} now={now} onClose={close} onToast={onToast} />}
      </div>
    </main>
  )
}
