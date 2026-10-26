import { useMemo, useRef, useState, type ReactNode } from 'react'
import { useView } from '../api/live'
import type { NamespaceRow, NodeRow } from '../api/types'
import type { StoragePair, StorageRow } from '../api/storage'
import { useStorageScope } from '../api/storage'
import { storageColumns } from '../columns/storage'
import { nWord } from '../lib/format'
import { parsePeek, peekLink } from '../lib/peek'
import { href, listParam, navigate, withParams } from '../lib/router'
import { csvText, exportFileName, exportTable, type ExportFormat } from '../lib/table'
import { useTable } from '../lib/useTable'
import {
  accessShort,
  ageShort,
  bytes,
  classEntries,
  classKeyOf,
  heading,
  labelFacets,
  labelMatch,
  mountText,
  namespaceEntries,
  nodeEntries,
  nodeKeysOf,
  nodeText,
  nsKeyOf,
  pairKey,
  pairLabels,
  pairTerms,
  podsOpenByDefault,
  scopeText,
  sidebarMatch,
  storageData,
  usageTone,
  type Section,
  type Selection,
  type Selections,
  type SideEntry,
} from '../lib/volumes'
import { download, xlsxBytes } from '../lib/xlsx'
import { ColumnsMenu } from './ColumnsMenu'
import { ExportMenu } from './ExportMenu'
import { Chevron, CloseIcon, FilterIcon } from './Icons'
import { SearchBox } from './SearchBox'
import type { TabProps } from './tabs'

/** A selection in the URL, as on the Pods tab: absent means everything, "-" means nothing. */
function readSelection(params: URLSearchParams, key: string): Selection {
  const list = listParam(params, key)
  if (!list.length) return null
  return new Set(list.filter((x) => x !== '-'))
}

function writeSelection(next: Set<string>, all: string[]): string[] | undefined {
  if (all.every((k) => next.has(k))) return undefined
  return next.size ? [...next] : ['-']
}

/** One section of the filter sidebar (ST-02), in the Pods tab's markup: Select all, the entries with only and a count. */
function FilterSection({ title, entries, sel, counts, onSet, note, testId }: { title: string; entries: SideEntry[]; sel: Selection; counts: Map<string, number>; onSet: (v: string[] | undefined) => void; note?: string; testId: string }) {
  const keys = entries.map((e) => e.key)
  const on = (k: string) => !sel || sel.has(k)
  const nOn = keys.filter(on).length
  const allOn = nOn === keys.length
  const toggle = (k: string) => {
    const next = new Set(sel ?? keys)
    if (next.has(k)) next.delete(k)
    else next.add(k)
    onSet(writeSelection(next, keys))
  }
  return (
    <>
      <div style={{ padding: '14px 16px 10px', display: 'flex', flexDirection: 'column', gap: 10, borderTop: '1px solid var(--line)' }}>
        <h2 className="rk-h">{title}</h2>
        {note && (
          <p style={{ margin: 0, fontSize: 12, color: 'var(--fg3)' }} data-testid={`${testId}-note`}>
            {note}
          </p>
        )}
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8, fontSize: 12, minHeight: 22 }}>
          <label className="rk-hit" style={{ display: 'inline-flex', alignItems: 'center', gap: 8, cursor: 'pointer', color: 'var(--fg2)' }}>
            <input type="checkbox" checked={allOn} ref={(el) => {
                if (el) el.indeterminate = !allOn && nOn > 0
              }} onChange={() => onSet(allOn ? ['-'] : undefined)} style={{ width: 15, height: 15, margin: 0, accentColor: 'var(--accent)', cursor: 'pointer' }} data-testid={`${testId}-all`} />
            <span>
              Select all <span style={{ color: 'var(--fg3)' }}>({keys.length})</span>
            </span>
          </label>
          <span style={{ color: 'var(--fg3)' }}>
            {nOn} of {keys.length} selected
          </span>
        </div>
      </div>
      <div style={{ padding: '0 0 4px' }} data-testid={testId}>
        {entries.map((e) => (
          <div key={e.key} className="rk-frow" data-sel={on(e.key)} data-key={e.key}>
            <label className="rk-hit" style={{ display: 'flex', alignItems: 'center', gap: 10, flex: '1 1 auto', minWidth: 0, padding: '6px 6px 6px 16px', cursor: 'pointer' }}>
              <input type="checkbox" checked={on(e.key)} onChange={() => toggle(e.key)} style={{ width: 15, height: 15, margin: 0, flex: 'none', accentColor: 'var(--accent)', cursor: 'pointer' }} />
              <span style={{ display: 'flex', flexDirection: 'column', minWidth: 0 }}>
                <span style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 0 }}>
                  <span className={e.mono ? 'rk-mono' : undefined} style={{ fontSize: e.mono ? 12.5 : 13, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', minWidth: 0, color: e.err ? 'var(--err)' : undefined }}>
                    {e.label}
                  </span>
                  {e.tag?.role && (
                    <span className="rk-role" data-role={e.tag.role}>
                      {e.tag.text}
                    </span>
                  )}
                  {e.tag && !e.tag.role && (
                    <span className="rk-tag" data-tone={e.tag.tone}>
                      {e.tag.text}
                    </span>
                  )}
                  {e.flag && (
                    <span className="rk-t" data-tone="warn" title={e.flag} style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontSize: 11, whiteSpace: 'nowrap' }}>
                      <span className="rk-dot" />
                      {e.flag}
                    </span>
                  )}
                </span>
                {e.sub && (
                  <span className="rk-st-side-sub" title={e.sub}>
                    {e.sub}
                  </span>
                )}
              </span>
            </label>
            <button className="rk-only" onClick={() => onSet([e.key])} title={`Show only ${e.mono ? e.label : e.label.toLowerCase()}`}>
              only
            </button>
            <span className="rk-num" style={{ flex: 'none', minWidth: 30, paddingRight: 16, textAlign: 'right', fontSize: 12, color: 'var(--fg3)' }}>
              {counts.get(e.key) ?? 0}
            </span>
          </div>
        ))}
        {entries.length === 0 && <p style={{ margin: 0, padding: '6px 16px 10px', fontSize: 12.5, color: 'var(--fg3)' }}>None.</p>}
      </div>
    </>
  )
}

function ChevronRight() {
  return (
    <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="m6 4 4 4-4 4" />
    </svg>
  )
}

/** The link that opens a pair's drawer over the tab (ST-13). */
function drawerLink(p: StoragePair) {
  return p.claim && p.namespace ? peekLink({ kind: 'claim', namespace: p.namespace, name: p.claim }) : peekLink({ kind: 'volume', name: p.volume ?? '' })
}

/** Select by label (ST-10): the claims' and volumes' labels, as the Nodes tab selects nodes (P2-09). */
function LabelPicker({ pairs, value, onChange }: { pairs: StoragePair[]; value: string[]; onChange: (v: string[]) => void }) {
  const [open, setOpen] = useState(false)
  const button = useRef<HTMLButtonElement>(null)
  const facets = useMemo(() => labelFacets(pairs), [pairs])
  const keys = [...facets.keys()].sort()
  const toggle = (l: string) => onChange(value.includes(l) ? value.filter((x) => x !== l) : [...value, l])
  const close = () => {
    setOpen(false)
    button.current?.focus()
  }
  return (
    <div
      className="rk-dd"
      style={{ position: 'relative', display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}
      onKeyDown={(e) => {
        if (e.key === 'Escape' && open) {
          e.stopPropagation()
          close()
        }
      }}
    >
      <button ref={button} className="rk-btn" aria-expanded={open} aria-haspopup="true" data-on={value.length > 0} onClick={() => setOpen(!open)} style={{ height: 32 }} title="Narrow the rows by the labels of their claims and volumes" data-testid="storage-labels">
        <FilterIcon />
        Select by label
        {value.length > 0 && <span className="rk-cnt">{value.length}</span>}
        <Chevron />
      </button>
      {value.map((l) => (
        <button key={l} className="rk-fchip" data-on="true" onClick={() => toggle(l)} aria-label={`Remove ${l}`} title={`Remove ${l}`}>
          {l}
          <CloseIcon />
        </button>
      ))}
      {open && (
        <>
          <button className="rk-ddcatch" tabIndex={-1} aria-hidden="true" onClick={() => setOpen(false)} />
          <div className="rk-ddmenu" role="group" aria-label="Select by label" style={{ minWidth: 'min(520px, calc(100vw - 32px))', maxHeight: 420, overflow: 'auto', padding: 12, gap: 10 }}>
            {keys.length === 0 && <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>No claim or volume has labels.</p>}
            {keys.map((k) => (
              <div key={k} style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
                <span className="rk-fkey">{k}</span>
                <div style={{ display: 'flex', flexWrap: 'wrap', gap: 5 }}>
                  {[...facets.get(k)!.entries()].map(([v, n]) => {
                    const l = `${k}=${v}`
                    return (
                      <button key={v} className="rk-fchip" aria-pressed={value.includes(l)} onClick={() => toggle(l)} data-label={l}>
                        {v || '(set)'}
                        <span className="rk-cnt">{n}</span>
                      </button>
                    )
                  })}
                </div>
              </div>
            ))}
          </div>
        </>
      )}
    </div>
  )
}

/**
 * The Storage tab (ST-01 to ST-12): classes, volumes, claims and the pods
 * that mount them, one row per volume and claim, filtered by namespace,
 * node and storage class in a sidebar as the Pods tab is (ST-02).
 */
export function StorageTab({ cluster, now, onToast, route }: TabProps) {
  const { rows, loaded } = useView<StorageRow>('storage')
  const nodesView = useView<NodeRow>('nodes')
  const namespacesView = useView<NamespaceRow>('namespaces')
  const scope = useStorageScope(route.cluster)
  const data = useMemo(() => storageData(rows), [rows])
  // A cluster-wide role reads classes and volumes; a namespace-bound one
  // gets their claims, with class and volume as names (ST-20).
  const canClasses = scope ? scope.classes : true
  const clusterWide = scope ? scope.classes && scope.volumes : true
  const p = route.params
  const attention = p.get('attention') === '1'
  const labels = listParam(p, 'label')
  const query = p.get('q') ?? ''
  const set = (changes: Record<string, string | string[] | undefined>) => navigate(withParams(route, changes), true)
  const peek = parsePeek(p.get('peek'))
  const selected = peek?.kind === 'claim' ? `pvc:${peek.namespace}/${peek.name}` : peek?.kind === 'volume' ? `pv:${peek.name}` : ''

  const classOf = useMemo(() => new Map(data.classes.map((c) => [c.name, c])), [data.classes])
  const cols = useMemo(() => storageColumns((x) => classOf.get(x.class)?.binding), [classOf])
  const labelled = useMemo(() => data.pairs.filter((x) => labelMatch(x, labels)), [data.pairs, labels])
  // Claim ascending by default (ST-04): a namespace's claims sit together.
  const table = useTable<StoragePair>({ id: 'storage', columns: cols, rows: labelled, terms: pairTerms, now, defaultSort: { col: 'claim', dir: 'asc' }, query, onQuery: (q) => set({ q: q || undefined }) })

  // The sidebar (ST-02): its entries come from every row the person may
  // see; its counts and the table from the rows search, labels and Needs
  // attention leave.
  const sels: Selections = useMemo(() => ({ ns: readSelection(p, 'ns'), node: readSelection(p, 'node'), cls: readSelection(p, 'class') }), [p])
  const nodeInfo = useMemo(() => nodesView.rows.map((n) => ({ name: n.name, role: n.role, cordoned: n.cordoned })), [nodesView.rows])
  const nsNames = useMemo(() => namespacesView.rows.map((n) => n.name), [namespacesView.rows])
  const nsList = useMemo(() => namespaceEntries(data.pairs, data.quotas, nsNames), [data.pairs, data.quotas, nsNames])
  const nodeList = useMemo(() => nodeEntries(data.pairs, nodeInfo), [data.pairs, nodeInfo])
  const classList = useMemo(() => classEntries(data.pairs, data.classes, canClasses), [data.pairs, data.classes, canClasses])
  const base = useMemo(() => (attention ? table.shown.filter((x) => x.attention) : table.shown), [attention, table.shown])
  const counts = useMemo(() => {
    const c: Record<Section, Map<string, number>> = { ns: new Map(), node: new Map(), cls: new Map() }
    const add = (m: Map<string, number>, k: string) => m.set(k, (m.get(k) ?? 0) + 1)
    for (const x of base) {
      if (sidebarMatch(x, sels, 'ns')) add(c.ns, nsKeyOf(x))
      if (sidebarMatch(x, sels, 'node')) for (const k of nodeKeysOf(x)) add(c.node, k)
      if (sidebarMatch(x, sels, 'cls')) add(c.cls, classKeyOf(x))
    }
    return c
  }, [base, sels])
  const inSidebar = useMemo(() => table.shown.filter((x) => sidebarMatch(x, sels)), [table.shown, sels])
  const needing = inSidebar.filter((x) => x.attention).length
  const shown = attention ? inSidebar.filter((x) => x.attention) : inSidebar
  const [podsOpen, setPodsOpen] = useState<Record<string, boolean>>({})
  const podOpen = (x: StoragePair) => podsOpen[pairKey(x)] ?? podsOpenByDefault(x)
  const namesOnlyNodes = nodesView.loaded && nodesView.rows.length === 0 && nodeList.some((e) => e.mono)

  // The rows in the order on screen, for the export (ST-06).
  const exportAs = (format: ExportFormat) => {
    const file = exportFileName(cluster?.name, 'storage', format)
    const t = exportTable(shown, table.cols, now)
    if (format === 'xlsx') download(xlsxBytes('Storage', t.head, t.rows), file)
    else if (format === 'csv') download(csvText(t.head, t.rows), file, 'text/csv;charset=utf-8')
    else download(JSON.stringify(t.json, null, 2) + '\n', file, 'application/json')
    onToast(`Downloaded ${file}`)
  }

  const head = heading(data, clusterWide)
  const usageOff = cluster?.uptime === 'unavailable'
  const visible = table.cols
  const ncol = visible.length
  const scopeLine = [scopeText(sels.ns, nsList, 'namespace', 'namespaces'), scopeText(sels.node, nodeList, 'node', 'nodes'), scopeText(sels.cls, classList, 'class', 'classes')].join(' · ')

  const cell = (id: string, x: StoragePair): ReactNode => {
    switch (id) {
      case 'volume': {
        if (x.state === 'pending' || !x.volume) return <div className="rk-st-name"><span className="rk-dim">{x.classMissing ? 'none' : 'none yet'}</span></div>
        // A role that may not read volumes sees the name from the claim, which opens nothing (ST-20).
        const name = clusterWide ? (
          <a className="rk-mono rk-st-row" {...drawerLink(x)} title={x.volume} data-testid="storage-volume">
            {x.volume}
          </a>
        ) : (
          <span className="rk-mono rk-st-plain" title={x.volume}>
            {x.volume}
          </span>
        )
        return (
          <>
            <div className="rk-st-name">{name}</div>
            {!x.class && x.source && <span className="rk-st-sub2">{x.source.text}</span>}
          </>
        )
      }
      case 'claim':
        if (x.claim)
          return (
            <a className="rk-mono rk-st-row" {...drawerLink(x)} data-testid="storage-claim" title={`${x.namespace}/${x.claim}`}>
              <span className="rk-dim">{x.namespace}/</span>
              {x.claim}
            </a>
          )
        if (x.wasClaim) return <span className="rk-mono rk-dim">was {x.wasClaim}</span>
        return <span className="rk-dim">—</span>
      case 'class': {
        if (!x.class) return <span className="rk-dim">none</span>
        if (x.classMissing)
          return (
            <>
              <span className="rk-mono" style={{ color: 'var(--err)' }}>
                {x.class}
              </span>
              <span className="rk-st-reason" data-tone="err">
                not found
              </span>
            </>
          )
        return canClasses && classOf.has(x.class) ? (
          <a className="rk-mono rk-st-row" {...peekLink({ kind: 'class', name: x.class })}>
            {x.class}
          </a>
        ) : (
          <span className="rk-mono rk-st-plain">{x.class}</span>
        )
      }
      case 'status':
        return (
          <>
            <span className="rk-pill" data-tone={x.tone}>
              <span className="rk-dot" />
              {x.phase}
            </span>
            {x.reason && (
              <span className="rk-st-reason" data-tone={x.reason.tone === 'muted' ? undefined : x.reason.tone}>
                {x.reason.text}
              </span>
            )}
          </>
        )
      case 'capacity':
        return x.state === 'pending' ? (
          <>
            {bytes(x.requested)}
            <span className="rk-st-sub2" style={{ fontFamily: 'var(--font)' }}>
              requested
            </span>
          </>
        ) : x.capacity ? (
          bytes(x.capacity)
        ) : (
          <span className="rk-dim">—</span>
        )
      case 'used': {
        const u = x.usage
        if (!u) return <span className="rk-dim">—</span>
        const tone = usageTone(u.pct)
        return (
          <span className="rk-st-used" data-tone={tone === 'ok' ? undefined : tone} title={`${bytes(u.used)} of ${bytes(u.capacity)} used${u.node ? `, reported by the kubelet on ${u.node}` : ''}`}>
            <span className="rk-bar">
              <span data-tone={tone === 'ok' ? undefined : tone} style={{ width: `${Math.min(100, u.pct).toFixed(1)}%` }} />
            </span>
            {Math.round(u.pct)}%
          </span>
        )
      }
      case 'access':
        return (
          <span className="rk-st-mode" title={(x.accessModes ?? []).join(', ')}>
            {(x.accessModes ?? []).map(accessShort).join(', ')}
          </span>
        )
      case 'reclaim':
        return x.state === 'pending' || !x.reclaim ? <span className="rk-dim">—</span> : x.reclaim
      case 'mounted': {
        const pods = x.pods ?? []
        if (!pods.length) return <span className="rk-dim">—</span>
        if (pods.length === 1) {
          const one = pods[0]!
          return (
            <>
              <a className="rk-mono rk-st-row" {...peekLink({ kind: 'pod', namespace: x.namespace ?? '', name: one.name })}>
                {one.name}
              </a>
              {one.status !== 'Running' && one.note && (
                <span className="rk-st-reason" data-tone="warn">
                  {one.note}
                </span>
              )}
            </>
          )
        }
        const open = podOpen(x)
        return (
          <span className="rk-st-mount">
            <button className="rk-st-chev" aria-expanded={open} aria-label={`${open ? 'Hide' : 'Show'} the ${pods.length} pods`} onClick={() => setPodsOpen({ ...podsOpen, [pairKey(x)]: !open })} data-testid="storage-pods-toggle">
              <ChevronRight />
            </button>
            {pods.length} pods
          </span>
        )
      }
      case 'node': {
        const t = nodeText(x)
        if (!t) return <span className="rk-dim">—</span>
        return t.endsWith(' nodes') ? t : <span className="rk-mono">{t}</span>
      }
      case 'age':
        return ageShort(x.created, now)
      case 'mode':
        return x.volumeMode ?? <span className="rk-dim">—</span>
      case 'source':
        return x.source ? <span className="rk-mono" title={x.source.text}>{x.source.text}</span> : <span className="rk-dim">—</span>
      case 'labels':
        return <span className="rk-mono" title={pairLabels(x).join('\n')}>{pairLabels(x).join(', ') || '—'}</span>
      case 'release':
        return x.release ? <a className="rk-mono rk-st-row" href={href('helm', [x.namespace ?? '', x.release])}>{x.release}</a> : <span className="rk-dim">—</span>
      case 'binding': {
        const b = classOf.get(x.class)?.binding
        return b ? (b === 'WaitForFirstConsumer' ? 'on first use' : 'immediate') : <span className="rk-dim">—</span>
      }
    }
    return null
  }

  const two = (x: StoragePair) => !!x.reason || x.state === 'pending' || (!x.class && !!x.source) || (x.pods ?? []).some((y) => y.status !== 'Running' && !!y.note && (x.pods ?? []).length === 1)
  const pairRows = (x: StoragePair) => {
    const k = pairKey(x)
    const pods = x.pods ?? []
    return [
      <tr key={k} className={'rk-st-vol' + (two(x) ? ' rk-st-two' : '')} data-sel={selected === k || undefined} data-row={k} data-attention={x.attention || undefined} data-testid="storage-row">
        {visible.map((c) => (
          <td key={c.id} className={c.right ? 'rk-r rk-num' : undefined}>
            {cell(c.id, x)}
          </td>
        ))}
      </tr>,
      ...(pods.length > 1 && podOpen(x)
        ? pods.map((y) => (
            <tr key={k + '/' + y.name} className="rk-st-pod" data-testid="storage-pod">
              <td>
                <span className="rk-st-elbow" />
                <a className="rk-mono rk-st-a" {...peekLink({ kind: 'pod', namespace: x.namespace ?? '', name: y.name })}>
                  {y.name}
                </a>
              </td>
              <td colSpan={Math.max(1, ncol - 4)}>
                {y.mountPath && <span className="rk-mono">{y.mountPath}</span>} <span className="rk-dim">· {mountText(y)}</span>
              </td>
              <td>
                <span className="rk-pill" data-tone={y.tone}>
                  <span className="rk-dot" />
                  {y.status}
                </span>
              </td>
              <td className="rk-mono">{y.node ?? <span className="rk-dim">—</span>}</td>
              <td className="rk-r rk-num">{y.since ? ageShort(y.since, now) : ''}</td>
            </tr>
          ))
        : []),
    ]
  }

  const empty = !loaded ? 'Waiting for the first list of volumes and claims…' : data.pairs.length === 0 ? (clusterWide ? 'No volumes or claims in this cluster.' : 'No claims in the namespaces your role can see.') : shown.length === 0 ? (attention ? 'No volume or claim needs attention.' : 'No volume or claim matches.') : ''
  const sortTh = (id: string) => {
    const c = visible.find((x) => x.id === id)!
    const act = table.sort.col === id
    return (
      <button onClick={() => table.onSort(id)} title={act ? `Sorted by ${c.sortName ?? c.label.toLowerCase()} — click to reverse` : `Sort by ${c.sortName ?? c.label.toLowerCase()}`}>
        {c.label}
        <span className="rk-arrow">{act ? (table.sort.dir === 'asc' ? ' ↑' : ' ↓') : ''}</span>
      </button>
    )
  }
  const colWidth = (id: string) => visible.find((c) => c.id === id)?.width

  return (
    <>
      <aside className="rk-pane" aria-label="Storage filters" style={{ flex: '1 1 320px', maxWidth: '100%', display: 'flex', flexDirection: 'column', background: 'var(--surface)', borderRight: '1px solid var(--line)' }} data-testid="storage-sidebar">
        <div style={{ marginTop: -1 }}>
          <FilterSection title="Filter by namespace" entries={nsList} sel={sels.ns} counts={counts.ns} onSet={(v) => set({ ns: v })} testId="storage-ns-list" />
          <FilterSection title="Filter by node" entries={nodeList} sel={sels.node} counts={counts.node} onSet={(v) => set({ node: v })} note={namesOnlyNodes ? 'Kubernetes lets you list claims but not nodes, so the nodes are listed by the names your pods carry.' : undefined} testId="storage-node-list" />
          <FilterSection title="Filter by storage class" entries={classList} sel={sels.cls} counts={counts.cls} onSet={(v) => set({ class: v })} note={canClasses ? undefined : 'Your role does not list storage classes, so the classes are the names your claims carry.'} testId="storage-class-list" />
        </div>
      </aside>
      <main className="rk-pane rk-st-page" aria-label="Storage" style={{ flex: '999 1 560px' }}>
        <div className="rk-st-head">
          <h1>Storage</h1>
          <span data-testid="storage-heading">
            {head.parts.map((x, i) =>
              x.mono ? (
                <span key={i} className="rk-mono">
                  {x.text}
                </span>
              ) : (
                <span key={i}>{x.text}</span>
              ),
            )}
            {usageOff && ' · Used shows — : the console does not read the kubelets’ stats (kubeletStats in the chart)'}
          </span>
        </div>

        <div className="rk-st-tb">
          <span className="rk-num" style={{ fontSize: 12, color: 'var(--fg3)' }} data-testid="storage-scope">
            {loaded ? `${nWord(shown.length, 'row', 'rows')} · ${scopeLine}` : 'Waiting for the first list…'}
          </span>
          <label className="rk-tcheck" title="Claims pending with a warning or naming a class that does not exist, lost claims, Released or Failed volumes, and volumes 85% full or more">
            <input type="checkbox" checked={attention} onChange={(e) => set({ attention: e.target.checked ? '1' : undefined })} data-testid="storage-attention" />
            Needs attention
            <span className="rk-cnt" data-tone={needing ? 'warn' : undefined} data-testid="storage-attention-count">
              {needing}
            </span>
          </label>
          <SearchBox rows={labelled} terms={pairTerms} value={table.query} onChange={table.setQuery} placeholder={clusterWide ? 'Search volumes, claims and pods' : 'Search claims and pods'} label={clusterWide ? 'Search volumes, claims and pods' : 'Search claims and pods'} width={260} testId="storage-search" />
          <LabelPicker pairs={data.pairs} value={labels} onChange={(v) => set({ label: v })} />
          <span className="rk-grow" />
          <ColumnsMenu cols={cols} hidden={table.hidden} defaults={table.defaults} onChange={table.setHidden} compact={false} />
          <ExportMenu onExport={exportAs} disabled={!shown.length} what="the rows in view" />
        </div>

        <div className="rk-st-wrap">
          <table className="rk-st-tree rk-st-flat" aria-label={clusterWide ? 'Volumes and claims' : 'Claims'} data-testid="storage-table" style={{ minWidth: visible.reduce((n, c) => n + (parseInt(c.width, 10) || 80), 0) }}>
            <colgroup>
              {visible.map((c) => (
                <col key={c.id} style={{ width: colWidth(c.id) }} />
              ))}
            </colgroup>
            <thead>
              <tr>
                {visible.map((c) => (
                  <th key={c.id} className={c.right ? 'rk-r' : undefined} aria-sort={table.sort.col === c.id ? (table.sort.dir === 'asc' ? 'ascending' : 'descending') : undefined}>
                    {sortTh(c.id)}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {shown.flatMap(pairRows)}
              {empty && (
                <tr>
                  <td colSpan={ncol} style={{ padding: '18px 16px', color: 'var(--fg3)', whiteSpace: 'normal' }} data-testid="storage-empty">
                    {empty}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>

        {!clusterWide && scope && (
          <div className="rk-callout" data-tone="info" data-testid="storage-scope-note">
            <svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <circle cx="8" cy="8" r="6.3" />
              <path d="M8 7.2v4M8 5h.01" />
            </svg>
            <span>
              Volumes and storage classes are cluster-scoped. Your role reads them by name only, from your claims: the volume and class details, and the Available, Released and Failed volumes no claim holds, need a SuperViewer or SuperAdmin role. <b>Used</b> comes from each kubelet’s Summary API, which the console reads with its own ServiceAccount (<span className="rk-mono">kubeletStats</span> in the chart, as for node uptime), for the claims you may see; a claim no running pod mounts shows —.
            </span>
          </div>
        )}
      </main>
    </>
  )
}
