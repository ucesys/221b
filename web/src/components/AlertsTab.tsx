import { useMemo, useState } from 'react'
import { useView } from '../api/live'
import type { AlertEntry, AlertRow } from '../api/types'
import { agoText, contextOf, countText, kindLabel, metaText, minutesOf, openLabel, searchText, sectionsOf, sevLabel, tilesOf, type AlertKind } from '../lib/alerts'
import { fileStamp, fmtTs } from '../lib/format'
import { href, navigate, withParams } from '../lib/router'
import { csvText, type ExportFormat } from '../lib/table'
import { download, xlsxBytes, type Cell } from '../lib/xlsx'
import { ExportMenu } from './ExportMenu'
import { ExcelIcon, SearchIcon } from './Icons'
import { ObjectPanel } from './ObjectPanel'
import { PodPanel } from './PodPanel'
import type { TabProps } from './tabs'

const KINDS: [AlertKind, string][] = [
  ['all', 'All'],
  ['pods', 'Pods'],
  ['nodes', 'Nodes'],
  ['volumes', 'Volumes'],
]

type SortCol = 'i' | 'reason' | 'msg' | 'count' | 'first' | 'last' | 'src'
interface Sort {
  col: SortCol
  dir: 'asc' | 'desc'
}

const SORT_LABEL: Record<SortCol, string> = {
  i: 'time order',
  reason: 'reason',
  msg: 'message',
  count: 'count (most first)',
  first: 'first seen (most recent first)',
  last: 'last seen (most recent first)',
  src: 'reporter',
}

const EVENT_HEAD = ['Section', 'Severity', 'Kind', 'Object', 'Where', 'Reason', 'Message', 'Count', 'First seen', 'Last seen', 'Reported by']

function sortEvents(events: AlertEntry[], s: Sort, now: number): AlertEntry[] {
  if (s.col === 'i') return s.dir === 'asc' ? events : [...events].reverse()
  const val = (e: AlertEntry): number | string => {
    switch (s.col) {
      case 'reason':
        return e.reason
      case 'msg':
        return e.message
      case 'count':
        return -e.count
      case 'first':
        return minutesOf(e.first, now)
      case 'last':
        return minutesOf(e.last, now)
      default:
        return e.reportedBy ?? ''
    }
  }
  const out = [...events].sort((a, b) => {
    const x = val(a)
    const y = val(b)
    const c = typeof x === 'number' && typeof y === 'number' ? x - y : String(x).localeCompare(String(y), undefined, { numeric: true })
    return s.dir === 'asc' ? c : -c
  })
  return out
}

function sectionName(r: AlertRow): string {
  if (r.kind === 'pod') return 'Pods'
  if (r.kind === 'pvc') return 'Persistent volumes and claims'
  return r.role === 'control-plane' ? 'Control-plane nodes' : 'Worker nodes'
}

function eventCells(r: AlertRow): Cell[][] {
  const sev = sevLabel(r).label
  const where = r.kind === 'node' ? metaText(r) : (r.namespace ?? '')
  return r.events.map((e) => [sectionName(r), sev, kindLabel(r), r.name, where, e.reason, e.message, e.count, e.first ? fmtTs(e.first) : '', e.last ? fmtTs(e.last) : '', e.reportedBy ?? ''])
}

function exportEvents(rows: AlertRow[], format: ExportFormat, cluster: string | undefined, what: string, onToast: (m: string) => void) {
  const cells = rows.flatMap(eventCells)
  const base = `${(cluster || 'cluster').replace(/[^A-Za-z0-9._-]+/g, '-')}-${what}-${fileStamp()}`
  if (format === 'xlsx') download(xlsxBytes('Alerts', EVENT_HEAD, cells), base + '.xlsx')
  else if (format === 'csv') download(csvText(EVENT_HEAD, cells), base + '.csv', 'text/csv')
  else
    download(
      JSON.stringify(
        cells.map((c) => Object.fromEntries(EVENT_HEAD.map((h, i) => [h, c[i]]))),
        null,
        2,
      ),
      base + '.json',
      'application/json',
    )
  onToast(`Downloaded ${base}.${format}`)
}

function AlertCard({ r, now, onOpen, onPod, onXl }: { r: AlertRow; now: number; onOpen: () => void; onPod: (key: string) => void; onXl: () => void }) {
  const [sort, setSort] = useState<Sort>({ col: 'i', dir: 'asc' })
  const sev = sevLabel(r)
  const ctx = contextOf(r, now)
  const events = sortEvents(r.events, sort, now)
  const th = (col: SortCol, label: string, right = false) => {
    const act = sort.col === col
    const tip = act ? `Sorted by ${SORT_LABEL[col]}, ${sort.dir === 'asc' ? 'ascending' : 'descending'} — click to reverse` : `Sort by ${SORT_LABEL[col]}`
    return (
      <button className={'rk-th' + (right ? ' rk-r' : '')} data-active={act} title={tip} onClick={() => setSort(act ? { col, dir: sort.dir === 'asc' ? 'desc' : 'asc' } : { col, dir: 'asc' })}>
        {label}
        <span className="rk-arrow">{act && col !== 'i' ? (sort.dir === 'asc' ? '↑' : '↓') : ''}</span>
      </button>
    )
  }
  return (
    <div className="rk-card" style={{ overflow: 'hidden' }} data-testid="alert" data-alert={r.key} data-severity={r.severity}>
      <div style={{ padding: '12px 16px', display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '8px 12px', borderBottom: '1px solid var(--line)' }}>
        <span className="rk-tag" data-tone={sev.tone} style={{ fontSize: 11, padding: '2px 8px' }}>
          {sev.label}
        </span>
        <span className="rk-tag" style={{ fontSize: 11, padding: '2px 7px' }}>
          {kindLabel(r)}
        </span>
        <span className="rk-mono" style={{ fontSize: 13, fontWeight: 600, overflowWrap: 'anywhere' }}>
          {r.name}
        </span>
        <span style={{ fontSize: 12, color: 'var(--fg3)' }}>{metaText(r)}</span>
        <span style={{ flex: '1 1 auto' }} />
        {r.status && (
          <span className="rk-pill" data-tone={r.statusTone}>
            <span className="rk-dot" />
            {r.status}
          </span>
        )}
        <button className="rk-btn" onClick={onOpen} data-testid="alert-open">
          {openLabel(r)}
        </button>
        <button className="rk-icon-btn" onClick={onXl} aria-label="Download these events as an Excel workbook" title="Download these events as an Excel workbook" style={{ width: 30, height: 30 }}>
          <ExcelIcon />
        </button>
      </div>
      {ctx.text && (
        <p className={ctx.mono ? 'rk-mono' : undefined} data-testid="alert-context" style={{ margin: 0, padding: '10px 16px', fontSize: 12, color: 'var(--fg2)', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', background: 'var(--surface2)', borderBottom: '1px solid var(--line)' }}>
          {ctx.text}
        </p>
      )}
      {events.length > 0 && (
        <div style={{ overflowX: 'auto' }}>
          <div className="rk-grid" role="table" aria-label={`Events about ${r.name}`} style={{ ['--cols' as string]: '150px minmax(300px, 3fr) 72px 110px 110px 180px', minWidth: 940 }}>
            <div className="rk-gh" role="row">
              {th('reason', 'Reason')}
              {th('msg', 'Message')}
              {th('count', 'Count', true)}
              {th('first', 'First seen')}
              {th('last', 'Last seen')}
              {th('src', 'Reported by')}
            </div>
            {events.map((e, i) => (
              <div key={i} className="rk-gr rk-top" role="row" data-testid="alert-event" data-reason={e.reason}>
                <span>
                  <span className="rk-t" data-tone={e.tone} style={{ fontWeight: 600 }}>
                    {e.reason}
                  </span>
                </span>
                <span className="rk-wrap" style={{ color: 'var(--fg2)' }}>
                  {e.message}
                  {e.pod && (
                    <span className="rk-mono" style={{ display: 'block', marginTop: 3, fontSize: 11.5, color: 'var(--fg3)' }}>
                      <button className="rk-link rk-mono" onClick={() => onPod(e.pod!)} style={{ fontSize: 11.5 }}>
                        {r.kind === 'pvc' ? `Reported for pod ${e.pod.split('/')[1]}` : `Pod ${e.pod.split('/')[1]}`}
                      </button>
                    </span>
                  )}
                </span>
                <span className="rk-r rk-num">{countText(e)}</span>
                <span className="rk-num" title={e.first ? fmtTs(e.first) : undefined}>
                  {agoText(e.first, now)}
                </span>
                <span className="rk-num" title={e.last ? fmtTs(e.last) : undefined}>
                  {agoText(e.last, now)}
                </span>
                <span className="rk-mono rk-wrap" style={{ fontSize: 11.5, color: 'var(--fg3)' }}>
                  {e.reportedBy ?? ''}
                </span>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

/** The Alerts tab (P4-07): pods failing to start, pending and recently recovered, node problems and volume warnings, each with its events. */
export function AlertsTab({ cluster, now, onToast, route }: TabProps) {
  const { rows, loaded } = useView<AlertRow>('alerts')
  const p = route.params
  const kind: AlertKind = KINDS.some(([k]) => k === p.get('kind')) ? (p.get('kind') as AlertKind) : 'all'
  const query = p.get('q') ?? ''
  const open = p.get('open') ?? ''
  const set = (changes: Record<string, string | undefined>) => navigate(withParams(route, changes), true)

  const tiles = useMemo(() => tilesOf(rows), [rows])
  const q = query.trim().toLowerCase()
  const shown = useMemo(() => (q ? rows.filter((r) => searchText(r).includes(q)) : rows), [rows, q])
  const sec = useMemo(() => sectionsOf(shown), [shown])
  const counts: Record<AlertKind, number> = {
    all: shown.length,
    pods: sec.pods.length,
    nodes: sec.controlPlane.length + sec.workers.length,
    volumes: sec.volumes.length,
  }
  const sections: { id: string; title: string; what: string; rows: AlertRow[]; show: boolean }[] = [
    { id: 'pods', title: 'Pods', what: 'pod', rows: sec.pods, show: kind === 'all' || kind === 'pods' },
    { id: 'control-plane', title: 'Control-plane nodes', what: 'control-plane', rows: sec.controlPlane, show: kind === 'all' || kind === 'nodes' },
    { id: 'workers', title: 'Worker nodes', what: 'worker', rows: sec.workers, show: kind === 'all' || kind === 'nodes' },
    { id: 'volumes', title: 'Persistent volumes and claims', what: 'volume', rows: sec.volumes, show: kind === 'all' || kind === 'volumes' },
  ]
  const visible = sections.filter((s) => s.show).flatMap((s) => s.rows)

  const openRow = (r: AlertRow) => {
    if (r.kind === 'node') navigate(href('nodes', [], { open: r.name }))
    else set({ open: `${r.kind}:${r.namespace}/${r.name}` })
  }
  const [openKind, openRest = ''] = open.split(':', 2) as [string, string?]
  const [openNs = '', openName = ''] = openRest.split('/', 2) as [string?, string?]
  const close = () => set({ open: undefined })

  return (
    <section aria-label="Alerts" style={{ padding: '20px 24px 32px', display: 'flex', flexDirection: 'column', gap: 14, flex: '1 1 auto', minWidth: 0 }}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        <h1 style={{ margin: 0, fontSize: 18, fontWeight: 600, letterSpacing: '-0.01em' }}>Alerts</h1>
        <p style={{ margin: 0, maxWidth: '100ch', fontSize: 12.5, color: 'var(--fg3)' }}>
          Warning events from the last hour, grouped by the pod, node or volume they concern, with what each node reports about itself: its conditions, and whether it is cordoned. Kubernetes keeps events for only one hour, so a pod that recovered stays here until its warnings expire.
        </p>
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(190px, 100%), 1fr))', gap: 10 }} data-testid="alert-tiles">
        {tiles.map((t) => (
          <div key={t.label + t.count} className="rk-card rk-tile" data-tone={t.tone} style={{ padding: '12px 14px', display: 'flex', flexDirection: 'column', gap: 4 }}>
            <span className="rk-num" style={{ fontSize: 22, fontWeight: 600, lineHeight: 1.1 }}>
              {t.count}
            </span>
            <span style={{ fontSize: 12, color: 'var(--fg2)' }}>{t.label}</span>
          </div>
        ))}
      </div>
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '10px 12px' }}>
        <div className="rk-seg" style={{ marginRight: 'auto' }} role="group" aria-label="Kind of object">
          {KINDS.map(([k, label]) => (
            <button key={k} aria-pressed={kind === k} onClick={() => set({ kind: k === 'all' ? undefined : k })} data-kind={k}>
              {label} <span style={{ color: 'var(--fg3)', fontWeight: 400 }}>{counts[k]}</span>
            </button>
          ))}
        </div>
        <label className="rk-field" style={{ width: 260 }}>
          <SearchIcon />
          <span className="rk-sr">Search alerts</span>
          <input type="search" placeholder="Search objects or messages" value={query} onChange={(e) => set({ q: e.target.value || undefined })} data-testid="alert-search" />
        </label>
        <ExportMenu onExport={(f) => exportEvents(visible, f, cluster?.name, 'alerts', onToast)} disabled={!visible.length} what="every event shown below" />
      </div>
      {!loaded && <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>Waiting for the first list of alerts…</p>}
      {loaded &&
        sections
          .filter((s) => s.show)
          .map((s) => (
            <div key={s.id} style={{ display: 'flex', flexDirection: 'column', gap: 14 }} data-testid={`alert-section-${s.id}`}>
              <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginTop: 6 }}>
                <h2 className="rk-h">{s.title}</h2>
                <span style={{ fontSize: 12, color: 'var(--fg3)' }}>{s.rows.length}</span>
              </div>
              {s.rows.map((r) => (
                <AlertCard key={r.key} r={r} now={now} onOpen={() => openRow(r)} onPod={(k) => set({ open: `pod:${k}` })} onXl={() => exportEvents([r], 'xlsx', cluster?.name, `${r.kind}-${r.name}-events`, onToast)} />
              ))}
              {s.rows.length === 0 && (
                <p style={{ margin: 0, padding: '18px 16px', fontSize: 12.5, color: 'var(--fg3)', border: '1px dashed var(--line2)', borderRadius: 10 }} data-testid="alert-empty">
                  {q ? `No ${s.what} alerts match this search.` : `No ${s.what} problems right now.`}
                </p>
              )}
            </div>
          ))}
      {openKind === 'pod' && openName && <PodPanel podKey={`${openNs}/${openName}`} row={null} gone={false} cluster={cluster} now={now} onClose={close} onToast={onToast} />}
      {openKind === 'pvc' && openName && <ObjectPanel kind="PersistentVolumeClaim" namespace={openNs} name={openName} row={null} gone={false} cluster={cluster} now={now} linkTo={(k, n) => href('namespaces', [openNs], { open: `${k}/${n}` })} onClose={close} onToast={onToast} />}
    </section>
  )
}
