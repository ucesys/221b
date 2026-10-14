import { useCallback, useMemo, useState } from 'react'
import { useView } from '../api/live'
import type { NodeRow, PodRow, PodView } from '../api/types'
import { POD_COLUMNS, podLabel, podStatusGroup, podTerms, type PodStatusFilter } from '../columns/pods'
import { nWord } from '../lib/format'
import { facetMatch, facetsActive, type FacetSelection, type RoleFilter } from '../lib/nodes'
import { listParam, navigate, withParams } from '../lib/router'
import { useTable } from '../lib/useTable'
import { ColumnsMenu } from './ColumnsMenu'
import { DataTable } from './DataTable'
import { ExportMenu } from './ExportMenu'
import { FacetPicker } from './FacetPicker'
import { SearchIcon } from './Icons'
import { PodPanel } from './PodPanel'
import { SearchBox } from './SearchBox'
import type { TabProps } from './tabs'

/** The key of pods without a node in the node list. */
export const UNSCHEDULED = '(unscheduled)'

/** A selection in the URL: absent means everything, "-" means nothing. */
function readSelection(params: URLSearchParams, key: string): Set<string> | null {
  const list = listParam(params, key)
  if (!list.length) return null
  return new Set(list.filter((x) => x !== '-'))
}

function writeSelection(next: Set<string>, all: string[]): string[] | undefined {
  if (all.every((k) => next.has(k))) return undefined
  return next.size ? [...next] : ['-']
}

const ROLES: RoleFilter[] = ['all', 'control-plane', 'worker']
const STATUSES: [PodStatusFilter, string][] = [
  ['all', 'All'],
  ['running', 'Running'],
  ['pending', 'Pending'],
  ['failing', 'Failing'],
  ['completed', 'Completed'],
]

function nodeKey(p: PodRow): string {
  return p.node || UNSCHEDULED
}

/** Whether a node's name, labels or taints contain the words. */
function nodeMatches(n: NodeRow, q: string): boolean {
  if (!q) return true
  if (n.name.toLowerCase().includes(q)) return true
  if (Object.entries(n.labels).some(([k, v]) => `${k}=${v}`.toLowerCase().includes(q))) return true
  return n.taints.some((t) => `${t.key}=${t.value ?? ''}:${t.effect}`.toLowerCase().includes(q))
}

/**
 * The Pods tab (P2-03): a filter panel by node and namespace on the left,
 * the pods on the right. Pods are listed for the nodes shown in the panel
 * that are ticked, in the ticked namespaces. The filters live in the URL,
 * so a node's pod count links here (P2-09).
 */
export function PodsTab({ cluster, now, onToast, route }: TabProps) {
  const pods = useView<PodRow>('pods')
  const nodesView = useView<NodeRow>('nodes')
  const nodes = nodesView.rows
  const p = route.params
  const role: RoleFilter = ROLES.includes(p.get('role') as RoleFilter) ? (p.get('role') as RoleFilter) : 'all'
  const status: PodStatusFilter = STATUSES.some(([id]) => id === p.get('status')) ? (p.get('status') as PodStatusFilter) : 'all'
  const query = p.get('q') ?? ''
  const facets = useMemo<FacetSelection>(() => ({ labels: listParam(p, 'label'), taints: listParam(p, 'taint') }), [p])
  const selected = p.get('open')
  const [nodeQuery, setNodeQuery] = useState('')
  const set = useCallback((changes: Record<string, string | string[] | undefined>) => navigate(withParams(route, changes), true), [route])

  // Every pod with its usage; rebuilt when either changes.
  const rows = useMemo<PodView[]>(() => pods.rows.map((r) => ({ ...r, usage: pods.usage.get(r.key) ?? null })), [pods.rows, pods.usage])

  // The node list: role, search and label and taint selection narrow it;
  // pods come from the shown nodes that are ticked.
  const unscheduled = useMemo(() => rows.filter((r) => !r.node).length, [rows])
  const nq = nodeQuery.trim().toLowerCase()
  const fxOn = facetsActive(facets)
  const sortedNodes = useMemo(() => [...nodes].sort((a, b) => (a.role === b.role ? a.name.localeCompare(b.name, undefined, { numeric: true }) : a.role === 'control-plane' ? -1 : 1)), [nodes])
  // Someone Kubernetes lets list pods but not nodes (P3-02) gets no node
  // rows: the nodes are then known by the names the pods carry, and the
  // list offers those, without roles, labels or taints.
  const namesOnly = nodesView.loaded && nodes.length === 0 && rows.some((r) => !!r.node)
  const podNodeNames = useMemo(() => (namesOnly ? [...new Set(rows.filter((r) => !!r.node).map((r) => r.node))].sort((a, b) => a.localeCompare(b, undefined, { numeric: true })) : []), [namesOnly, rows])
  const listed = useMemo(() => {
    const keys = namesOnly ? podNodeNames.filter((name) => !nq || name.toLowerCase().includes(nq)) : sortedNodes.filter((n) => (role === 'all' || n.role === role) && nodeMatches(n, nq) && (!fxOn || facetMatch(n, facets))).map((n) => n.name)
    if (unscheduled && role === 'all' && !fxOn && (!nq || 'unscheduled pending'.includes(nq))) keys.push(UNSCHEDULED)
    return keys
  }, [sortedNodes, role, nq, fxOn, facets, unscheduled, namesOnly, podNodeNames])
  const allKeys = useMemo(() => [...(namesOnly ? podNodeNames : sortedNodes.map((n) => n.name)), ...(unscheduled ? [UNSCHEDULED] : [])], [sortedNodes, unscheduled, namesOnly, podNodeNames])
  const totalKeys = allKeys.length
  const nodeSel = useMemo(() => readSelection(p, 'node'), [p])
  const inScope = useMemo(() => new Set(listed.filter((k) => !nodeSel || nodeSel.has(k))), [listed, nodeSel])
  const namespaces = useMemo(() => [...new Set(rows.map((r) => r.namespace))].sort(), [rows])
  const nsSel = useMemo(() => readSelection(p, 'ns'), [p])
  const nsIn = (ns: string) => !nsSel || nsSel.has(ns)

  const { byNode, byNs, nsAlerts } = useMemo(() => {
    const byNode = new Map<string, number>()
    const byNs = new Map<string, number>()
    const nsAlerts = new Map<string, { n: number; err: boolean }>()
    for (const r of rows) {
      const k = nodeKey(r)
      if (nsIn(r.namespace)) byNode.set(k, (byNode.get(k) ?? 0) + 1)
      if (inScope.has(k)) byNs.set(r.namespace, (byNs.get(r.namespace) ?? 0) + 1)
      if (r.tone === 'err' || (r.tone === 'warn' && !r.done)) {
        const a = nsAlerts.get(r.namespace) ?? { n: 0, err: false }
        a.n++
        if (r.tone === 'err') a.err = true
        nsAlerts.set(r.namespace, a)
      }
    }
    return { byNode, byNs, nsAlerts }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rows, inScope, nsSel])

  const scoped = useMemo(() => rows.filter((r) => inScope.has(nodeKey(r)) && nsIn(r.namespace)), [rows, inScope, nsSel]) // eslint-disable-line react-hooks/exhaustive-deps
  const statusCounts = useMemo(() => {
    const c: Record<PodStatusFilter, number> = { all: scoped.length, running: 0, pending: 0, failing: 0, completed: 0 }
    for (const r of scoped) c[podStatusGroup(r)]++
    return c
  }, [scoped])
  const byStatus = useMemo(() => (status === 'all' ? scoped : scoped.filter((r) => podStatusGroup(r) === status)), [scoped, status])

  const selectedRow = selected ? (rows.find((r) => r.key === selected) ?? null) : null
  const table = useTable<PodView>({ id: 'pods', columns: POD_COLUMNS, rows: byStatus, terms: podTerms, now, compact: false, defaultSort: { col: 'name', dir: 'asc' }, query, onQuery: (q) => set({ q }) })

  const toggleNode = (k: string) => {
    const next = new Set(nodeSel ?? allKeys)
    if (next.has(k)) next.delete(k)
    else next.add(k)
    set({ node: writeSelection(next, allKeys) })
  }
  const allNodesOn = listed.length > 0 && listed.every((k) => inScope.has(k))
  const toggleAllNodes = () => {
    const next = new Set(nodeSel ?? allKeys)
    for (const k of listed) {
      if (allNodesOn) next.delete(k)
      else next.add(k)
    }
    set({ node: writeSelection(next, allKeys) })
  }
  const toggleNs = (ns: string) => {
    const next = new Set(nsSel ?? namespaces)
    if (next.has(ns)) next.delete(ns)
    else next.add(ns)
    set({ ns: writeSelection(next, namespaces) })
  }
  const allNsOn = !nsSel || namespaces.every((ns) => nsSel.has(ns))
  const close = useCallback(() => set({ open: undefined }), [set])
  const onOpen = useCallback((key: string) => set({ open: selected === key ? undefined : key }), [selected, set])

  const cp = nodes.filter((n) => n.role === 'control-plane').length
  const nS = inScope.size
  const scopeText =
    (nS === totalKeys ? 'all nodes' : nS === 1 ? (inScope.has(UNSCHEDULED) ? 'unscheduled' : [...inScope][0]) : nS === 0 ? 'no nodes' : `${nS} nodes`) +
    (fxOn && nS ? ' with the selected labels and taints' : '') +
    ' · ' +
    (!nsSel ? 'all namespaces' : nsSel.size === 1 ? [...nsSel][0] : `${nsSel.size} namespaces`)
  const waiting = !pods.loaded || !cluster?.synced

  return (
    <>
      <aside className="rk-pane" aria-label="Pod filters" style={{ flex: '1 1 320px', maxWidth: '100%', display: 'flex', flexDirection: 'column', background: 'var(--surface)', borderRight: '1px solid var(--line)' }}>
        <div style={{ padding: '14px 16px 10px', display: 'flex', flexDirection: 'column', gap: 10, borderBottom: '1px solid var(--line)' }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8, flexWrap: 'wrap' }}>
            <h2 className="rk-h">Filter by node</h2>
            {!namesOnly && (
              <div className="rk-seg" role="group" aria-label="Nodes by role">
                {(
                  [
                    ['all', 'All', nodes.length],
                    ['control-plane', 'Control plane', cp],
                    ['worker', 'Workers', nodes.length - cp],
                  ] as [RoleFilter, string, number][]
                ).map(([id, label, n]) => (
                  <button key={id} aria-pressed={role === id} onClick={() => set({ role: id === 'all' ? undefined : id })}>
                    {label} <span style={{ color: 'var(--fg3)', fontWeight: 400 }}>{n}</span>
                  </button>
                ))}
              </div>
            )}
          </div>
          {namesOnly && (
            <p style={{ margin: 0, fontSize: 12, color: 'var(--fg3)' }} data-testid="nodes-by-name">
              Kubernetes lets you list pods but not nodes, so the nodes are listed by the names your pods carry, without roles, labels or taints.
            </p>
          )}
          <label className="rk-field">
            <SearchIcon />
            <span className="rk-sr">Filter the node list</span>
            <input type="search" placeholder="Find a node, label or taint" value={nodeQuery} onChange={(e) => setNodeQuery(e.target.value)} data-testid="node-filter" />
          </label>
          {!namesOnly && <FacetPicker nodes={nodes} value={facets} onChange={(f) => set({ label: f.labels, taint: f.taints })} block />}
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8, fontSize: 12, minHeight: 22 }}>
            <label className="rk-hit" style={{ display: 'inline-flex', alignItems: 'center', gap: 8, cursor: 'pointer', color: 'var(--fg2)' }}>
              <input type="checkbox" checked={allNodesOn} ref={(el) => {
                  if (el) el.indeterminate = !allNodesOn && nS > 0
                }} onChange={toggleAllNodes} style={{ width: 15, height: 15, margin: 0, accentColor: 'var(--accent)', cursor: 'pointer' }} />
              <span>
                Select all <span style={{ color: 'var(--fg3)' }}>{listed.length === totalKeys ? `(${listed.length})` : `(${listed.length} of ${totalKeys})`}</span>
              </span>
            </label>
            <span style={{ color: 'var(--fg3)' }}>
              {nS} of {listed.length} selected
            </span>
          </div>
        </div>
        <div style={{ padding: '4px 0' }} data-testid="node-list">
          {listed.map((k) => {
            const n = nodes.find((x) => x.name === k)
            const on = inScope.has(k)
            const flag = n ? (n.cordoned ? 'cordoned' : '') : k === UNSCHEDULED ? 'pending' : ''
            const label = n ? n.name : k === UNSCHEDULED ? 'Unscheduled' : k
            return (
              <div key={k} className="rk-frow" data-sel={on}>
                <label className="rk-hit" style={{ display: 'flex', alignItems: 'center', gap: 10, flex: '1 1 auto', minWidth: 0, padding: '6px 6px 6px 16px', cursor: 'pointer' }}>
                  <input type="checkbox" checked={on} onChange={() => toggleNode(k)} style={{ width: 15, height: 15, margin: 0, flex: 'none', accentColor: 'var(--accent)', cursor: 'pointer' }} />
                  <span className="rk-mono" style={{ fontSize: 12.5, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', minWidth: 0 }}>
                    {label}
                  </span>
                  {n && (
                    <span className="rk-role" data-role={n.role}>
                      {n.role}
                    </span>
                  )}
                  {flag && (
                    <span className="rk-t" data-tone="warn" title={flag} style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontSize: 11, whiteSpace: 'nowrap' }}>
                      <span className="rk-dot" />
                      {flag}
                    </span>
                  )}
                </label>
                <button className="rk-only" onClick={() => set({ node: [k] })} title={`Show only ${k === UNSCHEDULED ? 'pods without a node' : label}`}>
                  only
                </button>
                <span className="rk-num" style={{ flex: 'none', minWidth: 30, paddingRight: 16, textAlign: 'right', fontSize: 12, color: 'var(--fg3)' }}>
                  {byNode.get(k) ?? 0}
                </span>
              </div>
            )
          })}
          {listed.length === 0 && <p style={{ margin: 0, padding: '14px 16px', fontSize: 12.5, color: 'var(--fg3)' }}>No nodes match.</p>}
        </div>
        <div style={{ padding: '14px 16px 10px', display: 'flex', flexDirection: 'column', gap: 10, borderTop: '1px solid var(--line)' }}>
          <h2 className="rk-h">Filter by namespace</h2>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8, fontSize: 12, minHeight: 22 }}>
            <label className="rk-hit" style={{ display: 'inline-flex', alignItems: 'center', gap: 8, cursor: 'pointer', color: 'var(--fg2)' }}>
              <input type="checkbox" checked={allNsOn} ref={(el) => {
                  if (el) el.indeterminate = !allNsOn && (nsSel?.size ?? 0) > 0
                }} onChange={() => set({ ns: allNsOn ? ['-'] : undefined })} style={{ width: 15, height: 15, margin: 0, accentColor: 'var(--accent)', cursor: 'pointer' }} />
              <span>
                Select all <span style={{ color: 'var(--fg3)' }}>({namespaces.length})</span>
              </span>
            </label>
            <span style={{ color: 'var(--fg3)' }}>
              {nsSel ? [...nsSel].filter((x) => namespaces.includes(x)).length : namespaces.length} of {namespaces.length} selected
            </span>
          </div>
        </div>
        <div style={{ padding: '0 0 4px' }} data-testid="namespace-list">
          {namespaces.map((ns) => {
            const on = nsIn(ns)
            const a = nsAlerts.get(ns)
            return (
              <div key={ns} className="rk-frow" data-sel={on}>
                <label className="rk-hit" style={{ display: 'flex', alignItems: 'center', gap: 10, flex: '1 1 auto', minWidth: 0, padding: '6px 6px 6px 16px', cursor: 'pointer' }}>
                  <input type="checkbox" checked={on} onChange={() => toggleNs(ns)} style={{ width: 15, height: 15, margin: 0, flex: 'none', accentColor: 'var(--accent)', cursor: 'pointer' }} />
                  <span className="rk-mono" style={{ fontSize: 12.5, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', minWidth: 0 }}>
                    {ns}
                  </span>
                  {a && (
                    <span className="rk-t" data-tone={a.err ? 'err' : 'warn'} title={`${a.n} pods not well`} style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontSize: 11, whiteSpace: 'nowrap' }}>
                      <span className="rk-dot" />
                      {nWord(a.n, 'alert', 'alerts')}
                    </span>
                  )}
                </label>
                <button className="rk-only" onClick={() => set({ ns: [ns] })} title={`Show only ${ns}`}>
                  only
                </button>
                <span className="rk-num" style={{ flex: 'none', minWidth: 30, paddingRight: 16, textAlign: 'right', fontSize: 12, color: 'var(--fg3)' }}>
                  {byNs.get(ns) ?? 0}
                </span>
              </div>
            )
          })}
        </div>
        <p style={{ margin: 'auto 0 0', padding: '10px 16px 14px', fontSize: 11.5, lineHeight: 1.5, color: 'var(--fg3)', borderTop: '1px solid var(--line)' }}>
          Pods are listed for the nodes shown above that are ticked, in the ticked namespaces. Counts already apply the other filter.
        </p>
      </aside>
      <main className="rk-pane" aria-label="Pods" style={{ flex: '999 1 560px', minWidth: 0, display: 'flex', flexDirection: 'column' }}>
        <section aria-label="Pod list" style={{ padding: '18px 24px 28px', display: 'flex', flexDirection: 'column', gap: 10 }}>
          <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '10px 12px' }}>
            <h2 className="rk-h" style={{ fontSize: 12 }}>
              Pods
            </h2>
            <span className="rk-num" style={{ fontSize: 12, color: 'var(--fg3)' }} data-testid="pods-count">
              {waiting ? 'Waiting for the first list of pods…' : nWord(scoped.length, 'pod', 'pods')}
            </span>
            {!waiting && <span style={{ fontSize: 12, color: 'var(--fg3)' }}>· {scopeText}</span>}
            <div className="rk-seg" role="group" aria-label="Pods by status" style={{ marginLeft: 'auto' }}>
              {STATUSES.map(([id, label]) => (
                <button key={id} aria-pressed={status === id} onClick={() => set({ status: id === 'all' ? undefined : id })} data-status={id}>
                  {label} <span style={{ color: 'var(--fg3)', fontWeight: 400 }}>{statusCounts[id]}</span>
                </button>
              ))}
            </div>
            <SearchBox rows={byStatus} terms={podTerms} value={table.query} onChange={table.setQuery} placeholder="Search by name, label, node or status" label="Search pods" width={220} testId="pod-search" />
            <div className="rk-tools" style={{ gap: 8 }}>
              <ColumnsMenu cols={POD_COLUMNS} hidden={table.hidden} defaults={table.defaults} onChange={table.setHidden} compact={false} />
              <ExportMenu onExport={(f) => table.exportRows(f, cluster?.name, 'pods', onToast)} disabled={!table.shown.length} what="the pods in view" />
            </div>
          </div>
          <DataTable<PodView> label="Pods" rows={table.shown} cols={table.cols} rowKey={(r) => r.key} rowLabel={podLabel} sort={table.sort} onSort={table.onSort} selected={selected} onOpen={onOpen} now={now} empty={waiting ? 'Waiting for the first list of pods…' : 'No pods match these filters.'} rowAttr="pod" />
          <p style={{ margin: 0, fontSize: 11.5, color: 'var(--fg3)' }}>
            Usage comes from metrics-server. Bars compare usage with the pod’s request: amber means above request, red means within 10% of its limit. Click a pod for its details and logs.
          </p>
        </section>
      </main>
      {selected && <PodPanel podKey={selected} row={selectedRow} gone={pods.loaded && !selectedRow} cluster={cluster} now={now} onClose={close} onToast={onToast} />}
    </>
  )
}
