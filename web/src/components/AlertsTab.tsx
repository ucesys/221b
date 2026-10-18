import { useMemo, useState } from 'react'
import { useView } from '../api/live'
import type { AlertEntry, AlertRow } from '../api/types'
import { agoText, contextOf, countText, isActive, kindLabel, metaText, minutesOf, openLabel, searchText, sectionsOf, sevLabel, tilesOf, type AlertKind } from '../lib/alerts'
import { fileStamp, fmtTs } from '../lib/format'
import { navigate, withParams } from '../lib/router'
import { peekHref, type Peek } from '../lib/peek'
import { acknowledge } from '../api/acks'
import { csvText, type ExportFormat } from '../lib/table'
import { download, xlsxBytes, type Cell } from '../lib/xlsx'
import { ExportMenu } from './ExportMenu'
import { SearchIcon } from './Icons'
import type { TabProps } from './tabs'
import { AskSherlock } from './Sherlock'
import { troubleshootQuestion } from '../lib/sherlock'

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
  const sev = sevLabel(r).label + (r.ack ? ' (acknowledged)' : '')
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

function AlertCard({ r, now, onOpen, onPod, onExport, onAck }: { r: AlertRow; now: number; onOpen: () => void; onPod: (key: string) => void; onExport: (format: ExportFormat) => void; onAck: (acknowledge: boolean) => Promise<void> }) {
  const [sort, setSort] = useState<Sort>({ col: 'i', dir: 'asc' })
  const [busy, setBusy] = useState(false)
  const ack = async (on: boolean) => {
    setBusy(true)
    try {
      await onAck(on)
    } finally {
      setBusy(false)
    }
  }
  // An acknowledged alert is yellow (asked at the Phase 4 review); its
  // object's state keeps its own colour, so Ready and Unknown still differ.
  const sev = r.ack ? { ...sevLabel(r), tone: 'warn' } : sevLabel(r)
  const ctx = contextOf(r, now)
  const events = sortEvents(r.events, sort, now)
  const th = (col: SortCol, label: string, right = false) => {
    const act = sort.col === col
    const tip = act ? `Sorted by ${SORT_LABEL[col]}, ${sort.dir === 'asc' ? 'ascending' : 'descending'} — click to reverse` : `Sort by ${SORT_LABEL[col]}`
    return (
      <span className="rk-contents" role="columnheader" aria-sort={act ? (sort.dir === 'asc' ? 'ascending' : 'descending') : undefined}>
        <button className={'rk-th' + (right ? ' rk-r' : '')} data-active={act} title={tip} onClick={() => setSort(act ? { col, dir: sort.dir === 'asc' ? 'desc' : 'asc' } : { col, dir: 'asc' })}>
          {label}
          <span className="rk-arrow">{act && col !== 'i' ? (sort.dir === 'asc' ? '↑' : '↓') : ''}</span>
        </button>
      </span>
    )
  }
  return (
    <div className="rk-card" style={{ overflow: 'hidden' }} data-testid="alert" data-alert={r.key} data-severity={r.severity} data-acked={r.ack ? 'true' : undefined}>
      <div style={{ padding: '12px 16px', display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '8px 12px', borderBottom: '1px solid var(--line)' }}>
        <span className="rk-tag" data-tone={sev.tone} style={{ fontSize: 11, padding: '2px 8px' }} data-testid="alert-sev">
          {sev.label}
        </span>
        <span className="rk-tag" style={{ fontSize: 11, padding: '2px 7px' }}>
          {kindLabel(r)}
        </span>
        <span className="rk-mono" style={{ fontSize: 13, fontWeight: 600, overflowWrap: 'anywhere' }}>
          {r.name}
        </span>
        <span style={{ fontSize: 12, color: 'var(--fg3)' }}>{metaText(r)}</span>
        {r.ack && (
          <span className="rk-tag" data-tone="warn" style={{ fontSize: 11, padding: '2px 8px' }} title={`Acknowledged by ${r.ack.by}, ${fmtTs(r.ack.at)}. It stays off the banners until it changes or clears; the tab still counts it.`} data-testid="alert-acked">
            Acknowledged by {r.ack.by} · {agoText(r.ack.at, now)}
          </span>
        )}
        <span style={{ flex: '1 1 auto' }} />
        {r.status && (
          <span className="rk-pill" data-tone={r.statusTone}>
            <span className="rk-dot" />
            {r.status}
          </span>
        )}
        {isActive(r) && (
          <button
            className="rk-btn"
            disabled={busy}
            onClick={() => void ack(!r.ack)}
            title={r.ack ? 'Put it back on the banners, for everyone' : 'Take it off the banners at the top of every tab, for everyone, until it changes or clears. It stays here and in the tab’s count.'}
            data-testid="alert-ack"
          >
            {r.ack ? 'Undo acknowledge' : 'Acknowledge'}
          </button>
        )}
        {isActive(r) && <AskSherlock question={troubleshootQuestion(r)} label="Troubleshoot" testId="alert-troubleshoot" />}
        <button className="rk-btn" onClick={onOpen} data-testid="alert-open">
          {openLabel(r)}
        </button>
        <ExportMenu onExport={onExport} disabled={!r.events.length} what="these events" />
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
                <span role="cell">
                  <span className="rk-t" data-tone={e.tone} style={{ fontWeight: 600 }}>
                    {e.reason}
                  </span>
                </span>
                <span className="rk-wrap" role="cell" style={{ color: 'var(--fg2)' }}>
                  {e.message}
                  {e.pod && (
                    <span className="rk-mono" style={{ display: 'block', marginTop: 3, fontSize: 11.5, color: 'var(--fg3)' }}>
                      <button className="rk-link rk-mono" onClick={() => onPod(e.pod!)} style={{ fontSize: 11.5 }}>
                        {r.kind === 'pvc' ? `Reported for pod ${e.pod.split('/')[1]}` : `Pod ${e.pod.split('/')[1]}`}
                      </button>
                    </span>
                  )}
                </span>
                <span className="rk-r rk-num" role="cell">
                  {countText(e)}
                </span>
                <span className="rk-num" role="cell" title={e.first ? fmtTs(e.first) : undefined}>
                  {agoText(e.first, now)}
                </span>
                <span className="rk-num" role="cell" title={e.last ? fmtTs(e.last) : undefined}>
                  {agoText(e.last, now)}
                </span>
                <span className="rk-mono rk-wrap" role="cell" style={{ fontSize: 11.5, color: 'var(--fg3)' }}>
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

/** The section an alert is listed in. */
function sectionOf(r: AlertRow): string {
  return r.kind === 'pod' ? 'pods' : r.kind === 'pvc' ? 'volumes' : r.role === 'control-plane' ? 'control-plane' : 'workers'
}

/** The Alerts tab (P4-07): pods failing to start, pending and recently recovered, node problems and volume warnings, each with its events. */
export function AlertsTab({ cluster, now, onToast, route }: TabProps) {
  const { rows, loaded } = useView<AlertRow>('alerts')
  const p = route.params
  const kind: AlertKind = KINDS.some(([k]) => k === p.get('kind')) ? (p.get('kind') as AlertKind) : 'all'
  const query = p.get('q') ?? ''
  // Resolved alerts (pods that recovered) are hidden unless asked for;
  // acknowledged ones show unless hidden.
  const activeOnly = p.get('show') !== 'all'
  const hideAcked = p.get('acked') === 'hide'
  const set = (changes: Record<string, string | undefined>) => navigate(withParams(route, changes), true)

  const tiles = useMemo(() => tilesOf(rows), [rows])
  const q = query.trim().toLowerCase()
  const shown = useMemo(() => rows.filter((r) => (!activeOnly || isActive(r)) && (!hideAcked || !r.ack) && (!q || searchText(r).includes(q))), [rows, q, activeOnly, hideAcked])
  const sec = useMemo(() => sectionsOf(shown), [shown])
  const counts: Record<AlertKind, number> = {
    all: shown.length,
    pods: sec.pods.length,
    nodes: sec.controlPlane.length + sec.workers.length,
    volumes: sec.volumes.length,
  }
  const sections: { id: string; title: string; what: string; rows: AlertRow[]; show: boolean }[] = [
    { id: 'pods', title: 'Pods', what: 'pod', rows: sec.pods.filter(isActive), show: kind === 'all' || kind === 'pods' },
    { id: 'control-plane', title: 'Control-plane nodes', what: 'control-plane', rows: sec.controlPlane, show: kind === 'all' || kind === 'nodes' },
    { id: 'workers', title: 'Worker nodes', what: 'worker', rows: sec.workers, show: kind === 'all' || kind === 'nodes' },
    { id: 'volumes', title: 'Persistent volumes and claims', what: 'volume', rows: sec.volumes, show: kind === 'all' || kind === 'volumes' },
  ]
  const recovered = { id: 'recovered', title: 'Resolved', what: 'resolved', rows: sec.pods.filter((r) => !isActive(r)), show: kind === 'all' || kind === 'pods' }
  // What is going on first, then the pods that recovered, then the groups
  // with nothing to show.
  const ordered = [...sections.filter((s) => s.rows.length), ...(recovered.rows.length ? [recovered] : []), ...sections.filter((s) => !s.rows.length)]
  const visible = ordered.filter((s) => s.show).flatMap((s) => s.rows)
  const hiddenRecovered = activeOnly ? rows.filter((r) => !isActive(r) && (!q || searchText(r).includes(q))).length : 0
  const hiddenAcked = hideAcked ? rows.filter((r) => r.ack && (!activeOnly || isActive(r)) && (!q || searchText(r).includes(q))).length : 0

  // The object's details over this tab.
  const openRow = (r: AlertRow) => {
    const target: Peek = r.kind === 'node' ? { kind: 'node', name: r.name } : r.kind === 'pod' ? { kind: 'pod', namespace: r.namespace!, name: r.name } : { kind: 'object', type: 'PersistentVolumeClaim', namespace: r.namespace!, name: r.name }
    navigate(peekHref(target))
  }
  const openPod = (key: string) => {
    const [ns = '', name = ''] = key.split('/', 2)
    navigate(peekHref({ kind: 'pod', namespace: ns, name }))
  }
  const ack = async (r: AlertRow, on: boolean) => {
    try {
      await acknowledge(route.cluster, r.key, on)
      onToast(on ? `Acknowledged ${r.namespace ? r.namespace + '/' : ''}${r.name}: it is off the banners for everyone until it changes or clears.` : `${r.namespace ? r.namespace + '/' : ''}${r.name} is back on the banners.`)
    } catch (e) {
      onToast(`Not saved: ${(e as Error).message}`)
    }
  }

  return (
    <section className="rk-pane" aria-label="Alerts" style={{ padding: '20px 24px 32px', display: 'flex', flexDirection: 'column', gap: 14, flex: '1 1 auto', minWidth: 0 }}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        <h1 style={{ margin: 0, fontSize: 18, fontWeight: 600, letterSpacing: '-0.01em' }}>Alerts</h1>
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
        <label className="rk-check" title="Leave out the pods that recovered after warnings" style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 12.5 }}>
          <input
            type="checkbox"
            checked={activeOnly}
            onChange={(e) => {
              set({ show: e.target.checked ? undefined : 'all' })
              // Shown, they come after the active ones: bring them into view, so the change is seen.
              if (!e.target.checked) requestAnimationFrame(() => document.querySelector('[data-testid="alert-section-recovered"]')?.scrollIntoView({ block: 'start', behavior: 'smooth' }))
            }}
            data-testid="alert-active"
          />
          Hide resolved
          {hiddenRecovered > 0 && <span className="rk-cnt" title={`${hiddenRecovered} resolved alerts not shown`}>{hiddenRecovered} hidden</span>}
        </label>
        <label className="rk-check" title="Leave out the alerts someone acknowledged" style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 12.5 }}>
          <input type="checkbox" checked={hideAcked} onChange={(e) => set({ acked: e.target.checked ? 'hide' : undefined })} data-testid="alert-hide-acked" />
          Hide acknowledged
          {hiddenAcked > 0 && <span className="rk-cnt" title={`${hiddenAcked} acknowledged alerts not shown`}>{hiddenAcked} hidden</span>}
        </label>
        <label className="rk-field" style={{ width: 260 }}>
          <SearchIcon />
          <span className="rk-sr">Search alerts</span>
          <input type="search" placeholder="Search objects or messages" value={query} onChange={(e) => set({ q: e.target.value || undefined })} data-testid="alert-search" />
        </label>
        <ExportMenu onExport={(f) => exportEvents(visible, f, cluster?.name, 'alerts', onToast)} disabled={!visible.length} what="every event shown below" />
      </div>
      {!loaded && <p style={{ margin: 0, fontSize: 12.5, color: 'var(--fg3)' }}>Waiting for the first list of alerts…</p>}
      {loaded &&
        ordered
          .filter((s) => s.show)
          .map((s) => (
            <div key={s.id} style={{ display: 'flex', flexDirection: 'column', gap: 14 }} data-testid={`alert-section-${s.id}`}>
              <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginTop: 6 }}>
                <h2 className="rk-h">{s.title}</h2>
                <span style={{ fontSize: 12, color: 'var(--fg3)' }}>{s.rows.length}</span>
              </div>
              {s.rows.map((r) => (
                <AlertCard key={r.key} r={r} now={now} onOpen={() => openRow(r)} onPod={openPod} onExport={(f) => exportEvents([r], f, cluster?.name, `${r.kind}-${r.name}-events`, onToast)} onAck={(on) => ack(r, on)} />
              ))}
              {s.rows.length === 0 && (
                <p style={{ margin: 0, padding: '18px 16px', fontSize: 12.5, color: 'var(--fg3)', border: '1px dashed var(--line2)', borderRadius: 10 }} data-testid="alert-empty">
                  {q ? `No ${s.what} alerts match this search.` : activeOnly && rows.some((r) => !isActive(r) && sectionOf(r) === s.id) ? `No active ${s.what} alerts; the resolved ones are hidden.` : `No ${s.what} problems right now.`}
                </p>
              )}
            </div>
          ))}
    </section>
  )
}
