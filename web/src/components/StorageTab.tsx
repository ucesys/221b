import { useMemo, useRef, useState, type ReactNode } from 'react'
import { useView } from '../api/live'
import type { StoragePair, StorageRow } from '../api/storage'
import { useStorageScope } from '../api/storage'
import { storageColumns, viewColumns } from '../columns/storage'
import { fmtAge, nWord } from '../lib/format'
import { parsePeek, peekLink } from '../lib/peek'
import { href, listParam, navigate, withParams } from '../lib/router'
import { readJSON, writeJSON } from '../lib/storage'
import { csvText, exportFileName, exportTable, type ExportFormat } from '../lib/table'
import { useTable } from '../lib/useTable'
import {
  accessShort,
  byClass,
  byNamespace,
  bytes,
  bytesTotal,
  classFacts,
  classTail,
  heading,
  labelFacets,
  labelMatch,
  mountText,
  nodeText,
  openByDefault,
  pairKey,
  pairLabels,
  pairTerms,
  podsOpenByDefault,
  STORAGE_VIEWS,
  storageData,
  usageTone,
  type StorageGroup,
  type StorageView,
} from '../lib/volumes'
import { download, xlsxBytes } from '../lib/xlsx'
import { ColumnsMenu } from './ColumnsMenu'
import { ExportMenu } from './ExportMenu'
import { Chevron, CloseIcon, FilterIcon } from './Icons'
import { SearchBox } from './SearchBox'
import type { TabProps } from './tabs'

/** What the tab keeps per cluster in this browser (ST-02): the view, and the groups opened or closed by hand. */
interface Kept {
  view?: StorageView
  groups?: Record<string, boolean>
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

/** The Storage tab (ST-01 to ST-12): classes, volumes, claims and the pods that mount them, as one tree. */
export function StorageTab({ cluster, now, onToast, route }: TabProps) {
  const { rows, loaded } = useView<StorageRow>('storage')
  const scope = useStorageScope(route.cluster)
  const data = useMemo(() => storageData(rows), [rows])
  // A cluster-wide role reads classes and volumes; a namespace-bound one
  // gets the same page by namespace, with names only (ST-11).
  const canClasses = scope ? scope.classes : true
  const clusterWide = scope ? scope.classes && scope.volumes : true
  const p = route.params
  const keptKey = `221b.storage.${route.cluster}`
  const [kept, setKept] = useState<Kept>(() => readJSON<Kept>(keptKey, {}))
  const keep = (k: Kept) => {
    setKept(k)
    writeJSON(keptKey, k)
  }
  const asked = p.get('view') as StorageView | null
  let view: StorageView = asked && STORAGE_VIEWS.includes(asked) ? asked : (kept.view ?? (clusterWide ? 'class' : 'namespace'))
  if (view === 'class' && !canClasses) view = 'namespace'
  const attention = p.get('attention') === '1'
  const labels = listParam(p, 'label')
  const query = p.get('q') ?? ''
  const set = (changes: Record<string, string | string[] | undefined>) => navigate(withParams(route, changes), true)
  const peek = parsePeek(p.get('peek'))
  const selected = peek?.kind === 'claim' ? `pvc:${peek.namespace}/${peek.name}` : peek?.kind === 'volume' ? `pv:${peek.name}` : ''

  const classOf = useMemo(() => new Map(data.classes.map((c) => [c.name, c])), [data.classes])
  const allCols = useMemo(() => storageColumns((x) => classOf.get(x.class)?.binding), [classOf])
  const cols = useMemo(() => viewColumns(allCols, view), [allCols, view])
  const labelled = useMemo(() => data.pairs.filter((x) => labelMatch(x, labels)), [data.pairs, labels])
  const table = useTable<StoragePair>({ id: `storage-${view}`, columns: cols, rows: labelled, terms: pairTerms, now, defaultSort: { col: view === 'namespace' ? 'claim' : 'volume', dir: 'asc' }, query, onQuery: (q) => set({ q: q || undefined }) })
  const needing = table.shown.filter((x) => x.attention).length
  const shown = attention ? table.shown.filter((x) => x.attention) : table.shown
  const narrowed = attention || !!query.trim() || labels.length > 0
  const groups: StorageGroup[] = useMemo(() => (view === 'class' ? byClass(data.classes, shown) : view === 'namespace' ? byNamespace(shown, data.quotas) : []), [view, data.classes, data.quotas, shown])
  const visibleGroups = narrowed ? groups.filter((g) => g.pairs.length > 0) : groups
  const [podsOpen, setPodsOpen] = useState<Record<string, boolean>>({})
  const groupOpen = (g: StorageGroup) => (narrowed ? true : (kept.groups?.[g.key] ?? openByDefault(g, groups.length)))
  const podOpen = (x: StoragePair) => podsOpen[pairKey(x)] ?? podsOpenByDefault(x)

  // The rows in the order on screen, for the export (ST-06).
  const ordered = view === 'all' ? shown : visibleGroups.flatMap((g) => (groupOpen(g) ? g.pairs : g.pairs))
  const exportAs = (format: ExportFormat) => {
    const file = exportFileName(cluster?.name, 'storage', format)
    const t = exportTable(ordered, table.cols, now)
    if (format === 'xlsx') download(xlsxBytes('Storage', t.head, t.rows), file)
    else if (format === 'csv') download(csvText(t.head, t.rows), file, 'text/csv;charset=utf-8')
    else download(JSON.stringify(t.json, null, 2) + '\n', file, 'application/json')
    onToast(`Downloaded ${file}`)
  }

  const head = heading(data, clusterWide)
  const usageOff = cluster?.uptime === 'unavailable'
  const visible = table.cols
  const ncol = visible.length

  const cell = (id: string, x: StoragePair): ReactNode => {
    switch (id) {
      case 'volume': {
        if (x.state === 'pending' || !x.volume) return <div className="rk-st-name"><span className="rk-dim">{x.classMissing ? 'none' : 'none yet'}</span></div>
        const name =
          view === 'namespace' ? (
            <span className="rk-mono rk-st-plain" title={x.volume}>
              {x.volume}
            </span>
          ) : (
            <a className="rk-mono rk-st-row" {...drawerLink(x)} title={x.volume} data-testid="storage-volume">
              {x.volume}
            </a>
          )
        return (
          <>
            <div className="rk-st-name">{name}</div>
            {!x.class && x.source && view !== 'namespace' && <span className="rk-st-sub2">{x.source.text}</span>}
          </>
        )
      }
      case 'claim':
        if (x.claim)
          return (
            <a className="rk-mono rk-st-row" {...drawerLink(x)} data-testid="storage-claim" title={`${x.namespace}/${x.claim}`}>
              {view !== 'namespace' && <span className="rk-dim">{x.namespace}/</span>}
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
        return canClasses && classOf.has(x.class) && view !== 'namespace' ? (
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
            {Math.round(u.pct)}% <span className="rk-dim">{bytes(u.used)}</span>
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
        return fmtAge(x.created, now)
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
                <a className="rk-mono" {...peekLink({ kind: 'pod', namespace: x.namespace ?? '', name: y.name })}>
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
              <td className="rk-r rk-num">{y.since ? fmtAge(y.since, now) : ''}</td>
            </tr>
          ))
        : []),
    ]
  }

  const groupHeader = (g: StorageGroup) => {
    const open = groupOpen(g)
    const t = g.totals
    const toggle = () => keep({ ...kept, groups: { ...kept.groups, [g.key]: !open } })
    let name: ReactNode = g.name
    let facts: string[] = []
    let tail: ReactNode = null
    switch (g.kind) {
      case 'class': {
        const c = g.cls!
        name = (
          <span className="rk-st-cls">
            <a {...peekLink({ kind: 'class', name: c.name })} data-testid="storage-class-link">
              {c.name}
            </a>
          </span>
        )
        facts = classFacts(c)
        tail = (
          <>
            <b>{t.volumes}</b> {t.volumes === 1 ? 'volume' : 'volumes'} · <b>{bytesTotal(t.provisioned)}</b> provisioned
            {t.used > 0 && (
              <>
                {' '}
                · <b>{bytesTotal(t.used)}</b> used <span className="rk-st-meter" title={`${Math.round(t.usedPct)}% of what reports usage`}><span style={{ width: `${Math.min(100, t.usedPct).toFixed(1)}%` }} /></span>
              </>
            )}
            {classTail(t).map((s) => (
              <span key={s}>
                {' '}
                · <b>{s.split(' ')[0]}</b> {s.split(' ').slice(1).join(' ')}
              </span>
            ))}
          </>
        )
        break
      }
      case 'none':
        name = (
          <span className="rk-st-cls" style={{ fontFamily: 'var(--font)' }}>
            No storage class
          </span>
        )
        facts = ['volumes made by hand', 'nothing provisions or deletes these', 'a claim binds to one by name or by size']
        tail = (
          <>
            <b>{t.volumes}</b> {t.volumes === 1 ? 'volume' : 'volumes'} · <b>{bytesTotal(t.provisioned)}</b> provisioned
            {t.used > 0 && (
              <>
                {' '}
                · <b>{bytesTotal(t.used)}</b> used <span className="rk-st-meter"><span style={{ width: `${Math.min(100, t.usedPct).toFixed(1)}%` }} /></span>
              </>
            )}
            {classTail(t).map((s) => (
              <span key={s}>
                {' '}
                · <b>{s.split(' ')[0]}</b> {s.split(' ').slice(1).join(' ')}
              </span>
            ))}
          </>
        )
        break
      case 'missing':
        name = (
          <>
            <span className="rk-st-cls" data-tone="err">
              {g.name}
            </span>
            <span className="rk-tag" data-tone="err">
              class not found
            </span>
          </>
        )
        facts = ['no StorageClass with this name exists', 'the claim stays Pending; its only sign is a ProvisioningFailed event']
        tail = (
          <>
            <b>{t.claims}</b> {t.claims === 1 ? 'claim' : 'claims'} · <b>{bytesTotal(t.requested)}</b> requested
          </>
        )
        break
      case 'namespace': {
        name = (
          <span className="rk-st-cls">
            <a href={href('namespaces', [g.name])}>{g.name}</a>
          </span>
        )
        const rel = g.releases ?? []
        facts = [nWord(t.claims, 'claim', 'claims'), `${bytesTotal(t.requested)} requested`, ...(rel.length ? [`Helm release${rel.length === 1 ? '' : 's'} ${rel.join(', ')}`] : [])]
        const q = g.quota
        if (q?.hard) {
          const pc = (100 * (q.used ?? 0)) / q.hard
          tail = (
            <>
              quota <span className="rk-mono">requests.storage</span> <b>{bytesTotal(q.used ?? 0)}</b> of <b>{bytesTotal(q.hard)}</b>{' '}
              <span className="rk-st-meter">
                <span data-tone={usageTone(pc) === 'ok' ? undefined : usageTone(pc)} style={{ width: `${Math.min(100, pc).toFixed(1)}%` }} />
              </span>{' '}
              {Math.round(pc)}%
            </>
          )
        } else tail = <>no storage quota</>
        break
      }
      case 'unclaimed':
        name = <span className="rk-st-cls" style={{ fontFamily: 'var(--font)' }}>Volumes no claim holds</span>
        facts = ['Released and Available volumes', 'cluster-scoped']
        tail = (
          <>
            <b>{t.volumes}</b> {t.volumes === 1 ? 'volume' : 'volumes'} · <b>{bytesTotal(t.provisioned)}</b>
          </>
        )
        break
    }
    return (
      <tr key={'g:' + g.key} className="rk-st-grp" data-group={g.key}>
        <td colSpan={ncol}>
          <div className="rk-st-gh">
            <button className="rk-st-chev" aria-expanded={open} aria-label={`${open ? 'Collapse' : 'Expand'} ${g.kind === 'none' ? 'the volumes without a class' : g.name}`} onClick={toggle} data-testid="storage-group-toggle">
              <ChevronRight />
            </button>
            {name}
            {g.cls?.default && (
              <span className="rk-tag" data-tone="accent">
                default
              </span>
            )}
            <span className="rk-st-facts">
              {facts.map((f, i) => (
                <span key={f}>
                  {i > 0 && <i>·</i>}
                  {f}
                </span>
              ))}
            </span>
            <span className="rk-st-tot">{tail}</span>
          </div>
        </td>
      </tr>
    )
  }

  const empty = !loaded ? 'Waiting for the first list of volumes and claims…' : data.pairs.length === 0 ? (clusterWide ? 'No volumes or claims in this cluster.' : 'No claims in the namespaces your role can see.') : shown.length === 0 ? (attention ? 'No volume or claim needs attention.' : 'No volume or claim matches.') : ''
  const sortTh = (id: string) => {
    const c = visible.find((x) => x.id === id)!
    if (view !== 'all') return c.label
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
    <section className="rk-pane rk-st-page" aria-label="Storage">
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
        <div className="rk-seg" role="group" aria-label="Group by">
          <button aria-pressed={view === 'class'} disabled={!canClasses} title={canClasses ? 'Grouped by storage class' : 'Needs a SuperViewer or SuperAdmin role'} onClick={() => (keep({ ...kept, view: 'class' }), set({ view: 'class' }))} data-view="class">
            By class
          </button>
          <button aria-pressed={view === 'namespace'} onClick={() => (keep({ ...kept, view: 'namespace' }), set({ view: 'namespace' }))} data-view="namespace">
            By namespace
          </button>
          <button aria-pressed={view === 'all'} onClick={() => (keep({ ...kept, view: 'all' }), set({ view: 'all' }))} data-view="all">
            {clusterWide ? 'All volumes' : 'All claims'}
          </button>
        </div>
        <label className="rk-tcheck" title="Claims pending with a warning or naming a class that does not exist, lost claims, Released or Failed volumes, and volumes 85% full or more">
          <input type="checkbox" checked={attention} onChange={(e) => set({ attention: e.target.checked ? '1' : undefined })} data-testid="storage-attention" />
          Needs attention
          <span className="rk-cnt" data-tone={needing ? 'warn' : undefined} data-testid="storage-attention-count">
            {needing}
          </span>
        </label>
        <SearchBox rows={labelled} terms={pairTerms} value={table.query} onChange={table.setQuery} placeholder={clusterWide ? 'Search volumes, claims and pods' : 'Search claims and pods'} label={clusterWide ? 'Search volumes, claims and pods' : 'Search claims and pods'} width={300} testId="storage-search" />
        <LabelPicker pairs={data.pairs} value={labels} onChange={(v) => set({ label: v })} />
        <span className="rk-grow" />
        <ColumnsMenu cols={cols} hidden={table.hidden} defaults={table.defaults} onChange={table.setHidden} compact={false} />
        <ExportMenu onExport={exportAs} disabled={!ordered.length} what="the rows in view" />
      </div>

      <div className="rk-st-wrap">
        <table className={'rk-st-tree' + (view === 'all' ? ' rk-st-flat' : '')} aria-label={view === 'class' ? 'Volumes and claims by storage class' : view === 'namespace' ? 'Claims by namespace' : clusterWide ? 'All volumes' : 'All claims'} data-testid="storage-table" data-view={view}>
          <colgroup>
            {visible.map((c) => (
              <col key={c.id} style={{ width: colWidth(c.id) }} />
            ))}
          </colgroup>
          <thead>
            <tr>
              {visible.map((c) => (
                <th key={c.id} className={c.right ? 'rk-r' : undefined} aria-sort={view === 'all' && table.sort.col === c.id ? (table.sort.dir === 'asc' ? 'ascending' : 'descending') : undefined}>
                  {sortTh(c.id)}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {view === 'all'
              ? shown.flatMap(pairRows)
              : visibleGroups.flatMap((g) => [groupHeader(g), ...(groupOpen(g) ? g.pairs.flatMap(pairRows) : [])])}
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
            Volumes and storage classes are cluster-scoped. Your role reads them by name only: the By class view, the volume and class details, and the Available and Released volumes need a SuperViewer or SuperAdmin role. <b>Used</b> comes from each kubelet’s Summary API, which the console reads with its own ServiceAccount (<span className="rk-mono">kubeletStats</span> in the chart, as for node uptime), for the claims you may see; a claim no running pod mounts shows —.
          </span>
        </div>
      )}
    </section>
  )
}
