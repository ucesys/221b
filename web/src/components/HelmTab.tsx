import { useCallback, useMemo, useState } from 'react'
import { useDetails } from '../api/details'
import { useView } from '../api/live'
import type { ReleaseDetails, ReleaseRow } from '../api/types'
import { fmtAge, fmtTs } from '../lib/format'
import { apiPath, href, navigate, withParams } from '../lib/router'
import { CopyName } from './CopyName'
import { releaseItem, ReleaseBody, SUBTABS, type SubTab } from './ReleaseBody'
import { SideList, type SideGroup } from './SideList'
import type { TabProps } from './tabs'

type ReleaseFilter = 'all' | 'deployed' | 'failed'

/** The Helm tab (P2-06): releases on the left, the chosen one on the right with its pods, resources, values, notes and history. */
export function HelmTab({ cluster, now, onToast, route }: TabProps) {
  const { rows: releases, loaded } = useView<ReleaseRow>('helm')
  const p = route.params
  const [filter, setFilter] = useState<ReleaseFilter>('all')
  const [listQuery, setListQuery] = useState('')
  const [addons, setAddons] = useState(true)
  const sorted = useMemo(() => [...releases].sort((a, b) => a.name.localeCompare(b.name) || a.namespace.localeCompare(b.namespace)), [releases])
  const [wantNs, wantName] = route.path
  const current = sorted.find((r) => r.namespace === wantNs && r.name === wantName) ?? sorted.find((r) => r.group === 'apps') ?? sorted[0] ?? null
  const sub: SubTab = SUBTABS.includes(p.get('tab') as SubTab) ? (p.get('tab') as SubTab) : 'pods'
  const set = useCallback((changes: Record<string, string | string[] | undefined>) => navigate(withParams(route, changes), true), [route])
  const details = useDetails<ReleaseDetails>(current ? apiPath(`helm/${encodeURIComponent(current.namespace)}/${encodeURIComponent(current.name)}`) : null)
  const d = details.data

  const lq = listQuery.trim().toLowerCase()
  const listed = sorted.filter((r) => (filter === 'all' || (filter === 'deployed' ? r.status === 'deployed' : r.status === 'failed')) && (addons || r.group !== 'addons') && (!lq || r.name.includes(lq) || r.chart.includes(lq) || r.namespace.includes(lq)))
  const item = (r: ReleaseRow) => releaseItem(r, current?.key === r.key, (x) => href('helm', [x.namespace, x.name]), now, fmtAge)
  const groups: SideGroup[] = [
    { title: 'Applications', items: listed.filter((r) => r.group === 'apps').map(item) },
    { title: 'Cluster add-ons', items: listed.filter((r) => r.group === 'addons').map(item) },
  ]
  const segs: [ReleaseFilter, string, number][] = [
    ['all', 'All', releases.length],
    ['deployed', 'Deployed', releases.filter((r) => r.status === 'deployed').length],
    ['failed', 'Failed', releases.filter((r) => r.status === 'failed').length],
  ]

  return (
    <>
      <SideList
        label="Helm releases"
        title="Helm releases"
        aside={
          <div className="rk-seg" role="group" aria-label="Releases by status">
            {segs.map(([id, label, n]) => (
              <button key={id} aria-pressed={filter === id} onClick={() => setFilter(id)}>
                {label} <span style={{ color: 'var(--fg3)', fontWeight: 400 }}>{n}</span>
              </button>
            ))}
          </div>
        }
        query={listQuery}
        onQuery={setListQuery}
        placeholder="Filter by release, chart or namespace"
        extra={
          <label className="rk-hit" style={{ display: 'inline-flex', alignItems: 'center', gap: 8, cursor: 'pointer', fontSize: 12, color: 'var(--fg2)' }}>
            <input type="checkbox" checked={addons} onChange={(e) => setAddons(e.target.checked)} style={{ width: 15, height: 15, margin: 0, accentColor: 'var(--accent)', cursor: 'pointer' }} />
            <span>Show cluster add-ons</span>
          </label>
        }
        groups={groups}
        empty={loaded ? (releases.length ? 'No releases match this filter.' : 'No Helm releases in this cluster.') : 'Waiting for the first list…'}
        testId="release-list"
      />
      <main className="rk-pane" aria-label="Helm release" style={{ flex: '999 1 560px', minWidth: 0, display: 'flex', flexDirection: 'column' }}>
        {current && (
          <section aria-label={`Release ${current.name}`} style={{ padding: '18px 24px 28px', display: 'flex', flexDirection: 'column', gap: 14 }}>
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px 12px' }}>
              <h2 className="rk-mono" style={{ margin: 0, fontSize: 18, fontWeight: 600 }} data-testid="release-name">
                {current.name}
                <CopyName text={current.name} what="release name" />
              </h2>
              <span className="rk-pill" data-tone={current.tone} title={current.description}>
                <span className="rk-dot" />
                {current.status}
              </span>
              <span className="rk-tag" style={{ fontSize: 11, padding: '2px 7px' }}>
                revision {current.revision}
              </span>
            </div>
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px 18px', fontSize: 12.5, color: 'var(--fg3)' }} data-testid="release-facts">
              <span>
                Namespace{' '}
                <a className="rk-link rk-mono" href={href('namespaces', [current.namespace])} style={{ fontSize: 12.5 }}>
                  {current.namespace}
                </a>
              </span>
              <span>
                Chart <span className="rk-mono" style={{ color: 'var(--fg)' }}>{current.chart}-{current.chartVersion}</span>
              </span>
              <span>
                App version <span className="rk-mono" style={{ color: 'var(--fg)' }}>{current.appVersion || '—'}</span>
              </span>
              <span>
                Updated <span className="rk-num" style={{ color: 'var(--fg)' }}>{current.updated ? `${fmtAge(current.updated, now)} ago · ${fmtTs(current.updated)}` : '—'}</span>
              </span>
              <span>
                Managed by <span className="rk-mono" style={{ color: 'var(--fg)' }}>Helm</span>
              </span>
            </div>
            <ReleaseBody cluster={cluster} now={now} onToast={onToast} route={route} current={current} d={d} error={details.error} sub={sub} onSub={(id) => set({ tab: id === 'pods' ? undefined : id })} />
          </section>
        )}
        {!current && <p style={{ margin: 0, padding: '28px 24px', fontSize: 12.5, color: 'var(--fg3)' }}>{loaded ? 'No Helm releases: nothing in this cluster was installed with Helm, or 221B cannot read release records.' : 'Waiting for the first list of releases…'}</p>}
      </main>
    </>
  )
}
